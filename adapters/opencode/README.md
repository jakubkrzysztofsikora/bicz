# bicz OpenCode adapters

`v1/bicz.ts` targets the V1 default-export plugin API and uses Node built-ins only. `v2/bicz.ts` targets `Plugin.define({ id: "bicz", setup(ctx) })` and imports the host-provided `@opencode/plugin` API. Both communicate over the `bicz.adapter/1` Unix-socket protocol. Their canonical semantics are in `internal/adapter/`.

## Coverage

| Generation | Observed | Unobserved |
| --- | --- | --- |
| V1 | real OpenCode 1.18.31 with a mock provider: `tool.execute.before` reserve/deny, `tool.execute.after` settlement, failed-tool settlement via `message.part.updated`, projection/receipt/penalty cue through `experimental.chat.system.transform`, `/bicz` through `command.execute.before` | `experimental.session.compacting` (compaction not triggered); `--pure` disables external plugins; TUI/ACP/desktop/web routes; provider calls, subagent internals, and unmanaged launches |
| V2 | real engine `opencode v2.0.26` with a mock provider: plugin load, `/bicz` (`ctx.command.transform`), `ctx.tool.hook("execute.before"/"execute.after")` reserve/settle and deny, `ctx.session.hook("context")` projection and receipt, `ctx.session.hook("compaction")` penalty cue | desktop/web/ACP routes; `context` with a penalty present on an ordinary request (only via the fixture); provider calls, subagent internals, and unmanaged launches |

Hooks are cooperative, not an OS security boundary. No adapter claims metering of paths it cannot observe. Plugin-disabled `--pure` runs are specifically unobserved.

## Compatibility

- V1: the stable `1.x` line (`opencode-ai` on npm); run here in a real `opencode` 1.18.31 (embedded Bun 1.3.14) against a mock provider, plus the TS mock harness. The plugin targets the hook shapes observed there (`docs/RUNTIME_OPENCODE.md`), not the shapes the mock harness used to assume.
- V2: the beta `2.0` line of `anomalyco/opencode`, published on npm as `@opencode/cli@2.0.26` (binary `opencode`, `opencode2`); the plugin ABI is pinned to `@opencode/plugin@2.0.26` (Promise API is its default entrypoint). There is no GitHub release for `v2.0.26` and no 2.x on the legacy `opencode-ai` package. The installed generation is detected from `opencode --version` (`opencode v2.0.26` resolves to 2) and can be forced with `--generation`.
- Hooks fire only for tools routed through the plugin; `--pure` mode and desktop/ACP/web routes without a fixture are unobserved (see Coverage above).

## Install and uninstall

Build bicz, then use `bicz install --harness opencode --account "$USER"` and `bicz uninstall --harness opencode`. Preview with `--dry-run`. Generation is detected from `opencode --version`; `--generation 1` or `--generation 2` overrides detection. The plugin is deployed to `$OPENCODE_CONFIG_DIR/plugins` when set, otherwise `$XDG_CONFIG_HOME/opencode/plugins` or `~/.config/opencode/plugins`. Adapter JSON is written below the OpenCode config-dir override when set, otherwise to bicz's XDG config root. Writes are atomic, idempotent, backed up on apply, and uninstall removes only bicz-owned files. Symlinks, foreign plugin files, and paths outside `$HOME` are refused.

## Fail-safe and pricing

The plugin authorizes only when a positive integer price is available from the published schedule. It never invents a price. Without a schedule, it discloses an unmetered action by default (`no_schedule_mode: notice`) or blocks when configured as `block`. If the daemon is unavailable, default `unmanaged_mode: notice` allows execution with an explicit unmetered notice; `block` denies. Neither V1's nor V2's tool hooks have a field the model reads, so both adapters queue notices and receipts per session and emit them in the next model request's system context (V1 `experimental.chat.system.transform`, V2 `ctx.session.hook("context")`), together with the projection and every active penalty cue. Settlement receipts are emitted only after successful daemon settlement. Tool outcomes default to ordinary failure, with no adapter-invented fine. V1 never calls `tool.execute.after` for a failed tool, so a tool part ending in `error` (observed via `message.part.updated`) is settled as an ordinary failure instead of leaving the reservation held.

V1 uses `opencode1:auth:<callID>` and `opencode1:settle:<callID>`. V2 scopes stable IDs by session: `opencode2:auth:<encoded-sessionID>:<encoded-callID>` and `opencode2:settle:<encoded-sessionID>:<encoded-callID>`.

## Operator `/bicz` command

`/bicz status`, `/bicz reward AMOUNT "reason"`, and `/bicz loss RULE "reason" ["correction"]` run the central `bicz command --harness opencode --arguments-stdin --json`. The executable comes from `bicz_path` in the adapter config, then `$BICZ_BIN`, then `bicz` on `PATH`. The typed text goes to stdin through an argv array, so no shell parses it. The command is an operator action: it is not a tool call, so nothing is authorized, held, or settled. A loss without a saved correction returns the structured question and writes nothing.

| Generation | Route | Model involvement |
| --- | --- | --- |
| V2 | `ctx.command.transform` registers a `bicz` command; its `execute` runs the CLI and posts the result with `ctx.session.synthetic({ resume: false })` | none: verified in a real v2.0.26 engine. The result is admitted to the session inbox as a `synthetic` item (`delivery: steer`); no turn runs, no provider request is made, and it becomes a session message only when the session's next turn starts |
| V1 (weaker) | the plugin `config` hook adds a `bicz` command with an inert template; `command.execute.before` runs the CLI with the raw arguments and replaces the prompt parts (in place: V1 1.18.31 ignores reassigning `output.parts`) with the result | verified in a real 1.18.31: the ledger write happens before any model sees the turn (reward event 53 ms before the model request carrying it), but V1 still sends the replaced parts as a prompt, so the model is asked to relay the result: not model-free |

## Verification status

Both generations were run in real engines (details, transcripts and ledger deltas: `docs/RUNTIME_OPENCODE.md`). V2 (`@opencode/cli@2.0.26`, `opencode v2.0.26`): the engine loads `plugins/bicz.ts`; `/bicz status`, `reward 3 "x"` and a no-correction `loss` run through `POST /api/session/:id/command` with matching ledger events and no model turn (a synthetic inbox item); with a local mock OpenAI-compatible provider, a real prompt reserved (hold 5000 mc) and settled a `read` tool call, the projection and receipt reached the model through the `context` hook, an insufficient-credit denial reached the engine as a tool error without running the tool, and the `compaction` hook carried a penalty cue. V1 (1.18.31): the same flows run after four fixes the real engine exposed: daemon calls failed under the embedded Bun 1.3.14 (half-closed socket), notices never reached the model, failed tools were never settled, and `/bicz` output was not read back. Not exercised: see the Coverage table. A mock provider means no model behavior was measured.
