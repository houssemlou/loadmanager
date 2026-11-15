#!/usr/bin/env bash
# Shared helpers for scenario scripts. Requires only bash + curl.
# Scenarios target configs/example-station.json (400 kW grid, CP1/CP2 × 300 kW, connectors 1 and 2).

set -uo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
FAILS=0
CODE=""
BODY=""

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1;34m▶ %s\033[0m\n' "$*"; }
pass()  { printf '  \033[32m✔ %s\033[0m\n' "$*"; }
fail()  { printf '  \033[31m✘ %s\033[0m\n' "$*"; FAILS=$((FAILS + 1)); }

# call METHOD PATH [JSON_BODY] → sets CODE and BODY, prints the exchange.
call() {
  local method=$1 path=$2 body=${3:-} resp
  if [[ -n "$body" ]]; then
    resp=$(curl -sS -X "$method" -H 'Content-Type: application/json' -d "$body" -w $'\n%{http_code}' "$BASE_URL$path")
  else
    resp=$(curl -sS -X "$method" -w $'\n%{http_code}' "$BASE_URL$path")
  fi
  CODE=${resp##*$'\n'}
  BODY=${resp%$'\n'*}
  printf '  %s %s %s\n    → %s %s\n' "$method" "$path" "${body:+$body}" "$CODE" "$BODY"
}

expect_code() {
  if [[ "$CODE" == "$1" ]]; then pass "HTTP $1"; else fail "expected HTTP $1, got $CODE"; fi
}

# expect FIELD VALUE: the last response body contains "FIELD":VALUE (compact JSON).
expect() {
  if printf '%s' "$BODY" | grep -Eq "\"$1\":\"?$2\"?[,}]"; then pass "$1 = $2"; else fail "expected $1 = $2"; fi
}

# setpoint_of CHARGER CONNECTOR EXPECTED: polls the connector and checks its setpoint.
setpoint_of() {
  call GET "/chargers/$1/connectors/$2"
  expect setpoint_kw "$3"
}

start()  { call POST   "/chargers/$1/connectors/$2/session" "$3"; }
update() { call PATCH  "/chargers/$1/connectors/$2/session" "$3"; }
stop()   { call DELETE "/chargers/$1/connectors/$2/session"; }

wait_ready() {
  for _ in $(seq 1 30); do
    curl -sf "$BASE_URL/healthz" >/dev/null && return 0
    sleep 0.5
  done
  echo "Service not reachable at $BASE_URL (start it with: docker compose up --build)" >&2
  exit 1
}

# reset_station: bring the example station back to idle (no reset endpoint by design).
reset_station() {
  for ch in CP1 CP2; do
    curl -s -o /dev/null -X POST -H 'Content-Type: application/json' -d '{"status":"available"}' "$BASE_URL/chargers/$ch/status"
    for n in 1 2; do
      curl -s -o /dev/null -X DELETE "$BASE_URL/chargers/$ch/connectors/$n/session"
    done
  done
}

finish() {
  echo
  if [[ $FAILS -eq 0 ]]; then bold "✅ $(basename "$0"): all checks passed"; else bold "❌ $(basename "$0"): $FAILS check(s) failed"; fi
  exit "$FAILS"
}
