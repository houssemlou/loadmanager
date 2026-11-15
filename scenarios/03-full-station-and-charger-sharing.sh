#!/usr/bin/env bash
# Scenario 03 — saturated station and charger-level sharing.
# Why: with every connector busy the grid is the bottleneck; two connectors
# on one charger must share that charger's 300 kW; rounding must never
# make the total exceed the grid.
source "$(dirname "$0")/lib.sh"
wait_ready; reset_station

step "Three cars (300 kW each): CP1/1, CP1/2, CP2/1 → 400/3 = 133.3 each (rounded down)"
start CP1 1 '{"vehicle_max_power_kw": 300}'
start CP1 2 '{"vehicle_max_power_kw": 300}'
start CP2 1 '{"vehicle_max_power_kw": 300}'
expect setpoint_kw 133.3
call GET /status
expect active_sessions 3; expect headroom_kw 0.1

step "Fourth car on CP2/2 → 100 each, unserved demand is visible to OPS"
start CP2 2 '{"vehicle_max_power_kw": 300}'
expect setpoint_kw 100
call GET /status
expect headroom_kw 0; expect unserved_demand_kw 800

step "CP2 cars leave → the two CP1 cars share CP1's 300 kW: 150 each (charger limit, not grid)"
stop CP2 1
stop CP2 2
setpoint_of CP1 1 150
setpoint_of CP1 2 150
call GET /status
expect headroom_kw 100

finish
