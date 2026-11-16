#!/usr/bin/env bash
# Stop the stack (add -v to also delete the Postgres volume).
cd "$(dirname "$0")/.."
docker compose -f deploy/docker-compose.yml --env-file .env --profile full --profile obs --profile cdc --profile ingest down "$@"
