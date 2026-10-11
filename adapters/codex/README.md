# bicz — Codex adapter

Codex runs `bicz hook --harness codex --event <name>` per hook event, with a Claude-compatible
payload on stdin and `hookSpecificOutput` on stdout. The adapter is the `bicz` binary
(`internal/hooks`); it shares all logic with the Claude Code adapter.

## Field mapping: verified at runtime (codex-cli 0.162.1)

A real `codex exec` (0.162.1, hooks trusted, mock model, temp `HOME`/ledger) ran `SessionStart`,
`UserPromptSubmit`, `PreToolUse` and `PostToolUse` against the installed bicz hooks, and every field
name in the table matched the stdin Codex sent. Transcripts and captured JSON:
[`docs/RUNTIME_HOOKS.md`](../../docs/RUNTIME_HOOKS.md) section 3. Other Codex versions are not checked.

- All Codex field names live in **one table**: `CodexSpec.Fields` in `internal/hooks/codex.go`.
  If another Codex version differs, change that table and nothing else.

Input read: `session_id`, `hook_event_name`, `tool_name`, `tool_use_id`, `tool_input`,
`tool_response`, plus `prompt` and `turn_id` for `UserPromptSubmit`. Output Codex accepted:
`hookSpecificOutput.{hookEventName, permissionDecision, permissionDecisionReason, additionalContext}`
(deny value `"deny"`, honored: the tool did not run) and `{decision: "block", reason, systemMessage}`
for `UserPromptSubmit`. Codex also sends `cwd`, `model`, `permission_mode`, `transcript_path`
(null) and `source` (`SessionStart`), which bicz ignores. Things the docs did not say: Codex's shell
tool arrives as `tool_name: "Bash"` (so the schedule's `Bash` price applies), and `tool_response` is a
bare string. Codex has no `PostToolUseFailure` event, but `PostToolUse` **does** fire for a shell
command that exited non-zero; its payload has no exit code, so bicz settles it as `completed` (same
charge). `PreCompact`/`PostCompact` exist but their output schema has no `additionalContext`, so
compaction restoration relies on `SessionStart` with `source: "compact"` (not exercised live).
Installed events: `SessionStart`, `PreToolUse`, `PostToolUse`, `UserPromptSubmit`.

## Trusting the hooks

**On your machine: run `codex`, type `/hooks`, review the bicz entries and trust all of them.**
Codex 0.162.1 skips a new or changed hook until then. Trust is stored in
`$CODEX_HOME/config.toml` as `[hooks.state."<hooks.json path>:<event>:<group>:<handler>"]
trusted_hash = "sha256:<hash of the hook>"`; changing a hook command makes it `modified` and
untrusted again. Non-interactive: send `config/batchWrite` for `hooks.state` to `codex app-server`
with the hash from `hooks/list`, or per invocation pass `--dangerously-bypass-hook-trust`.

## Install / uninstall

```sh
bicz install --harness codex --account "$USER" --dry-run
bicz install --harness codex --account "$USER"
bicz uninstall --harness codex
```

Same rules as the Claude adapter (see `adapters/claude/README.md`), with these differences:

- Target: `$CODEX_HOME/hooks.json`, else `~/.codex/hooks.json`. The config is
  `<bicz config dir>/codex.json`.
- Entries are identified by a command containing `bicz hook --harness codex`.
- Frozen quote and call state work exactly as in the Claude adapter, under `.../hooks/codex/`.
- Idempotency keys are `codex:auth:<tool_use_id>` and `codex:settle:<tool_use_id>`.
- `PostToolUse` also fires for a shell command that exited non-zero (observed), with no exit code in
  the payload, so it is settled as `completed`. Codex has no `PostToolUseFailure`. Where a tool call
  fails without any `PostToolUse` (not observed), its reservation stays `held` (docs/CONTRACTS.md §1
  I8) until reconciled; it is never silently released.

## Slash command `bicz`

`bicz install --harness codex` adds a `UserPromptSubmit` hook (timeout 15 s) to `hooks.json` and a
user skill `~/.agents/skills/bicz/SKILL.md` plus `agents/openai.yaml` (`allow_implicit_invocation:
false`). Grammar and semantics: [`docs/SLASH_COMMAND.md`](../../docs/SLASH_COMMAND.md).

**Route.** Codex 0.162.1 has no user-defined slash commands; custom prompts (`/prompts:name`) are
**deprecated** in favor of skills (<https://learn.chatgpt.com/docs/custom-prompts.md>), so none is
installed. The operator types `$bicz ...`:

```text
$bicz loss no_fake_tests "claimed a pass" "run the real suite"
$bicz reward 5 "shipped the fix"
$bicz status
```

1. **Hook (direct, no model).** `bicz hook --harness codex --event UserPromptSubmit` receives the
   prompt as JSON (<https://learn.chatgpt.com/docs/hooks.md>). Only a prompt that *starts with* the
   token `$bicz` followed by whitespace or end is answered (`$biczx`, `please run $bicz`, `bicz status`
   are ordinary prompts and untouched). The text after the token is run in-process as data and the
   prompt is blocked (`decision: "block"`), so no model runs and no shell sees the text.
2. **Skill (fallback, prompt-mediated, weaker).** Until the hooks are trusted, Codex skips the hook
   (observed: the prompt reached the model and nothing was recorded). The model then loads the skill:
   it must refuse multi-line text, pick a fresh random
   `BICZ_ARGS_<16 hex>` delimiter that does not occur in the text, and run one
   `bicz command --harness codex --arguments-stdin` here-document. The model transcribes your text, so
   it could mis-transcribe or be talked into a wrong delimiter; Codex's sandbox and approval flow still
   applies, nothing is pre-approved, and the ledger records only that it came via `codex`. Intent and
   provenance are weaker than the hook route. `allow_implicit_invocation: false` keeps the model from
   invoking the skill on its own. **Not verified:** a real model transcribing this fallback faithfully.

Evidence (real `codex-cli 0.162.1`, `codex exec`, hooks trusted via `hooks.state`, no bypass flag,
mock model server, temp `HOME`, ledger and socket): `$bicz status`, `$bicz reward 2 "x"` and
`$bicz loss no_fake_tests "r"` (no saved correction) were each answered by the hook with
**0 requests to the model**; the reward moved the temp ledger 120000000 to 122000000 mc, status and the
no-correction loss recorded nothing, `bicz verify` INTEGRITY OK. The same prompt untrusted reached the
model and recorded nothing; with `--dangerously-bypass-hook-trust` the hook answered it. `codex exec`
printed only `hook: UserPromptSubmit Blocked`; the result text is in the hook JSON (`reason` and
`systemMessage`). **Not verified:** how the interactive TUI displays the blocked-prompt result.

## Coverage (docs/CONTRACTS.md §5.6)

Observed in a real Codex run: `SessionStart`, `UserPromptSubmit`, `PreToolUse` (allow, deny honored),
`PostToolUse` (settled), and **fail-open on a hook error and on a hook timeout** (the tool ran).

Not observed, and not described as metered: hosted tools (they bypass hooks); app-server
experimental transports; subagents and MCP/`apply_patch` tool names; the interactive TUI.
**Fail-open** (observed for a hook error and a hook timeout): Codex proceeds when a pre-tool hook errors, times out, or
is disabled or removed. A bicz hook that times out or crashes therefore does not block the tool,
whatever `unmanaged_mode` says. Observed means bicz is in the call path. It does not mean
enforced.

The unreachable-daemon behavior, the hard 3 s timeout, and the "never emit `allow`" rule are
identical to the Claude adapter.
