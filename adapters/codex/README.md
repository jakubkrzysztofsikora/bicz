# bicz — Codex adapter

Codex runs `bicz hook --harness codex --event <name>` per hook event, with a Claude-compatible
payload on stdin and `hookSpecificOutput` on stdout. The adapter is the `bicz` binary
(`internal/hooks`); it shares all logic with the Claude Code adapter.

## UNVERIFIED field mapping

**Every Codex field name used here is UNVERIFIED against a pinned installed Codex.**

- Installed when this was written: `codex-cli 0.162.1` (`codex --version`).
- The mapping comes from OpenAI's published hook documentation only. No Codex model session was
  run, no hook was executed by a real Codex, and no Codex credential file was read.
- All Codex field names live in **one table**: `CodexSpec.Fields` in `internal/hooks/codex.go`.
  If a pinned Codex differs, change that table and nothing else.

Mapped input: `session_id`, `hook_event_name`, `tool_name`, `tool_use_id`, `tool_input`,
`tool_response`. Mapped output: `hookSpecificOutput.{hookEventName, permissionDecision,
permissionDecisionReason, additionalContext}`, deny value `"deny"`. Codex also sends `turn_id`,
`cwd` and `model`, which bicz ignores. Codex has no `PostToolUseFailure` event. It lists
`PreCompact` and `PostCompact` events, but their output schema has no `additionalContext`, so bicz
cannot restore a projection there; compaction restoration relies on `SessionStart` with
`source: "compact"`. Only `SessionStart`, `PreToolUse` and `PostToolUse` are installed.

Runtime evidence (details and limits in [`docs/RUNTIME_HOOKS.md`](../../docs/RUNTIME_HOOKS.md)):
the real `codex-cli 0.162.1` parses the installer-generated `hooks.json` (3 hooks, no warnings) and
marks each hook **untrusted** until the user trusts it. **No hook has been run by a live Codex
session**, so the mapping above is still UNVERIFIED at runtime.

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
- `PostToolUse` runs only after a *successful* tool result in Codex, so a failed tool call is
  never settled by a `PostToolUseFailure` hook. Its reservation stays `held` (docs/CONTRACTS.md §1
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
2. **Skill (fallback, prompt-mediated, weaker).** Codex marks a new or changed hook **untrusted and
   skips it until you review it in `/hooks`** (or `--dangerously-bypass-hook-trust`). Until then the
   prompt reaches the model, which loads the skill: it must refuse multi-line text, pick a fresh random
   `BICZ_ARGS_<16 hex>` delimiter that does not occur in the text, and run one
   `bicz command --harness codex --arguments-stdin` here-document. The model transcribes your text, so
   it could mis-transcribe or be talked into a wrong delimiter; Codex's sandbox and approval flow still
   applies, nothing is pre-approved, and the ledger records only that it came via `codex`. Intent and
   provenance are weaker than the hook route. `allow_implicit_invocation: false` keeps the model from
   invoking the skill on its own.

Evidence (disposable temp `HOME`, real `codex-cli 0.162.1` with `--dangerously-bypass-hook-trust`,
local fake provider): `$bicz status`, `$bicz reward 0.5 "ok $(touch X) \`touch Y\`"` were answered by
the hook with **0 bytes sent to the model** and the reward appears in the ledger with no file created;
a multi-line attack was blocked with no file created; an ordinary prompt reached the model. **Not
verified:** how the interactive TUI displays the blocked-prompt result (`codex exec` printed only
`hook: UserPromptSubmit Blocked`; the text is also sent as `systemMessage`), and whether a real model
transcribes the fallback faithfully.

## Coverage (docs/CONTRACTS.md §5.6)

Observed: `SessionStart`, `PreToolUse`, `PostToolUse`.

Not observed, and not described as metered: hosted tools (they bypass hooks); app-server
experimental transports; **fail-open**: Codex proceeds when a pre-tool hook errors, times out, or
is disabled or removed. A bicz hook that times out or crashes therefore does not block the tool,
whatever `unmanaged_mode` says. Observed means bicz is in the call path. It does not mean
enforced.

The unreachable-daemon behavior, the hard 3 s timeout, and the "never emit `allow`" rule are
identical to the Claude adapter.
