#!/usr/bin/env bash
# Rebuild the Spanner target from the CDC history retained in Kafka.
#
# Use when the target is lost or wedged (the emulator keeps data in memory and can
# be left with a stuck read-write transaction when a client dies mid-transaction,
# docs/BUGS.md BUG-009). Steps: restart the emulator, re-apply the schema, rewind
# the applier's consumer group to the beginning, clear the exactly-once guard,
# replay, and compare row counts. Measured: 534,749 records in 112 s (EXPERIMENTS.md).
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
set -a; . ./.env; set +a
K="docker exec shadowcut-kafka-1 /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --group sc-applier"
psql_sp() { docker run --rm --network shadowcut_default postgres:16 psql -h spanner -d wiki -tAc "$1"; }

echo "1/6 stop applier"; docker stop -t 30 shadowcut-applier-1 >/dev/null || true
echo "2/6 restart emulator + PGAdapter"; docker restart shadowcut-spanner-1 >/dev/null
for i in $(seq 1 60); do docker run --rm --network shadowcut_default postgres:16 pg_isready -h spanner -q && break; sleep 2; done
echo "3/6 apply schema"; go run ./cmd/shadowcut schema apply
echo "4/6 rewind consumer group (waiting for it to become empty)"
for i in $(seq 1 30); do $K --reset-offsets --to-earliest --all-topics --execute >/dev/null 2>&1 && break; sleep 5; done
psql_sp "DELETE FROM sc_offsets" >/dev/null; psql_sp "DELETE FROM sc_apply_audit" >/dev/null
total=$($K --describe 2>/dev/null | awk 'NR>1 && $6 ~ /^[0-9]+$/ {s+=$6} END {print s+0}')
echo "5/6 replay $total records"; start=$(date +%s); docker start shadowcut-applier-1 >/dev/null
while true; do
  lag=$($K --describe 2>/dev/null | awk 'NR>1 && $6 ~ /^[0-9]+$/ {s+=$6} END {print s+0}')
  [ "$lag" -lt 100 ] && break; sleep 5
done
echo "    replayed in $(( $(date +%s) - start ))s"
echo "6/6 row counts (postgres vs spanner)"
for t in edits pages gh_events probe_events; do
  a=$(docker exec shadowcut-postgres-1 psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "select count(*) from $t")
  b=$(psql_sp "select count(*) from $t")
  echo "    $t: $a vs $b"
done
