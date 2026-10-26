# bicz — Oh My Pi adapter

`extension.ts` is a bicz harness adapter for the Oh My Pi coding agent. It records
tool calls in the bicz action-credit ledger (docs/CONTRACTS.md §5, protocol
`bicz.adapter/1`) and injects the read-only context projection at session start,
before each agent run, and during compaction.

It is a single self-contained file with **zero runtime npm dependencies** (Node
built-ins only) and is deployed by `bicz install` (below). The canonical Go
reference for its logic is `internal/adapter/` (client, pricing, outcome,
projection); this file mirrors those semantics so the two cannot drift silently.

## Install / uninstall

Install requires a bicz build. Under `bicz/`:

```sh
go build -o /tmp/bicz ./cmd/bicz
bicz open --db "$HOME/.bicz/ledger.sqlite" --identity "$USER" --capital 1000000 --reason "provision pilot identity" 2>/dev/null || true
bicz install --harness omp --account "$USER" --dry-run     # preview the exact diff
bicz install --harness omp --account "$USER"               # real install
```

What `bicz install --harness omp` does (user-scope only; no elevated
privileges, no LaunchAgents, no sudo):

1. Detects the OMP agent extensions root:
   `$PI_CODING_AGENT_DIR/extensions`, else `$PI_CONFIG_DIR/agent/extensions`,
   else `$HOME/.omp/agent/extensions` (OMP's native discovery directory for
   `.ts`/`.js` extension modules).
2. Writes `<root>/bicz.omp.ts` — a byte-exact copy of this `extension.ts` —
   atomically, with a timestamped backup of any prior bicz-owned version.
   A second `bicz install` is a no-op (idempotent).
3. Writes the bicz config to `$BICZ_CONFIG_DIR` / `$XDG_CONFIG_HOME` /
   `$HOME/.config` → `bicz/omp.json` (account id, supervisor socket derived
   from the ledger path, unmanaged / no-schedule mode, optional schedule path).
4. Refuses to clobber a foreign or malformed file at either target, refuses
   symlinked paths, and refuses paths outside `$HOME`.

Uninstall removes only bicz-owned entries (`bicz.omp.ts`, `omp.json`, and their
bicz backups) and restores the newest backup when safe:

```sh
bicz uninstall --harness omp --dry-run
bicz uninstall --harness omp
```

To move the config without reinstalling, point the extension at it explicitly:
`BICZ_CONFIG=/path/to/omp.json`. The extension always reads `omp.json` fresh per
process; no restart is required after an operator updates the ledger or quota.

## What this extension does and does not observe

Observed (bicz is in the call path — the `observed` rows of the coverage table,
docs/CONTRACTS.md §5.6):

| Source | What happens |
| --- | --- |
| `tool_call` (models/built-ins/extension tools) | priced against the published schedule, then `authorize` creates a bounded reservation; denied calls are blocked with the daemon's reason + lesson notice |
| `tool_result` | classified (ordinary_failure default, zero fine; breach classes require observable evidence) and settled; the receipt (and any notice or penalty cue queued by `tool_call`) is appended to the tool's own result content, which OMP lets `tool_result` patch |
| `session_start` / `before_agent_start` / `session.compacting` | read-only projection (`projection`) injected as a custom message / compaction context or the status line, plus the ledger-built accountability cue of every active confirmed penalty; `tool_call` also emits a cue the session has not yet been given (docs/CONTRACTS.md §6.4) |

Not observed (disclosed, never described as metered):

- browser / computer / eval host bridges and native tools that do not emit
  `tool_call` / `tool_result` events (coverage table row `omp host bridges`).
- provider model calls, subagent internals, and anything that bypasses the
  extension runtime entirely. A `bicz status` / `bicz doctor` run lists these
  paths explicitly.

## Fail-safe: daemon unreachable

When no supervisor answers on the configured socket, **recording is
unavailable** — bicz never pretends a charge was recorded. The default
(disclosed-safe) behavior is **pass-through with notice**: the tool call runs,
and an explicit notice is appended to that call's result content stating that this
action was NOT metered and no charge is recorded. The alternative is **block**: deny every
call with reason `daemon_unavailable` until a supervisor is reachable. Set it
per deployment in `omp.json`:

```json
{ "unmanaged_mode": "block" }
```

Pass-through-with-notice is the shipped default because bicz is
incentive-and-accountability only: the cost of a silent false economy (the agent
believing it was charged when it was not) is worse than the cost of work
proceeding while the ledger is down, and the operator can flip the policy to
block with one field. The same choice applies to a missing price schedule
(`no_schedule_mode`, also default `notice`): an action is never priced by an
invented amount, and it is disclosed as unrecorded instead.

## Pricing

A reservation's `max_cost` is derived only from the published price schedule
(`schedule_path` in `omp.json` — the operator's `bicz price` policy document,
which carries `tool_max_cost_mc`, `default_max_cost_mc`, `max_cost_cap_mc` as
part of the published body). `bicz install --schedule FILE --policy-version V`
writes both keys; a flagless reinstall keeps them. Without a schedule, no
amount is invented: the action is disclosed as not metered
(`no_schedule_mode`). Settlement charges the quoted operating price as
observed usage — bicz does not yet meter real CPU/token usage
(docs/CONTRACTS.md §9.3), which is disclosed here rather than claimed.

## Wire calls

The extension makes exactly these protocol calls (one connection each):

| op | when | key |
| --- | --- | --- |
| `projection` | session_start, before_agent_start, session.compacting | — |
| `authorize` | every priced tool_call, source `tool_call` | `omp:auth:<toolCallId>` |
| `settle` | every authorized tool_result | `omp:settle:<toolCallId>` |
| `status` | (not called by the extension; `bicz status` covers it) | — |

Idempotency keys are the stable OMP tool call ids, so a harness retry replays a
no-op (`duplicate_key_ok`) instead of a double charge.

## Operator `/bicz` command

The extension registers a native `/bicz` slash command with `pi.registerCommand`. OMP runs it before any prompt reaches a model. The handler runs `bicz command --harness omp --arguments-stdin --json --invocation <uuid>`, passing the typed text on stdin through an argv array, so no shell is involved. It shows the result with `ctx.ui.notify`: `info` for ok, `warning` for needs_input, `error` for refusals.

- `/bicz status`
- `/bicz reward AMOUNT "reason"`: explicit operator reward (or bare `/bicz reward "reason"` with inference)
- `/bicz reset [AMOUNT] ["reason"]`: unblocks balance up to baseline ceiling (or AMOUNT) without running reward logic or modifying penalty history
- `/bicz loss "reason"`: the default form. The inference model picks the rule and correction from the session transcript; requires an `inference` block in `omp.json`. The slash autocomplete lists it first.
- `/bicz loss RULE "reason" ["correction"]`: when no correction is given or saved, the operator gets the question and nothing is written
- `/bicz rules`: lists default rules and tiers

The executable is read from `bicz_path` in `omp.json` (written by `bicz install`), then `$BICZ_BIN`, then `bicz` on `PATH`. A slash command is an operator act, not a tool call: it never creates a reservation or settlement. Automated microrewards rebate tool costs on passing tests and plan completions within strict anti-farming invariants (clamped to cost, capped at baseline ceiling).

## Verified in a real omp

Real `omp --mode rpc` 18.2.11 (`/opt/homebrew/bin/omp`), temp `HOME` and `PI_CODING_AGENT_DIR`, extension installed with `bicz install --harness omp`, a temp ledger with a published schedule (`read` = 5000 mc, `schedule_path` and `policy_version` set in `omp.json`), and a local mock OpenAI-compatible provider (`models.yml`: `api: openai-completions`, fake key `fake-key-not-real`, Node built-ins only, scripted to emit one `read` tool call and then a final text). No real credential or `~/.omp` was used. Each run is `{"type":"prompt"}` over the RPC stdio protocol.

- Allowed call: `seq 7 res:omp:auth:<toolCallId>` (spend 0, `held_after` 5000) then `seq 8 settle:omp:auth:<toolCallId>` (spend -5000, class `completed`, released 5000); balance 9990000 -> 9985000, held 0. The tool result the model received carried a second text block `bicz receipt: completed, charged 5000 mc, fine 0`, and that block is in the next model request. The projection `bicz context: identity omptest ... ledger seq 6` was in the first request as a custom user-role message (`before_agent_start`).
- Insufficient credit (`read` priced 50000000 mc, spendable 9985000): no ledger event; `tool_execution_end isError=true` with `bicz: insufficient_credit ... cannot reserve 50000000 microcredits`; the file was not read; the model's next request carried that text as the tool result.
- Penalty cue: after `bicz loss ... --event omp-loss-1` the next turn's projection carried `bicz accountability record ... violated omp_rule ... (loss event omp-loss-1)` and the receipt added `outstanding loss shortfall 1000 mc`.
- Daemon down (`unmanaged_mode: notice`): the tool ran and its result carried `bicz: recording is unavailable ... NOT being metered`, which reached the model.
- Reinstall into the temp agent dir kept `schedule_path` and `policy_version`.

Two defects only the real omp showed (the mock harness had assumed otherwise), fixed in `extension.ts`:

1. Every settlement of a successful call declared class `ordinary_failure`, so the ledger recorded it as `settle ordinary_failure` and the receipt would have said so. Only a breach class is declared now (mirror of `hooks.breachClass`); an ordinary completion declares none and is recorded as `completed`.
2. The settlement receipt, the unmetered notice and the penalty cue were returned from `tool_call`/`tool_result` as `additionalContext`, a field OMP does not read: the receipt never reached the model (the request after the first run contained no `bicz receipt`). They are now queued per tool call and appended to the result content in `tool_result`.

Not exercised: the interactive TUI, `session.compacting` (compaction was not triggered), `unmanaged_mode: block`, subagents, and every provider or model behavior (the model is a mock). The `/bicz` command was verified earlier in the same omp version (docs/SLASH_COMMAND.md). A Node 26 mock-`ExtensionAPI` harness (`scripts/smoke_ts_harness.mjs`, run by `scripts/smoke.sh` against a real daemon) covers the adapter paths; it is rewritten to the shapes observed above.
