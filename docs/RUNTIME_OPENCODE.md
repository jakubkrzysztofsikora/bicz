# OpenCode V2 runtime validation

## Environment and artifact availability

- Installed CLI: `opencode --version` returned `1.18.31` (V1), read-only.
- Node: `v26.5.0`.
- `npm view @opencode/plugin@2.0.26` resolved the official package at `@opencode/plugin@2.0.26` with `dist.tarball=https://registry.npmjs.org/@opencode/plugin/-/plugin-2.0.26.tgz`.
- `npm view opencode-ai@2.0.26 version` returned registry `E404`; no V2 CLI binary was installed or used.
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

## Remaining runtime gap

This is validation against the official published plugin package and host loader, plus a real bicz daemon. The context registry and callback invocations are a focused ABI fixture, not a running OpenCode engine. No V2 `opencode` executable was available; `opencode-ai@2.0.26` is absent from npm and the installed binary is V1. Therefore this exercise does not establish that a V2 engine actually loads the plugin, dispatches tool hooks, propagates a thrown denial to its tool executor, or applies the session mutation in a live request. No model session, provider credential, login, or paid backend was used. A pinned V2 CLI/runtime artifact is the missing prerequisite for those checks.

The same ABI fixture also writes a confirmed penalty with the real `bicz loss` CLI (default disposition) and asserts that the `context` and `compaction` hooks carry the accountability cue (docs/CONTRACTS.md §6.4). V2 has no per-tool poll: a mid-turn penalty arrives with the next `context` hook call. V1 polls on `tool.execute.before`.
