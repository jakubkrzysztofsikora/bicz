#!/usr/bin/env bash
set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

go test -count=1 ./...

go build -trimpath -o "$tmp_dir/bicz" ./cmd/bicz
BICZ_CONFIG_DIR="$tmp_dir/config" "$tmp_dir/bicz" rehearsal \
  --db "$tmp_dir/ledgers" \
  --record "$tmp_dir/rehearsal.json" \
  --json >"$tmp_dir/report.json"

jq -e '
  .protocol == "bicz.rehearsal/1"
  and .simulation_only == true
  and .phase_b_evidence == null
  and .total_scenarios == 10
  and (.scenario_ids | sort) == ([
    "bankruptcy_keeps_operator_channel",
    "compensation_restores_exactly_once",
    "confirmed_breach_burns_and_teaches",
    "concurrent_reservations_cannot_overspend",
    "dismissal_blocks_despite_balance",
    "honest_failure_is_free",
    "integrity_breach_denies_immediately",
    "lesson_transfer_survives_reopen",
    "loss_shortfall_recovers",
    "tampered_event_detected"
  ] | sort)
' "$tmp_dir/report.json" >/dev/null

printf 'Workload: fixed Phase A rehearsal over shipped Go ledger/protocol/daemon; no model calls, network, or subjective-state claims\n'
jq -r '"METRIC rehearsal_failures=\(.failed)"' "$tmp_dir/report.json"
jq -r '"METRIC rehearsal_checks=\(.total_scenarios)"' "$tmp_dir/report.json"
jq -r '"METRIC rehearsal_pass_rate=\(100 * .passed / .total_scenarios)"' "$tmp_dir/report.json"
printf 'ASI workload=fixed-ten-phase-a-scenarios-over-go-runtime\n'