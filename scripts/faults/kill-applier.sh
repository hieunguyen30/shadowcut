#!/usr/bin/env bash
# Kill the CDC applier for N seconds (default 20), then restart it.
set -euo pipefail
docker kill shadowcut-applier-1 && sleep "${1:-20}" && docker start shadowcut-applier-1
