#!/usr/bin/env bash
# Scenario 04 — charger fault and recovery.
# Why: a faulted charger must not keep power reserved; its share goes to
# healthy chargers, OPS sees it unavailable, and it rejoins on recovery.
source "$(dirname "$0")/lib.sh"
wait_ready; reset_station

step "Car A on CP1/1, car B on CP2/1 → 200 / 200"
start CP1 1 '{"vehicle_max_power_kw": 300}'
start CP2 1 '{"vehicle_max_power_kw": 300}'
expect setpoint_kw 200

step "CP1 reports a fault → A gets 0, B takes 300"
call POST /chargers/CP1/status '{"status": "faulted"}'
expect_code 200; expect status faulted; expect allocated_kw 0
setpoint_of CP1 1 0
expect status unavailable
setpoint_of CP2 1 300

step "New car on faulted CP1/2 → rejected with 409"
start CP1 2 '{"vehicle_max_power_kw": 100}'
expect_code 409

step "CP1 recovers → A resumes, back to 200 / 200"
call POST /chargers/CP1/status '{"status": "available"}'
expect_code 200
setpoint_of CP1 1 200
setpoint_of CP2 1 200

step "Invalid inputs are rejected"
start CP1 2 '{"vehicle_max_power_kw": -1}'
expect_code 400
start CP1 9 '{"vehicle_max_power_kw": 10}'
expect_code 404
update CP1 1 '{"soc_pct": 150}'
expect_code 400

finish
