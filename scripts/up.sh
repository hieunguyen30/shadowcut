#!/usr/bin/env bash
# Bring up the whole local stack from scratch:
#   Postgres + Spanner emulator/PGAdapter + Kafka + Debezium + applier + live ingest
#   (+ Prometheus/Grafana with OBS=1, + shadow proxy and demo app with FULL=1)
set -euo pipefail
cd "$(dirname "$0")/.."
[ -f .env ] || { cp .env.example .env; echo "created .env from .env.example - set WIKI_UA_CONTACT"; }
set -a; . ./.env; set +a
C="docker compose -f deploy/docker-compose.yml --env-file .env"
$C up -d postgres spanner kafka connect toxiproxy
echo "waiting for PGAdapter..."; for i in $(seq 1 60); do docker run --rm --network shadowcut_default postgres:16 pg_isready -h spanner -q && break; sleep 2; done
go run ./cmd/shadowcut schema apply
./scripts/register-connector.sh
$C --profile full --profile cdc --profile ingest build
$C --profile cdc --profile ingest up -d applier ingest-wiki
[ "${OBS:-0}" = 1 ] && $C --profile obs up -d prometheus grafana
[ "${FULL:-0}" = 1 ] && $C --profile full up -d shadow demoapp
echo "stack is up: Postgres :15432, PGAdapter :15433, Kafka :19094, Connect :18083, Grafana :13000, demo app :18080"
