# The server is Go, the image is what the actions need: a container client for
# the ones that restart something, curl for the ones that call a webhook. Its
# whole state is one sqlite file on a volume.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /ammit .

FROM alpine:3.20
RUN apk add --no-cache docker-cli curl ca-certificates
COPY --from=build /ammit /usr/local/bin/ammit
ENV AMMIT_DB=/data/ammit.db AMMIT_CONFIG=/config/limits.yml
EXPOSE 8099
ENTRYPOINT ["ammit"]
