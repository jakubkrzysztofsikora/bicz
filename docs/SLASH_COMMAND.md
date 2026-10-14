# Operator slash command: `/bicz`

Moved out of the top-level README, which now links here. The 60-second version is in the
[README](../README.md#60-second-demo).

One grammar, one executor (`bicz command`), reached from each harness's own command surface. It is
the operator action of `bicz loss` / `bicz reward` / `bicz status` with **no `--db` or `--identity`
flags**: the identity and ledger come from the harness's installed config
(`<bicz config dir>/<harness>.json`, written by `bicz install`).

```text
/bicz loss "reason"                       burn credits; the model infers RULE and correction from the transcript (needs an `inference` block)
/bicz loss RULE "reason" ["correction"]   burn credits for a confirmed breach of RULE
/bicz reward AMOUNT "reason"              credit AMOUNT, a decimal credit count with up to 6 decimals
/bicz reset [AMOUNT] ["reason"]           reset budget to AMOUNT (default 100 credits) to unblock agent without running reward logic
/bicz status                              balance, outstanding loss, role, newest penalty
/bicz rules                               list the default rules: id, tier, on/off, statement
```

- **Reset unblocks without running reward logic.** When an agent runs out of credits because rewards
  were forgotten, `/bicz reset` replenishes the balance up to AMOUNT (or the default baseline of 100
  credits). Unlike `/bicz reward`, it **never** runs reward logic: `outstanding_loss` is strictly
  unchanged (it does not repay prior penalties), lessons are not modified, and role warnings remain intact.
- **Loss needs a correction.** Give it as the third argument, or it is reused from the correction
  saved by your earlier loss of the same RULE, or, for a RULE listed by `/bicz rules`, the shipped
  default correction is used (the receipt says `source=default`; see
  [DEFAULT_POLICIES.md](DEFAULT_POLICIES.md)). A correction you type beats a saved one, which beats
  the shipped default. With none of these, **nothing is written** and you get a
  structured question (`needs_input`, exit 3, with the exact command to re-run); a terminal user is
  prompted instead. A correction is never invented by the session's own agent, and an
  agent-proposed lesson is never used: a shipped default is operator-supplied text from the
  catalog, not model output.
- **Optional inference (off by default).** An operator-added `inference` block in the harness's
  bicz config (`<config dir>/<harness>.json`) lets the slash executor ask an
  **operator-configured external OpenAI-compatible endpoint** — `endpoint`, `model`, and
  `api_key_env` are all yours to set, so the infer model is normally a *different model family*
  from the harness's own; the running harness and its provider are never involved — to complete
  missing parameters. Three triggers: (a) an unparsable `loss`-shaped vent (you swore at the agent
  instead of typing `RULE "reason"`) is completed into `rule`, `reason`, and `correction`; your
  typed text is a **locator, not content**: the model finds the failure hotspot in the session
  transcript (your angry message and the agent turn(s) leading up to it) and drafts a factual
  reason from it; your raw text is quoted verbatim as data in the evidence. (b) A parsed loss
  whose correction resolved nowhere gets a model-drafted correction
  (`source=model_inferred`). (c) **The effortless reward**: `/bicz reward <free text>` with no
  amount is completed into a credit amount (hard-capped at [1, 100]; the ceiling is absolute) and
  a factual reason from what the transcript shows the agent finished; an explicit amount is never
  rewritten. Precedence for corrections: typed > saved > shipped default > `model_inferred` >
  prompted. Every model-drafted parameter is validated (the rule must be a shipped catalog id,
  never invented; reason/correction pass the same bounds as typed text; a reward amount must sit
  inside the band) and the receipt and recorded evidence say `model_inferred` plus the configured
  model name. An inferred command's idempotency key covers your raw text, so a retry — even with
  different model wording — can never burn or credit twice (a differently-worded retry is refused
  by the ledger and reported as already-recorded). A missing key, timeout, or invalid reply falls
  back to today's exact behavior (usage error / `needs_input`) and writes nothing. The endpoint must be
  `https://` (or `http://` on loopback/private addresses, for a local Ollama or llama.cpp); the
  key is read only from the named environment variable. Inference runs in the hook/`bicz command`
  process, never in the supervisor daemon.
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
| Codex 0.162.1 | `$bicz ...` | `UserPromptSubmit` hook receives the prompt as JSON, answers a prompt that *starts with* `$bicz`, and blocks it. Fallback when the hook is untrusted or off: the `bicz` skill makes the agent run one command | hook: **directly, no model; verified in a real `codex exec` 0.162.1** (`status`, `reward`, no-correction `loss`, 0 model requests, temp ledger). Fallback: **prompt-mediated** (the model transcribes your text; Codex approvals apply) |
| Oh My Pi | `/bicz ...` | the extension registers a native command with `pi.registerCommand`; OMP runs it before any prompt reaches a model ([`adapters/omp`](../adapters/omp/README.md#operator-bicz-command)) | `bicz command`, by argv with the text on stdin: no shell, no model. **Verified in a real `omp --mode rpc` (18.2.11)** on a temp ledger: `/bicz status`, `reward`, and a `loss` without a correction came back as native notify |
| OpenCode V2 | `/bicz ...` | `ctx.command.transform` registers the command; the result is posted as a synthetic session message and no turn is resumed ([`adapters/opencode`](../adapters/opencode/README.md#operator-bicz-command)) | `bicz command`, no model: **verified in a real `opencode v2.0.26`** (`status`, `reward`, no-correction `loss` through the session command route, no model request, queued as a synthetic inbox item) |
| OpenCode V1 | `/bicz ...` | **weaker**: `command.execute.before` runs the CLI and replaces the prompt parts with the result, but V1 still sends those parts as a prompt, so a model turn relays the result | the ledger write happens before any model sees the turn; a model relays the output. **Verified in a real `opencode` 1.18.31**: the reward event was written 53 ms before the model request that relays it |

The typed text is JSON data from the harness to the hook to the ledger. It is never interpolated
into a shell command, a here-document or a prompt, so no typed line can end a delimiter or run a
command. (An earlier design embedded `$ARGUMENTS` in a fixed here-document; a typed line equal to the
delimiter ended it and the following lines ran as shell. That route was removed.) A multi-line
argument is refused by the parser.

Codex 0.162.1 has no user-defined slash commands: its custom prompts (`/prompts:name`) are
deprecated in favor of skills, so bicz installs no custom prompt and the literal `/bicz` is not
available there; `$bicz` is the supported route. **To trust the hooks on your machine: run `codex`, type `/hooks`, and trust
the bicz entries** (Codex skips new or changed hooks until then, and `$bicz` takes the weaker fallback; observed). Install and remove with the normal
`bicz install --harness claude|codex` / `bicz uninstall` (dry-run shows the skill diff; other skills
are never touched). Details: [`adapters/claude`](../adapters/claude/README.md#slash-command-bicz),
[`adapters/codex`](../adapters/codex/README.md#slash-command-bicz).
