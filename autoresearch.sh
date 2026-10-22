#!/usr/bin/env bash
set -euo pipefail

log_file="$(mktemp)"
trap 'rm -f "$log_file"' EXIT

if go test -json ./... >"$log_file"; then
  passed_packages="$(jq -s '[.[] | select(.Action == "pass" and has("Package"))] | length' "$log_file")"
  printf 'METRIC passing_go_packages=%s\n' "$passed_packages"
else
  cat "$log_file"
  exit 1
fi
