#!/usr/bin/env bash
# Scenario 01 — core sharing flow: arrive alone, share, leave.
#   1. A lone car takes all the power it can (capped by its 300 kW charger).
#   2. A second car arrives → the 400 kW grid is shared 200 / 200.
#   3. The first car leaves → the second takes all it can (300).
source "$(dirname "$0")/lib.sh"
wait_ready; reset_station

step "Car A (wants 350 kW) connects alone on CP1/1 → gets 300 (charger limit)"
start CP1 1 '{"vehicle_max_power_kw": 350}'
expect_code 201; expect setpoint_kw 300

step "Car B (wants 350 kW) connects on CP2/1 → grid shared 200 / 200"
start CP2 1 '{"vehicle_max_power_kw": 350}'
expect_code 201; expect setpoint_kw 200
setpoint_of CP1 1 200

step "Car A leaves → Car B takes 300"
stop CP1 1
expect_code 200
setpoint_of CP2 1 300
setpoint_of CP1 1 0

step "OPS status"
call GET /status
expect active_sessions 1; expect headroom_kw 100

finish
