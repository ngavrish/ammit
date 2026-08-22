# The server is Go; the image is what the actions need — a container client for
# the ones that restart something, curl for the ones that call a webhook — and
# the charts, because the charts are part of this service rather than a
# neighbour of it.
#
# They were a second container for a while, and every question about them was a
# question about two things: which project they belonged to, what happened when
# one came up without the other, whether a deploy could reach them. The page has
# always presented them as one product — the charts are a panel inside ammit's
# own page — so they are one container.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /ammit .

FROM grafana/grafana:11.3.0
USER root
RUN apk add --no-cache docker-cli curl ca-certificates

COPY --from=build /ammit /usr/local/bin/ammit
# Provisioning and the dashboard travel with the image: a chart that has to be
# mounted from somewhere is a chart that is missing on a machine nobody set up.
COPY deploy/grafana/provisioning /etc/grafana/provisioning
COPY deploy/grafana/dashboards /var/lib/grafana/dashboards

ENV AMMIT_DB=/data/ammit.db \
    AMMIT_CONFIG=/config/limits.yml \
    AMMIT_CHARTS_URL=http://localhost:3000 \
    GF_SECURITY_ALLOW_EMBEDDING=true \
    GF_AUTH_ANONYMOUS_ENABLED=true \
    GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer \
    GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH=/var/lib/grafana/dashboards/ammit.json \
    GF_INSTALL_PLUGINS=frser-sqlite-datasource

EXPOSE 8099 3000
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
