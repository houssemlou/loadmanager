#!/usr/bin/env bash
# Scenario 02 — vehicle limits, charging curve taper, driver cap.
# Why: power a vehicle cannot use must flow to the others (water-filling),
# and demand must follow the charging curve as SoC rises.
#   Car B curve: 150 kW flat to 50 %, then 50 kW at 80 %, 10 kW at 100 %.
source "$(dirname "$0")/lib.sh"
wait_ready; reset_station

CURVE='[{"soc_pct":0,"max_power_kw":150},{"soc_pct":50,"max_power_kw":150},{"soc_pct":80,"max_power_kw":50},{"soc_pct":100,"max_power_kw":10}]'

step "Car A (300 kW) on CP1/1, alone → 300"
start CP1 1 '{"vehicle_max_power_kw": 300}'
expect setpoint_kw 300

step "Car B (curve, SoC 20 %) on CP2/1 → B limited by curve to 150, A gets the rest: 250"
start CP2 1 "{\"vehicle_max_power_kw\": 300, \"soc_pct\": 20, \"charging_curve\": $CURVE}"
expect demand_kw 150; expect setpoint_kw 150
setpoint_of CP1 1 250

step "Car B reaches 65 % → curve gives 100; A capped by its charger at 300"
update CP2 1 '{"soc_pct": 65}'
expect setpoint_kw 100
setpoint_of CP1 1 300

step "Car B reaches 90 % → curve gives 30"
update CP2 1 '{"soc_pct": 90}'
expect setpoint_kw 30

step "Driver of car A caps power at 120 kW (e.g. battery preconditioning) → A 120, B stays 30"
update CP1 1 '{"requested_power_kw": 120}'
expect demand_kw 120; expect setpoint_kw 120
setpoint_of CP2 1 30

finish
