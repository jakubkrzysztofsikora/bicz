# OpenCode runtime validation (V2 and V1)

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

Constant table checked against the engine source at tag `v2.0.26` (`9b4ec57`) and then run: `ctx.command.transform` + `editor.add({name,description,execute})`, `invocation.prompt.text`, `ctx.session.synthetic({sessionID,text,description,metadata,resume})`, and, in the next section, `execute.before`/`execute.after` (`tool, sessionID, agent, messageID, id, input`; `status` + `result`/`error`) and the `context`/`compaction` hooks with their mutable `system` array. No name had to change.

## V2 tool hooks, deny, context and compaction (mock provider, same engine)

V2 has no model-free way to run these: `session.shell` calls the shell service directly, not the tool dispatcher, and the other hooks run only while building a model request. So a local mock OpenAI-compatible provider was used (`/tmp/oc2/mock-openai.mjs`, Node built-ins only, `127.0.0.1:4197`): for a request with tools and no tool result it streams one scripted `read` tool call on a temp file, for a request ending in a tool result it streams a final text, and it logs every request. It was configured in the temp config as `providers.mock` (`package: "@ai-sdk/openai-compatible"`, `settings.baseURL`, `settings.apiKey: "fake-key-not-real"`, model `mock-1`); the engine ran it with its bundled provider code and sent `Authorization: Bearer fake-key-not-real`. No real credential, login or paid backend was used. A price schedule was published on the temp ledger (`bicz price --version 1`, a policy file, and `schedule_path` + `policy_version` in the adapter config; schedule `read` = 5000 mc).

Reserve and settle. `POST /api/session/:id/prompt {"text":"please read hello.txt"}` ran a real turn: model request 1 (tools advertised), a real `read` tool call, model request 2 (tool result). Ledger delta:

```text
before  events=3 balance=13000000 held=0
seq 4  spend  amount 0      res:opencode2:auth:<session>:call_mock_1     payload phase=reserve state=held amount=5000 held_after=5000 tool=read harness=opencode-v2
seq 5  spend  amount -5000  settle:opencode2:auth:<session>:call_mock_1  payload phase=settle class=completed charged=5000 released=5000 fine=0
after   events=5 balance=12995000 held=0
```

Context hook. The `system` of model request 1 ended with `bicz context: identity oc2test, role active, ledger seq 3 | balance 13000000 mc | held 0 mc | spendable 13000000 mc | policy version 1 ...`; request 2 carried `ledger seq 5 | balance 12995000 mc` and, queued by the settle, `bicz receipt: completed, charged 5000 mc, fine 0`.

Insufficient credit. With the schedule set to `read` = 50000000 mc and spendable 12995000, the same prompt produced no ledger event and no reserve; the engine's tool part ended `status: "error"` with `insufficient_credit: insufficient_credit: "oc2test" cannot reserve 50000000 microcredits (spendable=12995000)`, the file was not read (no content), and the mock's next request carried that text as the tool result. A thrown denial from `execute.before` reaches the engine as a tool error and the tool does not run.

Compaction and penalty cue. A confirmed penalty was written with the real CLI (`bicz loss ... --event oc2-loss-1`, -1000 mc, role escalation). `POST /api/session/:id/compact` made the engine issue a summarisation request to the mock (last message `You MUST summarize the conversation above ...`); its `system` carried `bicz accountability record: ... violated abi_rule ... (loss event oc2-loss-1)` through the `compaction` hook. The same projection renderer feeds the `context` hook, which carried the (penalty-free) projection on the ordinary requests above; an ordinary request with a penalty present was not run on V2.

`/bicz` with a reachable provider. With the mock listening, `/bicz status` and `reward 2 "y"` through the command route left the mock's request count unchanged (2 before, 2 after) and added exactly one ledger event (the reward). The full V2 ledger afterwards: `grant`, `reward`, `price`, `spend` reserve, `spend` settle, `loss`, `role`, `reward` (8 events).

## Real V1 engine run (OpenCode 1.18.31)

Binary: `opencode` on `PATH` (`~/.opencode/bin/opencode`), `opencode --version` = `1.18.31`; it embeds Bun 1.3.14 (`BUN_BE_BUN=1 opencode --version` prints `1.3.14`; V2's binary embeds Bun 1.4.2). It was run only with a temp `HOME`, `XDG_*_HOME`, `OPENCODE_CONFIG_DIR=/tmp/oc1/home/oc-config`, a temp ledger and socket, and the same mock provider (port 4196) configured as V1 `provider.mock` (`npm: "@ai-sdk/openai-compatible"`, fake key). `bicz install --harness opencode --generation 1` deployed the plugin; `opencode run ...` drove it. V1 installed `@opencode-ai/plugin@1.18.31` into the temp config dir.

Three defects of the previous V1 plugin only showed up here (the TS mock harness could not see them), and are fixed in `adapters/opencode/v1/bicz.ts`:

1. Every call to the daemon failed. The plugin wrote the request with `socket.end(request)`; under Bun 1.3.14 that drops the readable side, so the plugin read an empty response (`rpc:authorize len=0`, "malformed bicz response") although the daemon had processed the request. The reserve was recorded, the plugin treated it as daemon-unavailable, allowed the call unmetered and never settled: holds accumulated (`held` 5000 per call). A probe run with the engine's own Bun showed `end(req)` -> 0 bytes and `write(req)` (no half-close) -> the full reply under Bun 1.3.14; both work under Bun 1.4.2 and Node. The supervisor decodes one JSON value per connection, so the plugin now writes without half-closing and takes the reply as soon as it parses.
2. No notice, receipt, projection or penalty cue ever reached the model. The plugin set `output.additionalContext`, a field V1's tool hooks do not read (and discarded the projection on session events). Real shapes: `tool.execute.before` gets `{tool, sessionID, callID}` with output `{args}`; `tool.execute.after` gets `{tool, sessionID, callID, args}` with output `{title, output, metadata, attachments}`. `experimental.chat.system.transform` gets `{sessionID, model}` with a mutable `output.system: string[]` and does reach the model (a marker pushed there appeared in every mock request). The plugin now queues notices and receipts per session and emits the projection (all active cues) plus the queue from `experimental.chat.system.transform`.
3. Failed tools were never settled: V1 does not call `tool.execute.after` when a tool fails (a permission-rejected `read` left the reserve held). A failed tool is visible as a `message.part.updated` event whose tool part has `state.status === "error"`; the plugin now settles it as an ordinary failure (charge 5000 mc, no fine). Separately, the `/bicz` hook's `output.parts = [...]` assignment was not read back by V1 (the model saw the inert template and nothing was written); mutating the array in place works.

Follow-up: the V2 plugin had the same `socket.end(request)` pattern. It worked only because V2's engine embeds Bun 1.4.2 (probe above), so it now uses the same no-half-close, first-complete-JSON rpc. Re-run after the change in the real `opencode v2.0.26` engine (fresh identity `oc2b`, same mock provider, plugin reinstalled with `bicz install --harness opencode --generation 2 --account oc2b`): the reinstall kept `schedule_path` and `policy_version` and refreshed `account_id`; a real prompt wrote seq 10 `res:opencode2:auth:<session>:call_mock_..._1` (spend, 0) and seq 11 `settle:...` (spend, -5000), balance 10000000 -> 9995000, held 0; the second model request's system carried `bicz context: identity oc2b ... ledger seq 11 | balance 9995000 mc` and `bicz receipt: completed, charged 5000 mc, fine 0`; `/bicz status` and `reward 1 "z"` left the mock's request count at 2 and queued two synthetic inbox items (the reward with event id `slash-reward-opencode-3308eb5de643b7d04fdf`).

Fixed-plugin runs (ledger deltas on the V1 temp ledger). `held` also carries 25000 mc of reservations left by the pre-fix runs above, kept as evidence; the later `bicz loss` revoked them (`held_revoked=5`):

```text
reserve+settle  read ok:        seq 12 reserve res:opencode1:auth:<callID> (amount 0)   seq 13 settle -5000 "settle completed"   balance 9990000 -> 9985000
                model request 21 (tool result) system: "bicz context: ... balance 9985000 mc ..." + "bicz receipt: completed, charged 5000 mc, fine 0"
failed tool     read rejected: seq 14 reserve   seq 15 settle -5000 "settle ordinary_failure"   (V1: "The user rejected permission to use this specific tool call.")
deny            read priced 50000000: no ledger event; tool state error "insufficient_credit: ... cannot reserve 50000000 microcredits (spendable=9955000)"; mock request carried it as the tool result
```

`/bicz` through V1's command route (`opencode run --command bicz ARGS`; V1's `command.execute.before` gets `{command, sessionID, arguments}` with output `{parts}`):

```text
ledger 15 events
run --command bicz status        -> 15 events; the mock model's request carried "Relay the following bicz operator command result ... BICZ status identity=oc1test ... events=15"
run --command bicz reward 3 x    -> 16 events; event slash-reward-opencode-7b6162fe2a0381a73e77 at 02:47:10.258193Z; the model request carrying "BICZ reward recorded ..." arrived at 02:47:10.311Z
run --command bicz loss RULE r   -> 16 events (nothing written); the model received "BICZ loss needs input ... Nothing was recorded."
```

So the ledger write happens before the model turn (53 ms earlier for the reward), and, as documented, V1 still sends the replaced text to the model, which relays it: no model-free route on V1. A single quoted argv (`run --command bicz 'reward 3 "x"'`) reached the hook with the whole string wrapped in an extra pair of quotes, and the command parser rejected it as an unknown action (nothing written); separate argv work. Typed `/bicz ...` in the V1 TUI was not exercised.

Penalty cue on V1: after `bicz loss ... --event oc1-loss-1` (confirmed breach), the next `opencode run "hello after a penalty"` sent the mock a `system` ending `bicz context: identity oc1test, role warned, ... | bicz accountability record: operator-confirmed penalties ...` on both requests of the turn.

## Remaining runtime gap

Not exercised in a real engine: V1 `experimental.session.compacting` (compaction was not triggered on V1; only `system.transform` carries the projection there), the V1 TUI/ACP/desktop/web routes, the V2 desktop/web/ACP routes, `--pure`-style plugin-disabled runs, subagent internals and provider-hosted tools. The model on both engines was the mock provider, so nothing here measures any model's behavior. V1 settlement of a failed tool via `message.part.updated` ran for one failure mode (a permission-rejected `read`); tool exceptions, timeouts and aborts were not run. No real credential, login or paid backend was used.

The ABI fixture (published `@opencode/plugin@2.0.26`, `Host.load`) also writes a confirmed penalty with the real `bicz loss` CLI and asserts that the V2 `context` and `compaction` hooks carry the accountability cue (docs/CONTRACTS.md §6.4); in the real engine the `compaction` path was run (above). V2 has no per-tool poll: a mid-turn penalty arrives with the next `context` hook call. V1 carries every active cue on each `system.transform`.
