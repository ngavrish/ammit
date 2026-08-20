# The most boring container in the stack, on purpose: python, a container client
# for the actions that want one, curl for the actions that want that. Its whole
# state is one sqlite file.
FROM python:3.12-alpine

RUN apk add --no-cache docker-cli curl

COPY server/server.py /app/server.py

ENV PYTHONUNBUFFERED=1 AMMIT_DB=/data/ammit.db AMMIT_CONFIG=/config/limits.yml
EXPOSE 8099
ENTRYPOINT ["python3", "/app/server.py"]
