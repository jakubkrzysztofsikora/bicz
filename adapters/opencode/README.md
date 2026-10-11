# bicz OpenCode adapters

`v1/bicz.ts` targets the V1 default-export plugin API and uses Node built-ins only. `v2/bicz.ts` targets `Plugin.define({ id: "bicz", setup(ctx) })` and imports the host-provided `@opencode/plugin` API. Both communicate over the `bicz.adapter/1` Unix-socket protocol. Their canonical semantics are in `internal/adapter/`.

## Coverage

| Generation | Observed | Unobserved |
| --- | --- | --- |
| V1 | `tool.execute.before` authorize/deny, `tool.execute.after` settlement, configured session events for projection | `--pure` disables external plugins; desktop/ACP/web per-route coverage has no fixture; provider calls, subagent internals, and unmanaged launches |
| V2 | `/bicz` command (`ctx.command.transform`) and plugin load in a real V2 engine (`opencode v2.0.26`); `ctx.tool.hook("execute.before"/"execute.after")` and `ctx.session.hook("context"/"compaction")` against the published package only | tool and session hooks have not run in the engine (no model-free way to trigger them; needs a provider turn); per-client route coverage (desktop/web/ACP); provider calls, subagent internals, and unmanaged launches |

Hooks are cooperative, not an OS security boundary. No adapter claims metering of paths it cannot observe. Plugin-disabled `--pure` runs are specifically unobserved.

## Compatibility

- V1: the stable `1.x` line (`opencode-ai` on npm); tested here only against a mock harness.
- V2: the beta `2.0` line of `anomalyco/opencode`, published on npm as `@opencode/cli@2.0.26` (binary `opencode`, `opencode2`); the plugin ABI is pinned to `@opencode/plugin@2.0.26` (Promise API is its default entrypoint). There is no GitHub release for `v2.0.26` and no 2.x on the legacy `opencode-ai` package. The installed generation is detected from `opencode --version` (`opencode v2.0.26` resolves to 2) and can be forced with `--generation`.
- Hooks fire only for tools routed through the plugin; `--pure` mode and desktop/ACP/web routes without a fixture are unobserved (see Coverage above).

## Install and uninstall

Build bicz, then use `bicz install --harness opencode --account "$USER"` and `bicz uninstall --harness opencode`. Preview with `--dry-run`. Generation is detected from `opencode --version`; `--generation 1` or `--generation 2` overrides detection. The plugin is deployed to `$OPENCODE_CONFIG_DIR/plugins` when set, otherwise `$XDG_CONFIG_HOME/opencode/plugins` or `~/.config/opencode/plugins`. Adapter JSON is written below the OpenCode config-dir override when set, otherwise to bicz's XDG config root. Writes are atomic, idempotent, backed up on apply, and uninstall removes only bicz-owned files. Symlinks, foreign plugin files, and paths outside `$HOME` are refused.

## Fail-safe and pricing

The plugin authorizes only when a positive integer price is available from the published schedule. It never invents a price. Without a schedule, it discloses an unmetered action by default (`no_schedule_mode: notice`) or blocks when configured as `block`. If the daemon is unavailable, default `unmanaged_mode: notice` allows execution with an explicit unmetered notice; `block` denies. V1 attaches notices/receipts through its hook outputs; V2 queues them into the next mutable session context because V2 tool hooks have no output parameter. Settlement receipts are emitted only after successful daemon settlement. Tool outcomes default to ordinary failure, with no adapter-invented fine.

V1 uses `opencode1:auth:<callID>` and `opencode1:settle:<callID>`. V2 scopes stable IDs by session: `opencode2:auth:<encoded-sessionID>:<encoded-callID>` and `opencode2:settle:<encoded-sessionID>:<encoded-callID>`.

## Operator `/bicz` command

`/bicz status`, `/bicz reward AMOUNT "reason"`, and `/bicz loss RULE "reason" ["correction"]` run the central `bicz command --harness opencode --arguments-stdin --json`. The executable comes from `bicz_path` in the adapter config, then `$BICZ_BIN`, then `bicz` on `PATH`. The typed text goes to stdin through an argv array, so no shell parses it. The command is an operator action: it is not a tool call, so nothing is authorized, held, or settled. A loss without a saved correction returns the structured question and writes nothing.

| Generation | Route | Model involvement |
| --- | --- | --- |
| V2 | `ctx.command.transform` registers a `bicz` command; its `execute` runs the CLI and posts the result with `ctx.session.synthetic({ resume: false })` | none: verified in a real v2.0.26 engine. The result is admitted to the session inbox as a `synthetic` item (`delivery: steer`); no turn runs, no provider request is made, and it becomes a session message only when the session's next turn starts |
| V1 (weaker) | the plugin `config` hook adds a `bicz` command with an inert template; `command.execute.before` runs the CLI with the raw arguments and replaces the prompt parts with the result | the ledger write happens before any model sees the turn, but V1 still sends the replaced parts as a prompt, so a model turn relays the result |

## V2 ABI status

The V2 plugin's callback arity and mutable session-system injection are verified against the published `@opencode/plugin@2.0.26` package. The plugin was then run in a real V2 engine (`@opencode/cli@2.0.26`, `opencode v2.0.26`, isolated HOME/config/ledger): the engine loaded `plugins/bicz.ts`, listed the `bicz` command, and `/bicz status`, `reward 3 "x"` and a no-correction `loss` ran through `POST /api/session/:id/command` (the route V2 clients use), with matching ledger events (status and needs_input wrote nothing; reward wrote one event), `needs_input` posted as a synthetic inbox item, no assistant message, zero tokens and no provider request. Not exercised in the engine: `execute.before/after` (reserve/settle), `context` and `compaction`, which run only inside a model turn; they remain verified against the published package only. See `docs/RUNTIME_OPENCODE.md`.
