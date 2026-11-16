#!/usr/bin/env bash
# Cut the applier <-> Kafka link for N seconds.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
toxi POST /proxies/kafka_applier '{"enabled":false}'; sleep "${1:-20}"; toxi POST /proxies/kafka_applier '{"enabled":true}'
