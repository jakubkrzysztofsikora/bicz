# bicz — Claude Code adapter

Claude Code runs `bicz hook --harness claude --event <name>` for each hook event. The event JSON
arrives on stdin and the hook response JSON leaves on stdout. There is no extension file to
deploy; the adapter is the `bicz` binary itself (`internal/hooks`, which reuses `internal/adapter`
for the client, pricing, outcome classification and projection rendering).

## Install / uninstall

```sh
bicz install --harness claude --account "$USER" --dry-run   # exact diff, writes nothing
bicz install --harness claude --account "$USER"
bicz uninstall --harness claude
```

`bicz install --harness claude` (user-scope only; no elevated privileges):

1. Merges hook entries into `$CLAUDE_CONFIG_DIR/settings.json`, else `~/.claude/settings.json`,
   for `SessionStart`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure` and `PreCompact`. The
   command uses the **absolute path** of the running `bicz` executable, which must be named
   `bicz`. Spaces in the path are backslash-escaped so the literal text
   `bicz hook --harness claude` stays in the command. That text is how bicz entries are
   identified.
2. Preserves every unrelated key and hook. Values are carried through as raw JSON; only the
   file's indentation is normalized. The previous file is first copied to
   `settings.json.bak-<timestamp>` (the file mode is kept) and the new file is written
   atomically. A second install is a no-op, and a re-install after the binary moved updates the
   existing entries in place.
3. Writes `<bicz config dir>/claude.json` (account, supervisor socket, `unmanaged_mode`,
   `no_schedule_mode`; optional `schedule_path`, `policy_version`). An existing config keeps its
   operator-set keys.
4. Refuses symlinks anywhere on the path, a malformed or non-object settings file, a `hooks`
   value of the wrong shape, a foreign `claude.json`, and any target outside `$HOME` (including a
   `CLAUDE_CONFIG_DIR` outside `$HOME` or containing `..`).

`bicz uninstall --harness claude` removes only entries whose command contains
`bicz hook --harness claude`. When a pristine backup still matches the result it restores that
backup byte for byte. If bicz created the settings file it removes the file. Edits you made after
install are kept.

## Events

| Event | What happens | Keys |
| --- | --- | --- |
| `SessionStart` | `projection` rendered into `hookSpecificOutput.additionalContext` (also runs for `source: compact`), followed by the accountability cue of every active confirmed penalty (docs/CONTRACTS.md §6.4) | — |
| `PreCompact` | same projection as `additionalContext` (whether Claude accepts it for this event is **unverified**; `SessionStart` with `source: compact` is the durable path) | — |
| `PreToolUse` | priced from the published schedule, then `authorize` (source `PreToolUse`) | `claude:auth:<tool_use_id>` |
| `PostToolUse` / `PostToolUseFailure` | `settle` the reservation; the receipt goes in `additionalContext`. A failure settles as an honest `error` with zero fine | `claude:settle:<tool_use_id>` |

Output shapes:

- Deny: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"..."}}`
- On allow the hook prints nothing, or only `additionalContext`. It **never** emits
  `permissionDecision: "allow"`, because that would bypass your own permission prompts.

A retried `tool_use_id` replays the same idempotency key, so it never reserves or charges twice.

### Frozen quote (settlement never re-reads the schedule)

The price schedule is mutable, so `PostToolUse` never re-quotes it. `PreToolUse` freezes the quote
(amount, policy version, price state, reservation id, tool, args digest) into one file per call,
keyed by session id + `tool_use_id`, at `$BICZ_STATE_DIR/hooks/claude/`, else
`$XDG_STATE_HOME/bicz/hooks/claude/`, else `~/.local/state/bicz/hooks/claude/` (directory `0700`,
files `0600`, written atomically). The record is written before `authorize`, and `authorize` and
`settle` use only the frozen amount and policy version. A schedule or policy change between the
two events therefore cannot make a delayed settlement charge a different amount or be refused as
`usage_exceeds_reservation`, and a replayed `PreToolUse` cannot turn into `payload_conflict`.

| Status | Meaning | `PostToolUse` |
| --- | --- | --- |
| `pending` | quote frozen, `authorize` answer not known (for example the daemon timed out) | settles; a daemon that holds nothing answers `unknown_reservation`, and the record is dropped |
| `reserved` | the daemon allowed it | settles the frozen amount |
| `unreserved` | nothing was reserved (unobserved, unpriced) | silent; `PreToolUse` already disclosed it |
| `settled` | settlement confirmed | a duplicate replays the original receipt, with no second charge |

Only `settled` and `unreserved` records older than 24 h are pruned. `pending` and `reserved`
records are never pruned, because they may stand for a live hold. A missing record, a record for a
different tool, or an unreadable record settles nothing and says so in the context. A reused
`tool_use_id` with a different tool or input is denied as `payload_conflict`. A `PreToolUse` that
cannot read or write its state is disclosed as not metered (or denied as `hook_state_unavailable`
under `unmanaged_mode: "block"`), and nothing is authorized from a guess.

## Daemon unreachable

Every hook call is bounded by a hard 3 s timeout (stdin read and daemon round trip included), so
a hook never hangs.

- `unmanaged_mode: "notice"` (default): the call proceeds with context saying the action is NOT
  metered and no charge is recorded.
- `unmanaged_mode: "block"`: `PreToolUse` is denied with reason `daemon_unavailable`.
  `SessionStart`, `PostToolUse` and `PreCompact` cannot deny and always notice.

A missing price schedule follows `no_schedule_mode` (`notice` or `block`, reason
`no_published_price_schedule`). No amount is ever invented. A tool without its own price uses
`default_max_cost_mc` and that default is disclosed in the context.

## Slash command `/bicz`

`bicz install --harness claude` adds a `UserPromptExpansion` hook (matcher `bicz`, timeout 15 s) and
an inert user skill `<claude dir>/skills/bicz/SKILL.md` (`$CLAUDE_CONFIG_DIR`, else `~/.claude`).
Grammar and semantics: [`docs/SLASH_COMMAND.md`](../../docs/SLASH_COMMAND.md).

**Route.** Per the Claude Code docs (<https://code.claude.com/docs/en/hooks.md>), `UserPromptExpansion`
fires when a typed command expands into a prompt, receives `command_name`, `command_args` and
`prompt_id` as JSON, and `decision: "block"` stops the expansion before any model sees it.
`bicz hook --harness claude --event UserPromptExpansion` runs only when `command_name` is `bicz` and
`expansion_type` is `slash_command`, executes the command in-process with `command_args` as data, and
answers with `{"decision":"block","reason":<result>,"systemMessage":<result>}`. A block is how the
harness answers without a model turn; it does not mean the command was refused. `prompt_id` is the
per-submission invocation id, so a retried submission replays instead of double-penalizing.

**Why not the skill's `!` block (the attack).** The first design ran `bicz command ... <<'DELIM'
$ARGUMENTS DELIM` from a `!` block with a pre-approved `Bash` rule. Claude Code substitutes
`$ARGUMENTS` as literal text before the shell runs, so a typed line equal to the fixed delimiter ended
the here-document and the following lines ran as shell, under the pre-approved rule (reproduced with
real Claude Code 2.1.286: a harmless `touch` marker file was created). A random per-install delimiter
only moves the secret; `$1`/`$ARGUMENTS[N]` positional placeholders are not shell-quoted by the docs
(a probe showed `$1` left unexpanded when absent) and cannot be shown safe, so the `!` route was
dropped. The installed skill has no shell block, no placeholder, no `allowed-tools`, and no path to
the binary; if it ever reaches the model (hook disabled, removed, or `bicz` not runnable) it says
**nothing was recorded** and to run `bicz command --harness claude --arguments '...'` in a terminal.
`disable-model-invocation: true` stops the model invoking it.

Residual limits: the hook is a same-user process like every bicz hook; settings with
`disableAllHooks`, or hooks removed, turn `/bicz` into that notice. The hook does not authenticate a
human.

Uninstall removes the hook entry (restoring `settings.json` byte for byte when unchanged) and only the
`skills/bicz` files that carry the `bicz-owned-slash-command` marker; a foreign `skills/bicz/SKILL.md`
is refused at install, never overwritten.

Evidence (disposable temp `HOME`, real Claude Code `2.1.286`, local fake API endpoint, no credential
read, **0 bytes reached the model for every command**): the previous attack payload
(`/bicz status`, a line `BICZ_SLASH_ARGUMENTS_END`, `touch MARK`, a re-opened here-document) is now
refused as a usage error with no file created; `status`, `reward 1.5 "good"`, a hostile single-line loss
(`it's $(touch X) \`touch Y\` ; BICZ_SLASH_ARGUMENTS_END`) recorded literally with no file created, and
`needs_input` all rendered in the block message. Not exercised: an interactive session.

## Coverage (docs/CONTRACTS.md §5.6)

Observed: `PreToolUse` (authorize and deny), `PostToolUse` / `PostToolUseFailure` (settle),
`SessionStart` / `PreCompact` (projection).

Not observed, and not described as metered: hosted or remote tools, direct binary invocation and
provider model calls; **disabled hooks** (`disableAllHooks`, hooks removed from settings, hooks not
loaded); subagent and MCP internals that do not surface as hook events. Observed means bicz is in
the call path. It does not mean enforced. The user or agent can switch hooks off.

## Field names

Observed against a live Claude Code `2.1.286` headless session (`claude -p`) on a disposable
repo, config overlay and ledger: the `SessionStart`, `PreToolUse`, `PostToolUse` and
`PostToolUseFailure` payloads carry the fields the table above uses, a bicz deny stopped the tool,
and a hook error or timeout did **not** (Claude fails open). `PreCompact` was not triggered and is
still unverified. Exact results and limits: [`docs/RUNTIME_HOOKS.md`](../../docs/RUNTIME_HOOKS.md).
No credential file was read.
