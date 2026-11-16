#!/usr/bin/env bash
# Add LATENCY ms (default 500, jitter 100) on applier -> Spanner for N seconds.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
toxi POST /proxies/spanner_applier/toxics "{\"name\":\"lat\",\"type\":\"latency\",\"stream\":\"downstream\",\"attributes\":{\"latency\":${LATENCY:-500},\"jitter\":100}}"
sleep "${1:-30}"; toxi DELETE /proxies/spanner_applier/toxics/lat
