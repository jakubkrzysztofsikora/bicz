# Runtime hook validation (real Claude Code and Codex binaries)

What happened when the **installed** harness binaries, not fixtures, were pointed at bicz hooks.
Date 2026-10-10, macOS 26 (darwin/arm64). Versions from `--version`:

| Harness | Version | Result |
| --- | --- | --- |
| Claude Code | `2.1.286` | **Live headless sessions ran the bicz hooks** (section 2) |
| Codex | `codex-cli 0.162.1` | **No live hook run: blocked by the hook-trust gate** (section 3). The real Codex does parse the installer-generated `hooks.json`. |

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

## 3. Codex 0.162.1: blocked, and what was verified instead

**No live Codex hook invocation was observed.** Reachable alternatives, in the order tried:

1. **Temp `CODEX_HOME`** (keeps real config untouched): `CODEX_HOME=/tmp/... codex login status` prints
   `Not logged in`. Auth lives in `CODEX_HOME`, and copying a credential store was not allowed.
2. **Real `CODEX_HOME` plus inline `-c hooks...` or a project `.codex/hooks.json`**: Codex reports
   the hook with `trustStatus: "untrusted"` (below). Non-managed command hooks must be trusted
   before they run. Trust is persisted under `[hooks.state]` in the real `~/.codex/config.toml`
   (not to be modified here) or skipped with `--dangerously-bypass-hook-trust`, a `dangerously-`
   flag that was ruled out. Computing a `trusted_hash` and passing it inline would amount to
   approving the hook, which was also not done.
3. No offline command in `codex` 0.162.1 fires a hook without a model turn.

What **was** observed with the real binary and no model, using `codex app-server --listen stdio://`
and the `hooks/list` request against a temp `CODEX_HOME`:

- `bicz install --harness codex` (temp `HOME`) wrote `hooks.json` with `SessionStart`, `PreToolUse`
  and `PostToolUse`, each `bicz hook --harness codex --event <X>`, timeout 5. Codex parsed it:
  3 hooks, `source: user`, `enabled: true`, `isManaged: false`, `timeoutSec: 5`, **`warnings: []`,
  `errors: []`**, `trustStatus: untrusted` for all three. So the file format is accepted by the
  installed version, and the hooks will not run until the user trusts them.
- An inline `-c 'hooks.PreToolUse=[...]'` hook is also listed (`source: sessionFlags`, untrusted).
- The installed binary's string table lists the hook events `PreToolUse`, `PermissionRequest`,
  `PostToolUse`, `PreCompact`, `PostCompact`, `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
  `SubagentStart`, `SubagentStop`, `Stop`, `Interrupt`. There is no `PostToolUseFailure`.

Upstream JSON schemas (`openai/codex`, branch `main`, **not pinned to 0.162.1**, so drift is possible;
read from `codex-rs/hooks/schema/generated/`) agree with `CodexSpec.Fields`: input
`session_id`, `hook_event_name`, `tool_name`, `tool_input`, `tool_use_id`, `tool_response` (Post),
plus `cwd`, `model`, `permission_mode`, `transcript_path`, `turn_id`; PreToolUse output
`hookSpecificOutput.{hookEventName, permissionDecision (allow|deny|ask), permissionDecisionReason,
additionalContext, updatedInput}`; `SessionStart.source` includes `compact`. The `PreCompact` output
schema has **no** `additionalContext`/`hookSpecificOutput`, which is why bicz does not install
`PreCompact` for Codex and relies on `SessionStart` with `source: compact`. This corrected a stale
claim ("Codex has no PreCompact event") in `internal/hooks/codex.go` and `adapters/codex/README.md`.
This is a schema reading, **not** a live observation.

Codex fail-open: carried in bicz's coverage table as a known Codex behavior (hook error or timeout
does not block the tool); **not observed here**, since no Codex hook ran. bicz still treats
`unmanaged_mode: block` as unable to block there. Claude's fail-open, by contrast, was observed (D1, D2).

### Prerequisite to finish Codex validation (needs a human decision)

A person who accepts the risk can, in a disposable repo and with their normal login:

```sh
bicz install --harness codex --account <disposable-id> --ledger <disposable.sqlite>   # writes ~/.codex/hooks.json
codex                                   # run /hooks, review the three bicz entries, trust them
codex exec --sandbox read-only -C <disposable-repo> --ephemeral "Read ./canary.txt"
bicz uninstall --harness codex          # remove afterwards; trust state stays in config.toml
```

then compare against the Claude table above (allow, deny with an under-funded account and a canary
that must not appear, hook error/timeout fail-open, `tool_name` values such as `Bash` and
`apply_patch` against the schedule keys). Until then the Codex mapping stays UNVERIFIED at runtime.

## 4. What this does and does not prove

- Proven here: a real Claude Code 2.1.286 headless session invoked the installer-generated hook
  commands with the field names bicz reads, honored a bicz deny for `Read`, surfaced receipts and the
  projection, and failed open on a broken or slow hook. The real Codex 0.162.1 parses the generated
  `hooks.json` and gates it behind trust.
- Not proven: live Codex hook behavior, Claude `PreCompact`, mutating tools, interactive use, OpenCode,
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
