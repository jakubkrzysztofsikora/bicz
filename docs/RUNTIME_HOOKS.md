# Runtime hook validation (real Claude Code and Codex binaries)

What happened when the **installed** harness binaries, not fixtures, were pointed at bicz hooks.
Date 2026-10-10, macOS 26 (darwin/arm64). Versions from `--version`:

| Harness | Version | Result |
| --- | --- | --- |
| Claude Code | `2.1.286` | **Live headless sessions ran the bicz hooks** (section 2) |
| Codex | `codex-cli 0.162.1` | **Live `codex exec` runs ran all four bicz hooks** with the hooks trusted the way Codex itself records trust, no bypass flag (section 3) |

Everything used a disposable ledger, config dir, state dir, socket and working directory under
`/tmp`, the example schedule `docs/schedule.example.json` and the real built `bicz` binary. No real
`~/.claude/settings.json`, `~/.codex/config.toml` or any credential file was read or modified (their
mtimes were unchanged; the only residue was an empty `~/.claude/projects/-private-tmp-bz-work/memory`
directory that Claude created on its own, which was removed). No `HOME` redirect was used for
Claude, so its existing login was used as is (`claude auth status` showed logged in; account details
are not recorded here). No production identity was used and no penalty was recorded.

## 1. Method

Claude Code, one bounded call per scenario (each ran well under the 90 s limit, `timeout 90`):

```sh
claude -p "<prompt>" --model haiku --tools Read \
  --settings <overlay.json> --setting-sources project,local \
  --strict-mcp-config --disable-slash-commands --no-session-persistence \
  --max-budget-usd 0.30 --output-format stream-json --verbose --include-hook-events
```

- `--settings` is the documented overlay flag. `--setting-sources project,local` keeps the user's
  own settings out of the disposable run. Only the `Read` tool exists in the session. No
  permission-skip flag, no `--allowedTools`, no new approval was used: `Read` inside the working
  directory needs no prompt. `claude 2.1.286` has no `--max-turns`; the bound is the single-purpose
  prompt, the 0.30 USD budget cap and the 90 s timeout.
- The overlay is the hooks block produced by `bicz install --harness claude` run under a **temp
  `HOME`** (so the real settings were never the install target). Runs marked "direct" use those
  commands unchanged (`/tmp/bz/bicz hook --harness claude --event X`). Runs marked "wrapped" replace
  the command with a small `tee` wrapper that saves the harness-generated stdin and bicz's stdout/exit
  code, then runs the same `bicz hook`. Only key names, types and non-secret enumerations were kept
  from the captured payloads.
- Supervisor: real `bicz serve` on a temp ledger and `BICZ_RUNTIME_DIR`. Accounts: `rt-ok` (120000 mc),
  `rt-poor` (1000 mc). Reads target `/tmp/bz/work/canary.txt`, whose first line is a unique token.

## 2. Claude Code 2.1.286: observed results

| Run | Setup | Observed |
| --- | --- | --- |
| A (wrapped) | `rt-ok`, Read canary | `SessionStart`, `PreToolUse`, `PostToolUse` each ran, exit 0, outcome success. Pre printed nothing (no `permissionDecision`). Post printed `bicz receipt: completed, charged 5000 mc, fine 0` and Claude's stream carries it. Ledger 120000 to 115000, held 0. The canary token appeared in the transcript (3 occurrences): the tool ran. |
| B (direct) | `rt-poor`, Read canary | Pre output the deny JSON. Claude reported the tool result as an error: `PreToolUse:Read hook error: bicz: insufficient_credit — ... cannot reserve 5000 microcredits (spendable=1000)`. **Canary occurrences in the transcript: 0** (control run A: 3), so the Read did not execute. Model answered `REFUSED`. Ledger unchanged (1000, held 0). |
| C (wrapped) | `rt-ok`, Read of a nonexistent file | Claude sent `PostToolUseFailure` (not `PostToolUse`). bicz settled it as `bicz receipt: ordinary_failure, charged 5000 mc, fine 0`; ledger 115000 to 110000, held 0. |
| D1 (direct) | `rt-poor`, `PreToolUse` command replaced by `sh -c "exit 1"` | Hook outcome `error`, exit 1. **The Read still ran** (canary 3, ledger untouched). |
| D2 (direct) | `rt-poor`, `PreToolUse` command `sleep 8` with `timeout: 1` | Hook outcome `cancelled`. **The Read still ran** (canary 3). |
| E1 (direct) | daemon stopped, `unmanaged_mode: notice` | Read ran, canary 3, context says `NOT metered`. |
| E2 (direct) | daemon stopped, `unmanaged_mode: block` | bicz printed the `daemon_unavailable` deny, exit 0; Claude reported `PreToolUse:Read hook error: daemon_unavailable: ...`; canary 0, so the Read did not run. |
| F (direct) | `rt-ok`, no tools, asked to quote a `bicz context:` line | The model answered `bicz context: identity rt-ok, role active, ledger seq 7`, so the `SessionStart` `additionalContext` projection reaches the model. |

`bicz verify` on the disposable ledger after all runs: `INTEGRITY OK` (7 events).

### Field shapes Claude actually sent (names and types only)

- `SessionStart`: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `source` (`startup` seen). No `model` field in this version.
- `PreToolUse`: `session_id`, `transcript_path`, `cwd`, `prompt_id`, `permission_mode` (`default` seen), `hook_event_name`, `tool_name`, `tool_input` (`{file_path}` for Read), `tool_use_id` (`toolu_...`).
- `PostToolUse`: the same plus `tool_response` (an object for Read: `{type, file:{filePath, content, numLines, startLine, totalLines}}`) and `duration_ms`.
- `PostToolUseFailure`: the Pre fields plus `error` (string), `is_interrupt` (bool), `duration_ms`; **no** `tool_response`.

These match the names in `ClaudeSpec`; no source change was needed for Claude. The same
`tool_use_id` arrived on Pre and Post, which is what the idempotency keys and the frozen-quote state
file rely on.

### Fail-open semantics (observed)

- **Claude Code fails open on a hook that errors or times out** (D1, D2): the tool runs and nothing
  is metered. A deny only takes effect when the hook runs, exits 0 and prints the deny JSON.
  `unmanaged_mode: block` therefore protects against an unreachable daemon (E2) but not against a
  crashed, killed, slow or removed hook process. `bicz hook` exits 1 on malformed stdin or an unknown
  `--event`, which Claude treats as such a non-blocking error.
- The deny text is shown to the model prefixed with `PreToolUse:<tool> hook error:`; cosmetic.

### Not verified for Claude

`PreCompact` (not triggered; whether Claude accepts `additionalContext` there is still unverified),
interactive sessions, tools other than `Read`, permission-mode interplay (e.g. a hook deny versus
an allow rule), subagents, MCP tools, a hook running under a managed policy, latency under load.
A hook deny is observed to stop `Read`; stopping a mutating tool (Write/Bash) was not exercised because
that needs an extra permission grant, which this run was not allowed to add.

## 3. Codex 0.162.1: observed results

Date 2026-10-11. Everything ran in a temp `HOME` (`/tmp/bz-codex/home`, `CODEX_HOME=$HOME/.codex`), a
temp ledger and socket, `bicz serve` on that ledger, and a local mock model server
(`http://127.0.0.1:47613/v1`, Responses API, fake key `sk-fake-not-real`, provider passed with `-c`).
No real `~/.codex`, `auth.json` or credential was read or modified.

### 3.1 How Codex 0.162.1 records hook trust (read from `openai/codex` tag `rust-v0.162.1`)

- **Where:** the user config layer, `$CODEX_HOME/config.toml`, table `[hooks.state."<key>"]`, key
  `trusted_hash = "sha256:<hash>"` (source: `codex-rs/hooks/src/engine/discovery.rs`,
  `hook_trust_status`, `hook_trusted_hash`; `config_rules.rs` reads only the user and session-flag layers).
- **Key:** `<absolute hooks.json path>:<event snake_case>:<group index>:<handler index>`, e.g.
  `/private/tmp/bz-codex/home/.codex/hooks.json:pre_tool_use:0:0`.
- **Hash:** `currentHash`, a SHA-256 over a normalized TOML identity of the event name, matcher and
  handler (command, timeout, ...), so it changes when the hook command changes. `hooks/list` reports
  it per hook. Status is `trusted` when `trusted_hash == currentHash`, `modified` when the file
  has a different hash, `untrusted` when none is stored. Only `trusted` (or `managed`) hooks run.
- **How it is written:** the `/hooks` screen sends app-server `config/batchWrite` with
  `edits: [{keyPath: "hooks.state", value: {<key>: {...}}, mergeStrategy: "upsert"}]`.
- **Non-interactive options:** (a) send that same request to `codex app-server --listen stdio://`
  with `trusted_hash` set to `currentHash` (used here, temp home only); (b) write the TOML table by
  hand; (c) `codex --dangerously-bypass-hook-trust` per invocation (also exercised, below). No
  `codex hooks` subcommand exists.

### 3.2 Setup

```text
bicz init/open (codex-rt, 120 credits)/price (docs/schedule.example.json, v1)
bicz install --harness codex --account codex-rt --ledger /tmp/bz-codex/ledger.sqlite
  -> $CODEX_HOME/hooks.json (SessionStart, PreToolUse, PostToolUse, UserPromptSubmit),
     <cfg>/codex.json (schedule_path + policy_version added by hand), ~/.agents/skills/bicz
hooks/list, before trust:   all 4 hooks  trustStatus=untrusted
config/batchWrite hooks.state ... -> {"status":"ok"}
hooks/list, after:          all 4 hooks  trustStatus=trusted enabled=true
```

To capture payloads, a wrapper (`wrap.sh EVENT`) replaced the hook command: it saved Codex's stdin and
bicz's stdout/exit code, then ran the same `bicz hook --harness codex --event EVENT`. Editing the
command made Codex report all four hooks `modified` until re-trusted, which confirms the hash binds
the command. A first `$bicz status` run with the unwrapped installer command gave the same result.

### 3.3 `$bicz ...` through `codex exec` (UserPromptSubmit, 0 requests to the model)

Command: `codex exec --skip-git-repo-check --ephemeral --sandbox read-only -C /tmp/bz-codex/work -c model_provider="mock" ... '<prompt>'`.
Mock model request count after all three prompts: **0**.

| Prompt | Hook stdin (real Codex) | Hook stdout | Ledger codex-rt |
| --- | --- | --- | --- |
| `$bicz status` | `{"session_id","turn_id","transcript_path":null,"cwd","hook_event_name":"UserPromptSubmit","model","permission_mode":"bypassPermissions","prompt":"$bicz status"}` | `{"decision":"block","reason":"BICZ status identity=codex-rt ... BALANCE 120 credits ...","systemMessage":"..."}` | unchanged, 120000000 mc, events=2 |
| `$bicz reward 2 "x"` | same keys | `{"decision":"block","reason":"BICZ reward recorded: event=slash-reward-codex-... CREDIT amount=2 credits ... balance=122 credits ..."}` | 120000000 to **122000000**, events=3 |
| `$bicz loss no_fake_tests "r"` | same keys | `{"decision":"block","reason":"BICZ loss needs input\nNo correction is saved for rule no_fake_tests ... Nothing was recorded. ..."}` | unchanged, 122000000, events=3 |

`codex exec` printed only `hook: UserPromptSubmit` / `hook: UserPromptSubmit Blocked` for each (plus
`hook: SessionStart` / `hook: SessionStart Completed`); the result text is in the hook's JSON, not
printed by `exec`. How the interactive TUI shows it was **not** observed. `bicz verify`: `INTEGRITY OK`.

Trust gate, same reward prompt with an empty `config.toml` (no `[hooks.state]`):

| Run | Hooks ran | Model requests | Ledger |
| --- | --- | --- | --- |
| untrusted, no flag | no (0 captures) | 1 (the prompt reached the model; reply `MOCK-DONE`) | unchanged (121900000) |
| untrusted + `--dangerously-bypass-hook-trust` | yes (warning printed twice) | 0 | +3 credits (124900000) |

### 3.4 Tool-call hooks with the mock model (reserve, settle, deny, fail-open)

The mock answers the first Responses request with one `function_call` (`exec_command`, the tool
Codex 0.162.1 offers; its tools were `exec_command, write_stdin, request_user_input, view_image, ...`)
and then a final `MOCK-DONE` message. No hosted model, no credentials.

| Run | Observed |
| --- | --- |
| `echo hi` (`codex-rt`) | `command_execution` ran, `exit_code:0`, output `hi`. Ledger 122000000 to **121950000**, held 0, events 3 to 5. Pre stdout empty (allow prints nothing). Post stdout `{"hookSpecificOutput":{"additionalContext":"bicz receipt: completed, charged 50000 mc, fine 0","hookEventName":"PostToolUse"}}` |
| command exiting non-zero (`echo oops-on-stderr >&2; echo partial; exit 3`, re-run) | Codex's own event: `"exit_code":3,"status":"failed"`. Codex still sent **PostToolUse** (so the earlier "PostToolUse only after a successful result" claim was wrong), but the payload has **no exit code and no error flag**: `"tool_response":"partial\noops-on-stderr\n"`. bicz settled it as `completed`, charged 50000 mc (124900000 to 124850000, held 0); receipt `bicz receipt: completed, charged 50000 mc, fine 0`, hook exit 0. See 3.4a: this cannot be classified as `ordinary_failure` from the payload |
| under-funded account (`codex-poor`, 1000 mc), `touch CANARY` | Codex logged `Command blocked by PreToolUse hook: bicz: insufficient_credit ... cannot reserve 50000 microcredits (spendable=1000)`; the model received that text as the tool result; **CANARY not created** (workspace-write sandbox); no PostToolUse fired; ledger unchanged (1000, held 0) |
| PreToolUse hook exits 7 | tool **ran**, CANARY created: **fail-open on hook error observed** |
| PreToolUse hook sleeps 5 s with `timeout: 1` | tool **ran**, CANARY created: **fail-open on hook timeout observed** |

### 3.4a Why a non-zero exit is settled as `completed` (source: `rust-v0.162.1`)

- `codex-rs/core/src/tools/context.rs`, `ExecCommandToolOutput::post_tool_use_response`: the hook gets
  `JsonValue::String(truncated_output)` and nothing else. The `Process exited with code N` line exists
  only in `response_header()` (what the model sees and what is logged), not in the hook payload. The
  same type reports `success_for_logging() == true` always.
- The PostToolUse input schema (`hooks/schema/generated/post-tool-use.command.input.schema.json`) has
  `session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, tool_name,
  tool_input, tool_response, tool_use_id, agent_id, agent_type` and no exit, status or error field.
- Only `exec_command` (as `Bash`), `apply_patch` (text) and MCP tools build a PostToolUse payload in
  this tag; none carries an exit code.

So a failure is not unambiguously present in the payload. Any rule over the output text would be a
guess: a command can print "Exit code: 3" and succeed, or fail silently. bicz therefore keeps
`completed` (same charge, fine 0) and does not infer failure from text.
`TestCodexRealPayloadsReserveAndSettle` pins this with the captured exit-3 payload and with output
text that imitates an exit-code line. The honesty cost is real: a failed Codex shell command is
recorded as completed. It can be fixed only if Codex adds an exit field to the hook payload.

### 3.5 Real field names vs `CodexSpec.Fields`

| Event | Keys in the real Codex stdin | Spec |
| --- | --- | --- |
| SessionStart | `session_id, transcript_path (null), cwd, hook_event_name, model, permission_mode, source ("startup")` | `session_id`, `hook_event_name` read; **match** |
| UserPromptSubmit | `session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, prompt` | `prompt`, `session_id`, `turn_id`; **match** |
| PreToolUse | `session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, tool_name ("Bash"), tool_input ({"command":"echo hi"}), tool_use_id` | `tool_name`, `tool_input`, `tool_use_id`; **match** |
| PostToolUse | the Pre keys plus `tool_response` (a **string**) | `tool_response` (read as any JSON value); **match** |

Outputs Codex accepted: `hookSpecificOutput.{hookEventName, additionalContext}` (SessionStart, PostToolUse),
`hookSpecificOutput.{hookEventName:"PreToolUse", permissionDecision:"deny", permissionDecisionReason}`
(honored: tool blocked), and `{"decision":"block","reason","systemMessage"}` (UserPromptSubmit, blocked).
**No field-name mismatch was found, so no code change to the field table was needed.** Differences from
what the docs said: Codex's shell tool reaches hooks as `tool_name: "Bash"` (so the schedule key `Bash`
prices it), `tool_response` is a string, and PostToolUse fires for a failed shell command. A test replays
the captured Pre/Post payloads (`TestCodexRealPayloadsReserveAndSettle`).

### 3.6 Not verified for Codex

Interactive TUI (`/hooks` screen, display of a blocked-prompt reason), `apply_patch` and MCP tool names,
SessionStart with `source: compact`, a real model transcribing the skill fallback, hosted tools,
subagents, latency under real load, behavior of other Codex versions.

### How to trust the hooks on your own machine

Run `codex`, type `/hooks`, review the bicz entries, and trust all of them (it writes
`[hooks.state]` into your `~/.codex/config.toml`). Re-trust after any `bicz install` that changes a hook command.

## 4. What this does and does not prove

- Proven here: a real Claude Code 2.1.286 headless session invoked the installer-generated hook
  commands with the field names bicz reads, honored a bicz deny for `Read`, surfaced receipts and the
  projection, and failed open on a broken or slow hook. The real Codex 0.162.1, with the hooks trusted
  through its own `hooks.state` mechanism, ran SessionStart, UserPromptSubmit, PreToolUse and PostToolUse
  with the field names bicz reads, answered `$bicz` with 0 model requests, reserved and settled a real
  tool call, honored a bicz deny, and failed open on a hook error and a hook timeout.
- Not proven: Codex interactive TUI, `apply_patch`/MCP tools, Codex compaction, Claude `PreCompact`,
  Claude mutating tools, interactive use, OpenCode,
  Oh My Pi (separate workers/owners), or any behavioral effect on a model. Phase B is unchanged and
  untouched (`docs/REHEARSAL.md`). Nothing here is cooperative-denial hardening: a user or agent can
  still disable hooks.

## 5. Accountability cue delivery (added after the runs above; not part of them)

Not observed in a live Claude Code or Codex session. Verified with the real `bicz` binary, real daemon and
the real `bicz hook` process over stdin/stdout (`scripts/smoke.sh`, `internal/hooks` tests), which stand in
for the harness. For a confirmed penalty (`docs/CONTRACTS.md` §6.4):

- `SessionStart` and `PreCompact` append the active cues to the projection `additionalContext`.
- `PreToolUse` reads the projection the authorize response already carries; when its `penalty_seq` is newer
  than the per-session mark kept under the hook state directory (`hooks/<harness>/*.seen`), it adds the new
  cues as `additionalContext` (or to the deny reason). No extra daemon call. The mark records that the text
  was emitted to the harness, not that the model took it in. Losing the mark only repeats cues.
- Output is `additionalContext` only; no `permissionDecision` is ever set by a cue.
