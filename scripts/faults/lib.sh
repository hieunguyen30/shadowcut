#!/usr/bin/env bash
# Shared helpers for manual fault injection (sc-exp exp4 automates the same faults).
TOXI=${TOXI:-http://localhost:18474}
toxi() { curl -sf -X "$1" -H 'Content-Type: application/json' "$TOXI$2" ${3:+-d "$3"} >/dev/null && echo "toxiproxy $1 $2 ok"; }
