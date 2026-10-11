# OpenCode V2 runtime validation

## Environment and artifact availability

- Installed CLI: `opencode --version` returned `1.18.31` (V1), read-only; it was never run against the plugin.
- Node: `v26.5.0`.
- `npm view @opencode/plugin@2.0.26` resolved the official package at `@opencode/plugin@2.0.26` with `dist.tarball=https://registry.npmjs.org/@opencode/plugin/-/plugin-2.0.26.tgz`.
- `npm view opencode-ai@2.0.26 version` returned registry `E404`. The V2 CLI is published as `@opencode/cli@2.0.26` instead (see "Real V2 engine run" below).
- The official V2 plugin package and its dependencies were installed with `npm install --prefix /tmp/opencode-v2-abi --no-save --ignore-scripts @opencode/plugin@2.0.26 typescript@5.7.3`. This did not change the project or global OpenCode settings.

## Primary-source contract checked

Pinned to tag `v2.0.26`:

- [registration.ts](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/plugin/src/promise/registration.ts): `Hooks<Spec>` callbacks take one typed input, return `Promise<void> | void`, and registration returns a `Promise<Registration>` with async `dispose()`.
- [tool.ts](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/plugin/src/promise/tool.ts): `execute.before` receives one object containing `tool`, `sessionID`, `agent`, `messageID`, `id`, and mutable `input`; `execute.after` receives the identity/input plus `status: "completed"` + `result` or `status: "error"` + `error`.
- [session.ts](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/plugin/src/promise/session.ts): `context` and `compaction` inputs have mutable `system: Array<SystemPart>` and `sessionID`.
- [messages.ts](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/ai/src/schema/messages.ts): a `SystemPart` is `{ type: "text", text: string, ... }`.
- [plugin README](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/plugin/src/README.md): session context is changed directly in the one-argument callback; setup may await hook registrations and return cleanup.
- [plugin.ts](https://github.com/anomalyco/opencode/blob/v2.0.26/packages/plugin/src/promise/plugin.ts) exports `Plugin.define`; the published package implementation returns the definition. The package host subpath exports `Host.load`.

## Exercise performed

The adapter was copied to the isolated temporary workspace and typechecked against the actual package declarations with:

```sh
cp adapters/opencode/v2/bicz.ts /tmp/opencode-v2-abi/bicz.ts
cd /tmp/opencode-v2-abi
./node_modules/.bin/tsc -p tsconfig.json
```

The reusable regression fixture is `adapters/opencode/v2/abi-smoke.mjs`. Run it with the official package and a temporary bicz binary:

```sh
OPENCODE_PLUGIN_ROOT=/tmp/opencode-v2-abi/node_modules/@opencode/plugin \
BICZ_BIN=/tmp/bicz-opencode \
node adapters/opencode/v2/abi-smoke.mjs
```

It loads the adapter with the real `@opencode/plugin@2.0.26` `Host.load` and the adapter's actual `Plugin.define` import, not a loader shim. It registers callbacks in an in-process context registry matching the published types (not the OpenCode engine), then exercises them against a real temporary `bicz serve` socket and ledger. Covered behavior: allow/receipt, insufficient-credit throw, duplicate settle once, same call ID independently scoped across sessions, session `system` projection/receipt mutation, compaction mutation, retrying an uncertain settlement after daemon interruption, and daemon-down notice/block behavior. All ledger/config/socket files live in a fresh system temp directory; the fixture does not touch OpenCode config or credentials.

Observed output from the fixture:

```text
{"package":"@opencode/plugin@2.0.26","loader":"Host.load","allowAndReceipt":true,"duplicateSettlement":"once","crossSessionCallID":"independent","denial":"insufficient_credit: insufficient_credit: \"smoke\" cannot reserve 2000000 microcredits (spendable=999980)","settlementRetry":"settled after daemon recovery","contextMutation":"projection/receipt injected into system","compactionMutation":true,"daemonDownNotice":"bicz: connect ENOENT /var/folders/f7/b86z565n4wj8jwf0htfwqb280000gn/T/bicz-opencode-v2-abi-Kvf8TT/bicz.sock; recording unavailable and this action is NOT metered\nbicz: projection unavailable (connect ENOENT /var/folders/f7/b86z565n4wj8jwf0htfwqb280000gn/T/bicz-opencode-v2-abi-Kvf8TT/bicz.sock)","daemonDownBlock":"daemon_unavailable: bicz: connect ENOENT /var/folders/f7/b86z565n4wj8jwf0htfwqb280000gn/T/bicz-opencode-v2-abi-Kvf8TT/bicz.sock; recording unavailable and this action is NOT metered"}
```

## Real V2 engine run (opencode v2.0.26)

Provenance: `@opencode/cli@2.0.26` on npm (not `opencode-ai`; the GitHub repo has no release for `v2.0.26`, `gh release view v2.0.26 -R anomalyco/opencode` returns `release not found`). Its darwin-arm64 binary package was installed into `/tmp` only:

```sh
npm install --no-save @opencode/cli@2.0.26 @opencode/cli-darwin-arm64@2.0.26   # in /tmp/oc2/inst
shasum -a 256 node_modules/@opencode/cli-darwin-arm64/bin/opencode
# 1b6418a3bd4211344d8a2b75d7d4367b24283ae548bd6b896cf717f6f8858f26
opencode --version
# opencode v2.0.26
```

Isolation: temp `HOME`, `XDG_*_HOME`, `OPENCODE_CONFIG_DIR`, a temp ledger and bicz socket, and a freshly built bicz. The engine ran as `opencode serve --hostname 127.0.0.1 --port 4199` (V2's own server; its API is what the TUI uses) with `HTTP(S)_PROXY` pointed at a local capture proxy that logs and 502s every outbound request. No provider, credential or login was configured. The plugin was deployed with the real installer, and the V2 binary on `PATH` auto-detects as generation 2:

```sh
bicz install --harness opencode --generation 2 --account oc2test --ledger $HOME/ledger.db
# INSTALLED harness=opencode account=oc2test ledger=/tmp/oc2/home/ledger.db
bicz init --db $HOME/ledger.db && bicz open --db $HOME/ledger.db --identity oc2test --capital 10000000 --reason "..."
bicz serve --db $HOME/ledger.db --socket <socket from install>
```

Plugin load (engine log, then the engine's own command list):

```text
level=INFO message="location services booted" directory=/tmp/oc2/home/work
level=INFO msg="loading plugin" id=/tmp/oc2/home/oc-config/plugins/bicz.ts entrypoint=file:///tmp/oc2/home/oc-config/plugins/bicz.ts
level=INFO message=event event.type=command.updated
GET /api/command -> init, review, bicz ("Operator ledger command: status | rules | loss RULE ...")
```

Command route. V2's clients (TUI, mini, ACP) run a typed `/bicz ARGS` as `POST /api/session/:id/command {name:"bicz", text:"ARGS"}` ("Execute a slash command callback immediately"); `text` is the arguments only, which is what the plugin reads as `invocation.prompt.text`. The same route was driven directly:

```text
ledger BEFORE   events=1 balance=10000000 lessons=0
POST /command {"name":"bicz","text":"status"}               -> 204   ledger events=1 (unchanged)
POST /command {"name":"bicz","text":"reward 3 \"x\""}       -> 204   ledger events=2 balance=13000000, event slash-reward-opencode-355ea60f5f349d6ac85a
POST /command {"name":"bicz","text":"loss RULE \"r\""}      -> 204   ledger events=2 (unchanged; status needs_input, nothing written)
ledger AFTER    events=2 balance=13000000 outstanding_loss=0 lessons=0; bicz verify: INTEGRITY OK events=2
```

Where the result went. Each call produced one `synthetic` item with `delivery:"steer"` in `GET /api/session/:id/inbox`, with the plugin's metadata (`{source:"bicz", status:"ok"|"needs_input", event_id}`), and the reward event id appears in the text. Session state afterwards: `GET /api/session/:id/message` is empty, `cost=0`, no tokens, `GET /api/session/active` is `{}`: no turn ran and no assistant message exists. The capture proxy saw only three `CONNECT models.opencode.ai:443` (the engine's model-catalog refresh at location boot, answered 502) and nothing else, including nothing when `status` was run again later: no provider request was made.

Engine behavior to know: with `resume:false` the synthetic is admitted to the session inbox and is not materialized as a message until the session's next turn. The operator sees the result via the inbox; a model turn that starts later receives it as steered synthetic input. This is how V2 defines `resume:false`, not something the plugin can change without resuming a turn.

A second engine fact: the first `GET /api/command` right after location boot returned an empty list (plugins finish loading asynchronously); the command was present a moment later.

Constant table checked against the engine source at tag `v2.0.26` (`9b4ec57`): `ctx.command.transform` + `editor.add({name,description,execute})`, `invocation.prompt.text`, and `ctx.session.synthetic({sessionID,text,description,metadata,resume})` were exercised live. `execute.before`/`execute.after` event fields (`tool, sessionID, agent, messageID, id, input`, and `status` + `result`/`error`) match `packages/core/src/tool.ts`; the `context`/`compaction` hooks take a mutable `system` array exactly as the engine's own `opencode.prompt.identity` plugin uses them. These were read in source, not run.

## Remaining runtime gap

Not exercised in the real engine: `execute.before`/`execute.after` (reserve/settle, denial propagation), `context`, and `compaction`. V2 exposes no model-free way to run them: its `session.shell` route calls the shell service directly, not the tool dispatcher, and the other hooks run only while building a model request. Exercising them needs a model turn (a provider), which this verification deliberately did not configure. Their callback arity, denial semantics and session mutation are covered by the ABI fixture above (published `@opencode/plugin@2.0.26` with `Host.load`, real daemon), not by the engine. Client-route coverage (desktop, web, ACP) and `--pure`-style plugin-disabled runs remain unobserved. No model session, provider credential, login or paid backend was used.

The same ABI fixture also writes a confirmed penalty with the real `bicz loss` CLI (default disposition) and asserts that the `context` and `compaction` hooks carry the accountability cue (docs/CONTRACTS.md §6.4). V2 has no per-tool poll: a mid-turn penalty arrives with the next `context` hook call. V1 polls on `tool.execute.before`.
