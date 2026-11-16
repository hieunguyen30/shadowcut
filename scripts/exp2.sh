#!/usr/bin/env bash
# Experiment 2: behaviour parity on the live Wikipedia workload.
# Runs the shadow proxy + demo app in Docker, drives k6 read traffic for DURATION,
# then summarizes state/shadow into results/exp2.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
DURATION=${DURATION:-10m}
C="docker compose -f deploy/docker-compose.yml --env-file .env"
rm -rf state/shadow && mkdir -p state/shadow results/exp2
$C --profile full up -d --force-recreate shadow demoapp
for i in $(seq 1 60); do curl -sf localhost:18080/healthz >/dev/null && break; sleep 1; done
MSYS_NO_PATHCONV=1 docker run --rm --network shadowcut_default -v "$(pwd -W 2>/dev/null || pwd)/deploy/k6:/k6" \
  -e DURATION="$DURATION" grafana/k6:latest run --summary-export /k6/exp2-k6-summary.json /k6/exp2.js | tail -40 | tee results/exp2/k6.txt
mv deploy/k6/exp2-k6-summary.json results/exp2/k6-summary.json 2>/dev/null || true
sleep 20   # let queued shadow comparisons finish
go run ./cmd/sc-exp exp2 --state state/shadow --out results/exp2
