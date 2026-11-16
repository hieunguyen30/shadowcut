#!/usr/bin/env bash
# Undo every fault: re-enable proxies, drop toxics, start containers.
. "$(dirname "$0")/lib.sh"
for p in kafka_applier spanner_applier spanner_pg; do
  toxi POST /proxies/$p '{"enabled":true}' || true
  toxi DELETE /proxies/$p/toxics/lat 2>/dev/null || true
done
docker start shadowcut-applier-1 shadowcut-connect-1 >/dev/null 2>&1 || true
docker unpause shadowcut-ingest-wiki-1 >/dev/null 2>&1 || true
echo healed
