#!/usr/bin/env bash
# Kill Kafka Connect (Debezium) for N seconds, then restart it.
set -euo pipefail
docker kill shadowcut-connect-1 && sleep "${1:-20}" && docker start shadowcut-connect-1
