#!/usr/bin/env bash
# Registers (or updates) the Debezium Postgres connector. Credentials come from
# .env and are substituted at registration time, so none are stored in the repo.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
CONNECT=${CONNECT_URL:-http://localhost:18083}
for i in $(seq 1 60); do curl -sf "$CONNECT/connectors" >/dev/null && break; sleep 2; done
body=$(sed -e "s|\${POSTGRES_USER}|$POSTGRES_USER|" -e "s|\${POSTGRES_PASSWORD}|$POSTGRES_PASSWORD|" -e "s|\${POSTGRES_DB}|$POSTGRES_DB|" deploy/debezium/postgres-connector.json)
name=$(echo "$body" | grep -o '"name": *"[^"]*"' | head -1 | sed 's/.*: *"\(.*\)"/\1/')
config=$(echo "$body" | sed -n '/"config"/,$p' | sed '1s/.*"config": *//' | sed '$d')
if curl -sf "$CONNECT/connectors/$name" >/dev/null; then
  curl -sf -X PUT -H 'Content-Type: application/json' "$CONNECT/connectors/$name/config" -d "$config" >/dev/null
  echo "updated connector $name"
else
  curl -sf -X POST -H 'Content-Type: application/json' "$CONNECT/connectors" -d "$body" >/dev/null
  echo "created connector $name"
fi
for i in $(seq 1 30); do
  s=$(curl -s "$CONNECT/connectors/$name/status")
  echo "$s" | grep -q '"state":"RUNNING".*"tasks":\[{"id":0,"state":"RUNNING"' && { echo "connector RUNNING"; exit 0; }
  sleep 2
done
echo "connector not running: $s" >&2; exit 1
