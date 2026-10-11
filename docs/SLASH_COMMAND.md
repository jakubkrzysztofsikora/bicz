# Operator slash command: `/bicz`

Moved out of the top-level README, which now links here. The 60-second version is in the
[README](../README.md#60-second-demo).

One grammar, one executor (`bicz command`), reached from each harness's own command surface. It is
the operator action of `bicz loss` / `bicz reward` / `bicz status` with **no `--db` or `--identity`
flags**: the identity and ledger come from the harness's installed config
(`<bicz config dir>/<harness>.json`, written by `bicz install`).

```text
/bicz loss RULE "reason" ["correction"]   burn credits for a confirmed breach of RULE
/bicz reward AMOUNT "reason"              credit AMOUNT, a decimal credit count with up to 6 decimals
/bicz status                              balance, outstanding loss, role, newest penalty
```

- **Loss needs a correction.** Give it as the third argument, or it is reused from the correction
  saved by your earlier loss of the same RULE. With neither, **nothing is written** and you get a
  structured question (`needs_input`, exit 3, with the exact command to re-run); a terminal user is
  prompted instead. A correction is never invented, and an agent-proposed lesson is never used.
- **Reward amounts are credits, not microcredits.** `5` is 5,000,000 mc, `0.25` is 250,000 mc.
  Digits only, at most 6 decimals: no sign, exponent, separator, float, or rounding.
- **Quoting is the only syntax.** Text is data: `$(...)`, backticks, `;` and `$VAR` are literal and
  never reach a shell. The rule id has no spaces, so a reason typed in the rule slot is refused.
- **Retries do not double-penalize.** The event id is derived from what you typed (plus the
  harness's per-submission invocation id when it has one), so a retried submission replays the
  original event and issues no second burn or cue. Typing the identical command twice without an
  invocation id is also the same event; change the reason to record a distinct second loss.
- **Accountability is automatic.** A slash loss is a `confirmed_breach`: the agent gets the usual
  cue on its next projection, like any operator loss.

**Authority.** A slash command is an explicit operator invocation recorded under the local OS user,
with unsigned provenance (`[/bicz via <harness>, unsigned same-user]` in the recorded text and a
`slash-<action>-<harness>-<hash>` event id). It is **not** cryptographic authentication of a human.
No model chooses a rule, amount, reason, correction, penalty, or reward, and no judge is involved.

| Harness | Typed as | Route | Who executes the command |
| --- | --- | --- | --- |
| Claude Code 2.1.286 | `/bicz ...` | `UserPromptExpansion` hook (matcher `bicz`) receives the typed text as JSON, runs it, and blocks the expansion with the result. The `bicz` skill is only an inert notice for a hook that did not run | **`bicz hook`, directly.** No shell, no here-document, no model: a live run sent 0 bytes to the model |
| Codex 0.162.1 | `$bicz ...` | `UserPromptSubmit` hook receives the prompt as JSON, answers a prompt that *starts with* `$bicz`, and blocks it. Fallback when the hook is untrusted or off: the `bicz` skill makes the agent run one command | hook: **directly, no model**. Fallback: **prompt-mediated** (the model transcribes your text; Codex approvals apply) |
| Oh My Pi | `/bicz ...` | the extension registers a native command with `pi.registerCommand`; OMP runs it before any prompt reaches a model ([`adapters/omp`](../adapters/omp/README.md#operator-bicz-command)) | `bicz command`, by argv with the text on stdin: no shell, no model. **Verified in a real `omp --mode rpc` (18.2.11)** on a temp ledger: `/bicz status`, `reward`, and a `loss` without a correction came back as native notify |
| OpenCode V2 | `/bicz ...` | `ctx.command.transform` registers the command; the result is posted as a synthetic session message and no turn is resumed ([`adapters/opencode`](../adapters/opencode/README.md#operator-bicz-command)) | `bicz command`, no model. Plugin ABI verified against the published package; **V2 engine not run** |
| OpenCode V1 | `/bicz ...` | **weaker**: `command.execute.before` runs the CLI and replaces the prompt parts with the result, but V1 still sends those parts as a prompt, so a model turn relays the result | the ledger write happens before any model sees the turn; a model relays the output |

The typed text is JSON data from the harness to the hook to the ledger. It is never interpolated
into a shell command, a here-document or a prompt, so no typed line can end a delimiter or run a
command. (An earlier design embedded `$ARGUMENTS` in a fixed here-document; a typed line equal to the
delimiter ended it and the following lines ran as shell. That route was removed.) A multi-line
argument is refused by the parser.

Codex 0.162.1 has no user-defined slash commands: its custom prompts (`/prompts:name`) are
deprecated in favor of skills, so bicz installs no custom prompt and the literal `/bicz` is not
available there; `$bicz` is the supported route, and **Codex marks new hooks untrusted until you
review them in `/hooks`** (until then `$bicz` takes the weaker fallback). Install and remove with the normal
`bicz install --harness claude|codex` / `bicz uninstall` (dry-run shows the skill diff; other skills
are never touched). Details: [`adapters/claude`](../adapters/claude/README.md#slash-command-bicz),
[`adapters/codex`](../adapters/codex/README.md#slash-command-bicz).
