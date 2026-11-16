#!/usr/bin/env bash
# Cut the applier <-> Spanner link for N seconds.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
toxi POST /proxies/spanner_applier '{"enabled":false}'; sleep "${1:-20}"; toxi POST /proxies/spanner_applier '{"enabled":true}'
