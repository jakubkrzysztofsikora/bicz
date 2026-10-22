#!/bin/sh
# Reproducible adapter smoke: real `bicz serve` daemon + real `bicz hook` CLI,
# on a throwaway HOME / ledger / socket. It never touches your real ~/.claude,
# ~/.codex, ~/.config/opencode, ~/.omp, or any credential file.
#
#   scripts/smoke.sh [path-to-bicz]      (default: builds ./cmd/bicz into a temp dir)
#
# Needs: jq. Optional: node >= 22.6 (runs the TS mock-harness section).
# What it proves: Claude/Codex hook stdin->stdout through a live daemon, priced
# from the published example schedule (docs/schedule.example.json), deny on
# insufficient credit, duplicate settle replay, unreachable-daemon notice/block,
# installer merge + exact uninstall restore. What it does NOT prove: any real
# Claude Code / Codex / OMP / OpenCode process ever calling these hooks.
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$here")"
command -v jq >/dev/null 2>&1 || { echo "smoke: jq is required" >&2; exit 1; }
# Short path: unix socket paths are length-limited.
tmp="$(mktemp -d /tmp/bicz-smoke.XXXXXX)"
daemon_pid=""
cleanup() {
  if [ -n "$daemon_pid" ]; then kill "$daemon_pid" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

bin="${1:-}"
if [ -z "$bin" ]; then
  bin="$tmp/bicz"
  (cd "$root" && go build -trimpath -o "$bin" ./cmd/bicz)
fi

pass=0
failn=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL %s\n' "$1"; }
check() { # check <name> <condition command...>
  name="$1"; shift
  if "$@"; then ok "$name"; else bad "$name"; fi
}
show() { printf '     %s\n' "$1"; }

export HOME="$tmp/home"
mkdir -p "$HOME" "$tmp/rt"
unset BICZ_AUTHORITY BICZ_ACTOR BICZ_OPERATOR CLAUDE_CONFIG_DIR BICZ_CONFIG BICZ_CONFIG_DIR \
  XDG_CONFIG_HOME BICZ_STATE_DIR XDG_STATE_HOME OPENCODE_CONFIG_DIR
export CODEX_HOME="$HOME/.codex"
export BICZ_RUNTIME_DIR="$tmp/rt"
ledger="$tmp/ledger.sqlite"
cfgdir="$HOME/.config/bicz"

# --- ledger, identity, published schedule ------------------------------------
"$bin" init --db "$ledger" >/dev/null
"$bin" open --db "$ledger" --identity alice --capital 120000 --reason "smoke identity" >/dev/null
# The example schedule prices identities provisioned at its own initial capital,
# and the guard band is measured against that capital (docs/CONTRACTS.md §6.3).
# This harness deliberately drains a tiny identity to prove fail-closed denies,
# which would put it inside the band and cap ordinary attempts — a behaviour
# with its own unit tests and its own Phase A scenario. Publish the shipped
# example with the band disabled so this harness keeps proving hook plumbing.
schedule="$tmp/schedule.json"
jq '.guard_band_bp = 0' "$root/docs/schedule.example.json" >"$schedule"
"$bin" price --db "$ledger" --version 1 --policy "$schedule" --reason "smoke: publish example schedule" >/dev/null
check "schedule published (bicz price)" test "$("$bin" status --db "$ledger" --identity alice | jq -r .balance)" = 120000

# --- installers write only under the temp HOME ---------------------------------
mkdir -p "$HOME/.claude"
printf '{\n  "model": "opus",\n  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/other-audit"}]}]}\n}\n' >"$HOME/.claude/settings.json"
cp "$HOME/.claude/settings.json" "$tmp/settings.before"
if "$bin" install --harness claude --account alice --ledger "$ledger" --dry-run | grep -q 'bicz hook --harness claude'; then ok "claude install --dry-run shows the diff"; else bad "claude dry-run"; fi
check "dry-run wrote nothing" cmp -s "$HOME/.claude/settings.json" "$tmp/settings.before"
"$bin" install --harness claude --account alice --ledger "$ledger" >/dev/null
"$bin" install --harness codex --account alice --ledger "$ledger" >/dev/null
check "claude merge kept the unrelated hook and key" sh -c "jq -e '.model==\"opus\" and ([.hooks.PreToolUse[].hooks[].command]|any(.==\"/usr/local/bin/other-audit\"))' '$HOME/.claude/settings.json' >/dev/null"
check "claude + codex hook entries present" sh -c "grep -q 'bicz hook --harness claude' '$HOME/.claude/settings.json' && grep -q 'bicz hook --harness codex' '$CODEX_HOME/hooks.json'"
second="$("$bin" install --harness claude --account alice --ledger "$ledger")"
check "second install is a no-op" sh -c "printf '%s' '$second' | grep -q 'up-to-date'"

# Point both adapters at the published schedule (see docs/SETUP.md: "Schedule").
for h in claude codex; do
  jq --arg p "$root/docs/schedule.example.json" '.schedule_path=$p | .policy_version="1"' "$cfgdir/$h.json" >"$cfgdir/$h.json.new"
  mv "$cfgdir/$h.json.new" "$cfgdir/$h.json"
done

# wait_ready LOG: the supervisor prints "socket=..." once it listens.
wait_ready() {
  n=0
  until grep -q 'socket=' "$1" 2>/dev/null || [ $n -ge 50 ]; do sleep 0.2; n=$((n + 1)); done
  sleep 0.3
}

# --- live daemon ------------------------------------------------------------------
"$bin" serve --db "$ledger" 2>"$tmp/serve.log" &
daemon_pid=$!
wait_ready "$tmp/serve.log"

hook() { printf '%s' "$3" | "$bin" hook --harness "$1" --event "$2"; }
pre='{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_01","tool_input":{"command":"ls"}}'
post='{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"toolu_01","tool_input":{"command":"ls"},"tool_response":{"stdout":"x"}}'
balance() { "$bin" status --db "$ledger" --identity alice | jq -c '{balance,held}'; }

out="$(hook claude SessionStart '{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}')"
check "claude SessionStart injects the projection" sh -c "printf '%s' '$out' | jq -e '.hookSpecificOutput.additionalContext|test(\"identity alice\")' >/dev/null"

out="$(hook claude PreToolUse "$pre")"
show "PreToolUse stdout: ${out:-<empty>}   ledger $(balance)"
check "claude PreToolUse allow emits no permissionDecision" sh -c "! printf '%s' '$out' | grep -q permissionDecision"
check "claude PreToolUse reserved the example price (Bash 50000 mc)" test "$(balance)" = '{"balance":120000,"held":50000}'

out="$(hook claude PostToolUse "$post")"
show "PostToolUse stdout: $out   ledger $(balance)"
check "claude PostToolUse settles 50000 mc and releases the hold" test "$(balance)" = '{"balance":70000,"held":0}'
dup="$(hook claude PostToolUse "$post")"
show "duplicate PostToolUse: $dup"
check "duplicate tool_use_id settles once (balance unchanged, replay marked)" sh -c "printf '%s' '$dup' | grep -q 'no second charge' && test '$(balance)' = '{\"balance\":70000,\"held\":0}'"

deny="$(hook claude PreToolUse '{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"WebFetch","tool_use_id":"toolu_02","tool_input":{"url":"https://example.invalid"}}')"
show "insufficient-credit stdout: $deny"
check "insufficient credit emits the deny JSON" sh -c "printf '%s' '$deny' | jq -e '.hookSpecificOutput.permissionDecision==\"deny\" and .hookSpecificOutput.hookEventName==\"PreToolUse\"' >/dev/null"
check "deny reserved nothing" test "$(balance)" = '{"balance":70000,"held":0}'

cpre='{"session_id":"c1","hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"call_01","tool_input":{"path":"a"}}'
cout="$(hook codex PreToolUse "$cpre")"
check "codex PreToolUse allow emits no permissionDecision, reserves Read 5000 mc" sh -c "! printf '%s' '$cout' | grep -q permissionDecision && test '$(balance)' = '{\"balance\":70000,\"held\":5000}'"
cpost="$(hook codex PostToolUse '{"session_id":"c1","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"call_01","tool_input":{"path":"a"},"tool_response":"ok"}')"
check "codex PostToolUse settles" sh -c "printf '%s' '$cpost' | grep -q 'charged 5000 mc' && test '$(balance)' = '{\"balance\":65000,\"held\":0}'"

# --- schedule republished between Pre and Post: settlement keeps the frozen quote ---
hook claude PreToolUse '{"session_id":"s2","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_03","tool_input":{"command":"pwd"}}' >/dev/null
jq '.tool_max_cost_mc.Bash=9000' "$root/docs/schedule.example.json" >"$tmp/changed-schedule.json"
jq --arg p "$tmp/changed-schedule.json" '.schedule_path=$p | .policy_version="2"' "$cfgdir/claude.json" >"$cfgdir/claude.json.new" && mv "$cfgdir/claude.json.new" "$cfgdir/claude.json"
hook claude PostToolUse '{"session_id":"s2","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"toolu_03","tool_input":{"command":"pwd"},"tool_response":"x"}' >/dev/null
check "schedule change between Pre and Post cannot change the charge (50000 frozen, not 9000)" test "$(balance)" = '{"balance":15000,"held":0}'

# --- automatic accountability cue: real `bicz loss`, real hooks, real daemon -----------
"$bin" loss --db "$ledger" --identity alice --rule smoke_admin --correction "none required" --evidence "scheduled debit" \
  --policy-version 1 --reason "smoke: administrative debit" --amount 1000 --event smoke-admin --disposition administrative >/dev/null
admin_pre='{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"toolu_p0","tool_input":{"path":"a"}}'
out="$(hook claude PreToolUse "$admin_pre")"
check "administrative debit emits no cue on the next PreToolUse" test -z "$out"
hook claude PostToolUse '{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"toolu_p0","tool_input":{"path":"a"},"tool_response":"x"}' >/dev/null
lossout="$("$bin" loss --db "$ledger" --identity alice --rule no_fake_tests --correction "run the real suite and report its true result" \
  --evidence "claimed a pass without running it" --policy-version 1 --reason "smoke: confirmed breach" --amount 1000 --event smoke-loss-1)"
show "operator: $(printf '%s' "$lossout" | tail -n 1)"
check "bare bicz loss issues the cue to the operator" sh -c "printf '%s' '$lossout' | grep -q 'ACCOUNTABILITY issued'"
out="$(hook claude PreToolUse '{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"toolu_p1","tool_input":{"path":"a"}}')"
show "live-session PreToolUse: $out"
check "the live session gets the cue before its next call, with no permissionDecision" sh -c "printf '%s' '$out' | jq -e '(.hookSpecificOutput.additionalContext|test(\"violated no_fake_tests\")) and (.hookSpecificOutput.additionalContext|test(\"1000 mc\")) and (.hookSpecificOutput|has(\"permissionDecision\")|not)' >/dev/null"
hook claude PostToolUse '{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"toolu_p1","tool_input":{"path":"a"},"tool_response":"x"}' >/dev/null
out="$(hook claude PreToolUse '{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"toolu_p2","tool_input":{"path":"a"}}')"
show "second PreToolUse: ${out:-<empty>}"
check "the same penalty is not delivered twice" sh -c "! printf '%s' '$out' | grep -q 'violated'"
hook claude PostToolUse '{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"toolu_p2","tool_input":{"path":"a"},"tool_response":"x"}' >/dev/null
out="$(hook claude SessionStart '{"session_id":"fresh","hook_event_name":"SessionStart"}')"
check "a restarted session gets the persisted penalty at SessionStart" sh -c "printf '%s' '$out' | jq -e '.hookSpecificOutput.additionalContext|test(\"violated no_fake_tests\")' >/dev/null"
replay="$("$bin" loss --db "$ledger" --identity alice --rule no_fake_tests --correction "run the real suite and report its true result" \
  --evidence "claimed a pass without running it" --policy-version 1 --reason "smoke: confirmed breach" --amount 1000 --event smoke-loss-1)"
check "an exact replay issues no second cue" sh -c "printf '%s' '$replay' | grep -q 'ACCOUNTABILITY none replayed=true'"
check "ledger still verifies with classified losses" sh -c "'$bin' verify --db '$ledger' | grep -q 'INTEGRITY OK'"

check "ledger verifies" sh -c "'$bin' verify --db '$ledger' | grep -q 'INTEGRITY OK'"

# --- daemon unreachable ----------------------------------------------------------------
kill "$daemon_pid" 2>/dev/null || true; wait "$daemon_pid" 2>/dev/null || true; daemon_pid=""
sleep 0.5
down='{"session_id":"s9","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_09","tool_input":{"command":"ls"}}'
start="$(date +%s)"
out="$(hook claude PreToolUse "$down")"
check "daemon down + notice: allow with 'NOT metered' context, no deny" sh -c "printf '%s' '$out' | grep -q 'NOT metered' && ! printf '%s' '$out' | grep -q permissionDecision"
jq '.unmanaged_mode="block"' "$cfgdir/claude.json" >"$cfgdir/claude.json.new" && mv "$cfgdir/claude.json.new" "$cfgdir/claude.json"
out="$(hook claude PreToolUse "$down")"
check "daemon down + block: deny daemon_unavailable" sh -c "printf '%s' '$out' | jq -e '.hookSpecificOutput.permissionDecision==\"deny\" and (.hookSpecificOutput.permissionDecisionReason|test(\"daemon_unavailable\"))' >/dev/null"
check "hooks returned within the 3s bound" test $(( $(date +%s) - start )) -le 6

# --- uninstall restores exactly -------------------------------------------------------------
"$bin" uninstall --harness claude >/dev/null
"$bin" uninstall --harness codex >/dev/null
check "uninstall restored claude settings byte-for-byte" cmp -s "$HOME/.claude/settings.json" "$tmp/settings.before"
check "codex hooks.json removed (bicz created it)" test ! -e "$CODEX_HOME/hooks.json"

# --- TS mock harness against a fresh real daemon -------------------------------------------------
if command -v node >/dev/null 2>&1 && node -e 'process.exit(+process.versions.node.split(".")[0] >= 22 ? 0 : 1)'; then
  "$bin" open --db "$ledger" --identity tsmock --capital 200000 --reason "smoke ts mock identity" >/dev/null
  "$bin" serve --db "$ledger" 2>"$tmp/serve2.log" &
  daemon_pid=$!
  wait_ready "$tmp/serve2.log"
  sock="$(sed -n 's/.*socket=\([^ ]*\).*/\1/p' "$tmp/serve2.log" | head -1)"
  if NODE_NO_WARNINGS=1 node "$here/smoke_ts_harness.mjs" --bicz "$bin" --ledger "$ledger" --root "$root" --tmp "$tmp" --sock "$sock"; then ok "TS mock harness (OMP + OpenCode V1) against the real daemon"; else bad "TS mock harness"; fi
else
  echo "skip TS mock harness: node >= 22.6 not found"
fi

printf '\n%d passed, %d failed\n' "$pass" "$failn"
[ "$failn" -eq 0 ]
