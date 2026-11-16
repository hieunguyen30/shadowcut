#!/usr/bin/env bash
# Pending cloud experiments for Shadowcut. NOT RUN: written so they can be executed
# as-is by someone with a GCP project and billing. Every step prints what it does.
#
# Prereqs: gcloud (authenticated: `gcloud auth login && gcloud auth application-default login`),
# terraform >= 1.6, docker, go. Set PROJECT (and optionally REGION).
#
#   PROJECT=my-project ./scripts/cloud/run-pending.sh all
#   PROJECT=my-project ./scripts/cloud/run-pending.sh exp3        # one step
#   PROJECT=my-project ./scripts/cloud/run-pending.sh destroy     # ALWAYS run when done
#
# Estimated cost (us-central1, list prices; verify on cloud.google.com/pricing):
#   Spanner 1000 PU (1 node) regional  ~ $0.90/h        x ~4 h  ~ $3.60
#   Spanner storage (< 10 GB)          ~ $0.30/GB-month (prorated)       ~ $0.05
#   Cloud SQL db-custom-2-7680         ~ $0.13/h        x ~4 h  ~ $0.55
#   Cloud Run service + jobs           ~ $0.10 - $1
#   Datastream (CDC, < 1 GB)           ~ $2/GB processed        ~ $2
#   Dataflow (Datastream-to-Spanner)   2 x n1-standard-2 ~ $0.25/h x 2 h ~ $0.50
#   Artifact Registry / egress         < $0.50
#   TOTAL for one ~4 h session         ~ $8 - $12 (destroy afterwards!)
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${PROJECT:?set PROJECT}"
REGION=${REGION:-us-central1}
TAG=${TAG:-$(git rev-parse --short HEAD)}
IMAGE="$REGION-docker.pkg.dev/$PROJECT/shadowcut/shadowcut:$TAG"
OUT=results/cloud/$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$OUT"
say() { printf '\n==> %s\n' "$*"; }

infra() {
  say "terraform apply (Spanner PG-dialect DB, Cloud SQL PG16, Cloud Run, Artifact Registry)"
  terraform -chdir=terraform init
  terraform -chdir=terraform apply -var project_id="$PROJECT" -var region="$REGION" -var image_tag="$TAG"
  say "build and push $IMAGE"
  gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet
  docker build -f deploy/Dockerfile -t "$IMAGE" . && docker push "$IMAGE"
}

pgadapter() {
  # Local PGAdapter against REAL Spanner using your application-default credentials.
  say "starting local PGAdapter on :25433 for $PROJECT/shadowcut/wiki"
  docker rm -f sc-pgadapter-real >/dev/null 2>&1 || true
  docker run -d --name sc-pgadapter-real -p 25433:5432 \
    -v "$HOME/.config/gcloud:/gcloud:ro" -e GOOGLE_APPLICATION_CREDENTIALS=/gcloud/application_default_credentials.json \
    gcr.io/cloud-spanner-pg-adapter/pgadapter:latest -p "$PROJECT" -i shadowcut -d wiki -x
  sleep 8
  export SPANNER_DSN="postgres://localhost:25433/wiki?sslmode=disable"
}

schema() {
  pgadapter
  say "apply schema/spanner.sql to real Spanner"
  go run ./cmd/shadowcut schema apply --spanner "$SPANNER_DSN"
}

exp3() {
  # Hotspotting: sequential vs bit-reversed vs UUID keys under identical load.
  say "Experiment 3 via Cloud Run job (32 writers x 2 min per strategy)"
  gcloud run jobs execute shadowcut-exp --region "$REGION" --wait \
    --args="sc-exp,exp3,--dsn,postgres://localhost:5433/wiki?sslmode=disable,--writers,32,--duration,2m,--env,REAL Spanner 1000 PU $REGION,--out,/tmp/exp3"
  say "Also run from the workstation for raw files (latency includes WAN RTT; compare relative numbers only):"
  pgadapter
  go run ./cmd/sc-exp exp3 --dsn "$SPANNER_DSN" --writers 32 --duration 2m --env "REAL Spanner via local PGAdapter (WAN)" --out "$OUT/exp3"
}

confirm() {
  # Re-run every emulator-derived finding against real Spanner.
  pgadapter
  say "feature probe on real Spanner (confirms collation, NULL ordering, time zones, unsupported features)"
  SPANNER_DSN=$SPANNER_DSN go run ./cmd/sc-exp probe --out "$OUT/probe" --evidence ""
  diff <(cut -d, -f1-4 results/probe/probe.csv) <(cut -d, -f1-4 "$OUT/probe/probe.csv") > "$OUT/probe/emulator-vs-real.diff" || true
  say "curated hazard replay + Experiment 1 against real Spanner (needs the dataset replicated; see 'baseline')"
  SPANNER_DSN=$SPANNER_DSN go run ./cmd/sc-exp exp1 --out "$OUT/exp1"
}

baseline() {
  # Managed baseline: Datastream + Dataflow (Spanner migration tool's minimal-downtime path)
  # vs our Debezium -> Kafka -> applier. Same source (Cloud SQL), same workload.
  say "load data into Cloud SQL (live Wikipedia for 30 min) through the Cloud SQL connector"
  echo "  run: gcloud sql connect shadowcut-src --user=shadowcut   (then \\i schema/postgres.sql)"
  echo "  run: PG_DSN=<cloudsql dsn> go run ./cmd/sc-ingest wiki --duration 30m"
  say "Spanner migration tool, minimal-downtime migration (Datastream + Dataflow)"
  echo "  install: gcloud components install spanner-migration-tool"
  echo "  run:     gcloud alpha spanner migrate schema-and-data --source=postgresql --target-profile='instance=shadowcut,dbName=wiki_smt,dialect=postgresql' \\"
  echo "           --source-profile='host=<cloudsql-ip>,port=5432,user=shadowcut,dbName=wiki,streamingCfg=streaming.json'"
  say "measure: replication lag (sc_heartbeat), lost/duplicate rows (sc-exp exp4 probe writer), and"
  echo "  verifier drift on both targets:"
  echo "  shadowcut verify --spanner postgres://localhost:25433/wiki_smt --rounds 5 --sample 0.1 --out $OUT/baseline-smt"
  echo "  shadowcut verify --spanner postgres://localhost:25433/wiki     --rounds 5 --sample 0.1 --out $OUT/baseline-debezium"
}

destroy() {
  say "terraform destroy"
  docker rm -f sc-pgadapter-real >/dev/null 2>&1 || true
  terraform -chdir=terraform destroy -var project_id="$PROJECT" -var region="$REGION"
}

case "${1:-all}" in
  infra) infra ;;
  schema) schema ;;
  exp3) exp3 ;;
  confirm) confirm ;;
  baseline) baseline ;;
  destroy) destroy ;;
  all) infra; schema; exp3; confirm; baseline; echo; echo "results in $OUT - now run: $0 destroy" ;;
  *) echo "usage: $0 {all|infra|schema|exp3|confirm|baseline|destroy}"; exit 2 ;;
esac
