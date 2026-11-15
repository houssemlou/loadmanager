#!/usr/bin/env bash
# Scenario 05 — asynchronous setpoint push (SSE).
# Why: when car B connects on CP2, CP1's setpoint drops although CP1 sent
# nothing; CP1 must be told. Decreases must be published before increases
# so the grid limit holds while chargers apply their new setpoints.
source "$(dirname "$0")/lib.sh"
wait_ready; reset_station

STREAM=$(mktemp)
trap 'kill $CURL_PID 2>/dev/null; rm -f "$STREAM"' EXIT

step "Subscribe to GET /setpoints/stream (all chargers)"
curl -sN "$BASE_URL/setpoints/stream" > "$STREAM" &
CURL_PID=$!
sleep 0.5

step "Car A connects on CP1/1, then car B on CP2/1"
start CP1 1 '{"vehicle_max_power_kw": 350}'
start CP2 1 '{"vehicle_max_power_kw": 350}'
sleep 0.5

step "Received SSE events (snapshot first, then setpoint changes)"
grep -E '^(event|data):' "$STREAM" | paste - - | sed 's/^/  /'

dec=$(grep -n '"charger_id":"CP1","connector_id":"1","setpoint_kw":200' "$STREAM" | head -1 | cut -d: -f1)
inc=$(grep -n '"charger_id":"CP2","connector_id":"1","setpoint_kw":200' "$STREAM" | head -1 | cut -d: -f1)
if [[ -n "$dec" ]]; then pass "CP1/1 was pushed its lowered setpoint (200) without asking"; else fail "no push for CP1/1 → 200"; fi
if [[ -n "$dec" && -n "$inc" && "$dec" -lt "$inc" ]]; then
  pass "decrease (CP1/1 300→200) published before increase (CP2/1 0→200)"
else
  fail "expected decrease before increase"
fi

finish
