#!/usr/bin/env bash
# Quick end-to-end check of the public edge.
. "$(dirname "$0")/lib.sh"
require curl
BASE="${BASE:-http://localhost:8080}"

check() {
  local name=$1 url=$2 want=${3:-200} code
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$url" || true)
  if [ "$code" = "$want" ]; then ok "$name ($code)"; else die "$name: got $code, want $want ($url)"; fi
}

check "edge health"  "$BASE/nginx-health"
check "ping"         "$BASE/api/v1/ping"
check "stats"        "$BASE/api/v1/stats"
check "item (cache)" "$BASE/api/v1/items/1"
check "dashboard"    "$BASE/"

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"type":"smoke.test","data":{"ok":true}}' "$BASE/api/v1/events" || true)
if [ "$code" = "202" ]; then ok "publish event (202)"; else die "publish event: got $code"; fi
