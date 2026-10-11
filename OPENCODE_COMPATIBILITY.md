# OpenCode compatibility contract (V1 and V2)

Companion to `IMPLEMENTATION_PLAN.md`, milestone 0 (freeze contracts) and milestone 6 (adapters).
Pinned as of **2026-10-10**. Verification status is per-claim: **VERIFIED** = confirmed in an official source
(repo source, official docs pages, or npm registry); **CANDIDATE** = name/behavior taken from a third-party migration
playbook or search snippet, not yet confirmed in official V2 material — must be confirmed against the official pinned V2
code/docs or the installed binary before implementation; **[EVIDENCE GAP]** = unverifiable now and must be closed by a
fixture against the pinned installed binary, not by guessing. Absence of a command/flag from a docs page is **not**
evidence of removal. `DESIGN.md` remains the design authority for economic semantics; this file is the OpenCode
integration/install contract.

## 1. Version landscape (VERIFIED)

- Repository: `anomalyco/opencode` (MIT, TypeScript), default branch `dev`; separate `2.0` branch carries the V2 line
  ([repo metadata](https://api.github.com/repos/anomalyco/opencode), [branches](https://api.github.com/repos/anomalyco/opencode/branches?per_page=100)).
- Tags: both lines tag in the same repo — `v1.x.y` through `v1.18.35` (release published 2026-10-06) and `v2.0.0` … `v2.0.26`
  ([tags API](https://api.github.com/repos/anomalyco/opencode/tags?per_page=100)).
  **No GitHub release object exists for `v2.0.0` or `v2.0.26`** (both `releases/tags/*` return 404); V2 is a beta line without
  per-tag release artifacts in this repo as of the pin date.
- npm (`opencode-ai`, legacy registry name): `latest` = `1.18.35`; dist-tags include `dev` = `0.0.0-dev-202610092229`, `beta` =
  `0.0.0-beta-202608110357` and snapshot builds. **No stable 2.x was observed on this legacy package**; the V2 line is packaged
  as `@opencode/*` workspace packages (`@opencode/plugin`, `@opencode/client`) in repo source — publication status of those
  registry packages was not verified
  ([dist-tags](https://registry.npmjs.org/-/package/opencode-ai/dist-tags), [/latest](https://registry.npmjs.org/opencode-ai/latest)).
- `@opencode-ai/plugin` `latest` = `1.18.35` (V1 types) and **already ships V2 preview exports**:
  `./v2/effect`, `./v2/promise`, `./v2/effect/plugin`, `./v2/effect/integration` (Effect `4.0.0-beta.83` dependency)
  ([npm registry](https://registry.npmjs.org/@opencode-ai%2fplugin/latest)).
- **Pinned primary V2 source: tag `v2.0.26`.** `packages/plugin/package.json` confirms `@opencode/plugin` 2.0.26 with root
  export `./src/promise/index.ts` (plus `./effect`, `./host`, `./runtime`, `./tui`); `packages/client/package.json` confirms
  `@opencode/client` 2.0.26 with `./promise`, `./effect`, `./solid`, `./service` exports; both depend on
  `@opencode/protocol`/`@opencode/schema`
  ([plugin package.json](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/package.json),
  [client package.json](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/client/package.json)).
  The **Promise API is the default `@opencode/plugin` entrypoint**.
- Desktop app (BETA): `opencode-desktop-*.dmg` (`opencode-desktop-darwin-aarch64.dmg` on `2.0` branch), Windows `.exe`,
  Linux `.deb/.rpm/AppImage`; Homebrew cask `opencode-desktop`, Scoop `extras/opencode-desktop`
  ([README dev](https://raw.githubusercontent.com/anomalyco/opencode/dev/README.md), [README 2.0](https://raw.githubusercontent.com/anomalyco/opencode/2.0/README.md)).
- Install script `https://opencode.ai/install` (path priority `$OPENCODE_INSTALL_DIR` → `$XDG_BIN_DIR` → `$HOME/bin` →
  `$HOME/.opencode/bin`). `opencode.ai` returns HTTP 403 to direct fetches **[EVIDENCE GAP: cannot confirm which version the
  script serves today]**; the official migrate-from-V1 page states "the V2 curl installer replaces the V1 binary" (search
  snippet of [opencode.ai/v2/docs/migrate-v1](https://opencode.ai/v2/docs/migrate-v1), full page not fetchable — CANDIDATE until
  re-read).

## 2. What V2 actually is (architecture check, VERIFIED)

V2 is **not** an IDE-only replacement of the CLI plugin engine. Verified statements:

- "OpenCode is an open source AI coding agent. It's available as a terminal-based interface, desktop app, or IDE extension"
  ([docs index](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/index.mdx)).
- Client/server architecture: "the TUI frontend is just one of the possible clients"
  ([README 2.0 FAQ](https://raw.githubusercontent.com/anomalyco/opencode/2.0/README.md)).
- IDE surfaces are clients of the same engine: VS Code/Cursor extension auto-installs when `opencode` runs in the integrated
  terminal ([ide.mdx](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/ide.mdx));
  ACP via `opencode acp` (stdin/stdout nd-JSON) for Zed/JetBrains/Neovim ([acp.mdx](https://raw.githubusercontent.com/anomalyco/opencode/2.0/packages/web/src/content/docs/acp.mdx));
  `opencode web`/`serve`/`attach` for remote/web clients ([cli.mdx](https://raw.githubusercontent.com/anomalyco/opencode/2.0/packages/web/src/content/docs/cli.mdx)).
- The plugin engine persists in V2; the playbook's 2026-09-25 review of the official docs identifies the plugin API, the
  server/client API, and terminal-client config as the three breaking areas
  ([migration playbook](https://gist.github.com/yohi/0c3d8edea98d86e7f5c8d6c2d9f93d2d), §27 — CANDIDATE until the official
  migrate-v1 page is re-read).
- Both generations use the same `opencode` command; V1 and V2 are **not** installed side-by-side by default (V1 package-managed
  install generally must be removed before V2; keep config backups) ([migrate-v1 snippet](https://opencode.ai/v2/docs/migrate-v1) — CANDIDATE).

## 3. V1 contract (shared entrypoint generation, VERIFIED)

Verified sources: [plugins.mdx](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/plugins.mdx)
(identical SHA `a8be7982` on `dev` and `2.0`), [cli.mdx dev](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/cli.mdx),
[server.mdx](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/server.mdx) (identical on `2.0`),
[permissions.mdx](https://raw.githubusercontent.com/anomalyco/opencode/2.0/packages/web/src/content/docs/permissions.mdx).

- **Plugin entrypoint:** default-export async function receiving a context and returning a hooks object, e.g.
  `export const MyPlugin = async ({ project, client, $, directory, worktree }) => ({ "tool.execute.before": async (input, output) => {…} })`.
  Hooks returned: `tool.execute.before` / `tool.execute.after`, `event`, `shell.env`, `command.*`, `config`, `dispose`,
  `chat.message` / `chat.params` / `chat.headers`, `permission.ask`, `experimental.chat.*`. Blocking demonstrated by throwing
  from the pre-tool hook (`DESIGN.md` §OpenCode reflects this generation).
- **Loading:** `.opencode/plugins/` (project) and `~/.config/opencode/plugins/` (global), auto-loaded at startup; npm plugins
  via config `plugin[]`, installed automatically with Bun at startup, cached in `~/.cache/opencode/node_modules/`
  ([plugins.mdx](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/plugins.mdx)).
- **Events (V1 doc list):** `session.created/compacted/deleted/diff/error/idle/status/updated`, `tool.execute.before/after`,
  `shell.env`, `permission.asked/replied`, `message.*`, `command.executed`, `installation.updated`, `lsp.*`, `todo.updated`,
  `tui.*`, `server.connected` (same source). `DESIGN.md`'s `experimental.session.compacting` and SDK `session.abort` are the
  older spellings; the doc list uses `session.compacted` and the server API uses `POST /session/:id/abort`.
- **CLI (V1, dev cli.mdx):** `tui` (default), `run`, `serve`, `web`, `attach`, `acp`, `auth login/list/logout`, `mcp` (+`auth`,`debug`),
  `models`, `session list/delete`, `stats`, `export`, `import`, `plugin <module> --global`, `pr`, `db`, `debug`, `upgrade [target] --method curl|npm|pnpm|bun|brew`,
  `uninstall --keep-config --keep-data --dry-run`. Global flags: `--help`, `--version`/`-v`, `--print-logs`, `--log-level`,
  **`--pure` (run without external plugins)**. Env: `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, `OPENCODE_CONFIG_CONTENT`,
  `OPENCODE_PERMISSION` (inline JSON permissions), `OPENCODE_SERVER_PASSWORD/USERNAME`, `OPENCODE_DISABLE_AUTOUPDATE`,
  `OPENCODE_DISABLE_DEFAULT_PLUGINS`, `OPENCODE_CLIENT`.
- **Server API (V1, server.mdx):** `opencode serve` headless HTTP server (default `127.0.0.1:4096`), OpenAPI 3.1 at `/doc`.
  Relevant endpoints: `GET /global/health` → `{healthy, version}`; `GET /event` (SSE, first event `server.connected`);
  `POST /session/:id/abort`; `POST /session/:id/permissions/:permissionID` `{response, remember?}`; `POST /session/:id/message`,
  `/prompt_async`, `/shell`, `/command`; `PATCH /config`; `PUT /auth/:id`; `GET /experimental/tool/ids`,
  `GET /experimental/tool?provider=&model=`; `POST /mcp`. `opencode run --format json` emits raw JSON events.
- **Auth/config storage:** `opencode auth login` writes `~/.local/share/opencode/auth.json`; providers sourced from
  Models.dev; `.env` and environment keys also loaded (cli.mdx).
- **Permissions (V1.1.1+; permissions.mdx, same on `2.0`):** `permission` map of `"allow" | "ask" | "deny"`; granular object
  syntax per tool (`bash: {"*": "ask", "git *": "allow"}`), last matching rule wins, `~`/`$HOME` expansion,
  `external_directory` (paths outside the working directory), `doom_loop` (identical tool call repeated 3×), defaults are
  permissive with `read` allowing `.env` denied by default; per-agent overrides in config or Markdown frontmatter
  (`permission:` in `~/.config/opencode/agents/*.md`).

## 4. V2 contract (VERIFIED from pinned v2.0.26 source; runtime fixture still required)

Primary source verified directly at tag `v2.0.26`: [packages/plugin/src/promise/index.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/index.ts)
exports `* as Plugin from "./plugin.js"` plus schema types; [packages/plugin/src/promise/plugin.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/plugin.ts)
defines `Plugin` as `{ id, setup(context): Promise<Cleanup|void>|Cleanup|void }`, `define(plugin)`, and the full `Context`
domain surface (app, location, options, agent, aisdk, command, event, experimental.terminal, integration, mcp, model,
generate, permission, plugin.list, provider, reference, rpc, session, shell, skill, storage, tool, vcs, websearch,
worktree). The official [promise API README](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/README.md)
documents `hook`/`reload`/`dispose` semantics with examples. The third-party playbook
([gist](https://gist.github.com/yohi/0c3d8edea98d86e7f5c8d6c2d9f93d2d)) corroborates the contract but is not the authority; its
own `2.0.10`/`1.18.29` "minimum verified" claims are the playbook's, not this contract's support promise — the pinned source
target is the `v2.0.26` tag, pending runtime fixture. Remaining unverified names (config-mapping strings, event-payload
schema, server path parity, per-client-route coverage) are **CANDIDATE**/[EVIDENCE GAP] as marked.

- **Plugin entrypoint (VERIFIED, v2.0.26 source):** `import { Plugin } from "@opencode/plugin"; export default Plugin.define({ id, setup(ctx) {…} })`.
  `promise/plugin.ts` defines `interface Plugin { id: string; setup: (context: Context) => Promise<Cleanup|void>|Cleanup|void }`
  and `function define(plugin: Plugin)`; the root export is `promise/index.ts` per `packages/plugin/package.json` `.`
  export. `setup` may return a cleanup; registrations can be disposed early via `Registration.dispose` (README). Context
  domains are listed above; `ctx.location` identifies the loading site, not runtime session identity. **[EVIDENCE GAP: bind
  to the installed binary's bundled types before implementation]**.
- **Runtime hooks (VERIFIED names from v2.0.26 source; payload schemas cited):** tool domain
  `ctx.tool.hook("execute.before"/"execute.after", …)` with payloads (`tool, sessionID, agent, messageID, id, input`;
  `execute.after`: `status: "completed"|"error"` plus `result`/`error`)
  ([tool.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/tool.ts)); session
  domain `ctx.session.hook("prompt"|"context"|"compaction"|"generate"|"title"|"model.request"|"http.request"|"http.response"|"experimental.ws.handshake"|"experimental.ws.send"|"experimental.ws.receive"|"retry", …)`,
  `SessionRequestKind = "primary"|"compaction"|"title"|"generate"`, and session API
  `create/get/remove/switchAgent/switchModel/prompt/generate/command/compact/synthetic/interrupt/update/move/wait/context`
  ([session.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/session.ts));
  permission `ctx.permission.hook("evaluate", …)` (`PermissionEvaluation`: sessionID, agent?, action, resources,
  metadata?, source?, effect, message?) plus API `list/get/reply`
  ([permission.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/permission.ts));
  shell `ctx.shell.hook("create.before", …)` with `{command, cwd, timeout, shell, env}`
  ([shell.ts](https://raw.githubusercontent.com/anomalyco/opencode/v2.0.26/packages/plugin/src/promise/shell.ts));
  `ctx.aisdk.hook("sdk"|"language", …)` and `ctx.event.subscribe` (type via `EventApi`) per the README — **event-payload
  schema is CANDIDATE until runtime fixture**. This corroborates the playbook's V1→V2 mapping
  (`tool.execute.before`→`ctx.tool.hook("execute.before")`, `permission.ask`→`ctx.permission.hook("evaluate")`,
  `shell.env`→`ctx.shell.hook("create.before")`, chat/session hooks→`ctx.session.hook`, auth→`ctx.integration.*`), which
  remains corroboration, not authority.
- **Session request kinds (VERIFIED, v2.0.26 source):** `prompt`, `context`, `compaction`, `generate`, `title`,
  `model.request`, `http.request`/`http.response` (plus experimental WS hooks), per `SessionHooks` in `session.ts`; not one
  universal envelope — each kind carries its own typed payload; do not assume `context` covers compaction.
- **Transforms (VERIFIED names, v2.0.26 README/source):** `ctx.<domain>.transform` exists for agent, command, integration,
  mcp, model, provider, reference, skill, tool, vcs, websearch, with matching `.reload()`; editors are synchronous and
  replayable (load async data before registering or reloading). Custom tools via `ctx.tool.transform` add/update/remove
  with `Info{name, description, input/output schema, execute(input, context{signal, progress})}`.
- **Server/client (VERIFIED package, CANDIDATE runtime):** `@opencode/client` 2.0.26 exists as source package with
  `./promise`, `./effect`, `./solid`, `./service` exports; the playbook's `import { OpenCode } from "@opencode/client"`
  usage and "server API is a breaking area" statement are CANDIDATE runtime claims; endpoint path parity must be
  fixture-tested on the pinned V2 binary (server doc file is byte-identical on both branches as of the pin).
- **Configuration (partial VERIFIED, mapping CANDIDATE):** VERIFIED from 2.0 docs: supported V1 config still loads; native V2
  fields may coexist at the top level (native wins); nested mixing stays bounded; modes are deprecated in favor of the
  `agent` option; agents live in `~/.config/opencode/agents/*.md` + `.opencode/agents/`
  ([modes.mdx 2.0](https://raw.githubusercontent.com/anomalyco/opencode/2.0/packages/web/src/content/docs/modes.mdx)). CANDIDATE
  mapping highlights (playbook §17): `permission`+`tools` → ordered `permissions[]`; `bash` → `shell`; `task` → `subagent`;
  `write`/`patch` → `edit`; `agent`/`mode` → `agents`; `prompt` → `system`; `snapshot` → `snapshots`; `mcp` →
  `mcp.servers`; `command` → `commands`; `provider.npm` → `package`; `provider.api` → `settings.baseURL`; preferred layouts
  `.opencode/agents/`, `.opencode/commands/`, `.opencode/skills/`, `.opencode/plugins/`. Fields the playbook lists as
  parse-but-unsupported (gist §18, e.g., `logLevel`, `server`, `compaction.tail_turns`, `experimental.*`,
  `provider.whitelist/blacklist`) are CANDIDATE until confirmed.
- **CLI (2.0-branch cli.mdx, VERIFIED as listed; absence ≠ removal):** the 2.0 page lists `tui`, `run`, `serve`, `web`,
  `attach`, `acp`, `auth`, `mcp`, `models`, `session list/delete`, `stats`, `export`, `import`, `uninstall`, `upgrade`, and
  global `--help`, `--version`/`-v`, `--print-logs`, `--log-level`. **Not listed on the 2.0 page** (presence must be verified
  on the installed binary; absence from docs is not evidence of removal): `plugin`, `pr`, `db`, `debug` commands, global
  `--pure`, and `tui` flags `--auto`, `--mdns`, `--cors`. The shared plugins.mdx still documents `opencode plugin`-style npm
  loading.
- **OpenCode Go (VERIFIED shape, pricing omitted):** [go.mdx](https://raw.githubusercontent.com/anomalyco/opencode/2.0/packages/web/src/content/docs/go.mdx)
  documents a subscription provider for tested open coding models with a console usage tracker and optional balance fallback.
  Pricing/limits are out of scope for this contract. Relevant to bicz metering: subscription/credit reports are **not** a
  per-request billing meter; treat usage as disclosed fixed internal action credits plus bounded inference caps (see plan
  §Claims), never as a cash charge, and never assume rights to proxy the subscription's OAuth.

## 5. Enforcement-relevant surfaces (both generations)

- Loopback HTTP server is core (TUI is a client); `serve`/`web` can bind other hostnames; basic auth via
  `OPENCODE_SERVER_PASSWORD` env — same-user-readable, **not** operator authentication for bicz.
- **Common server architecture:** web UI, ACP clients, and the desktop app all attach to the same local server, so a client
  is **not an inherent plugin bypass**. However, actual hook coverage per client route has no runtime fixture yet: assume
  nothing, fixture-test each route on the installed version. Known bypass concerns are: plugin-disabling switches (`--pure`
  on V1, `OPENCODE_DISABLE_DEFAULT_PLUGINS`); direct `opencode` launches outside the supervised deployment; and any session
  not routed through bicz's executor. Hook-gating alone is never a security boundary (matches `DESIGN.md` "Native hook
  execution is not an OS security boundary").
- **Scope of enforcement:** bicz enforces within its supervised runner identity/deployment and that runner's process
  descendants (no executor/gateway bypass, no egress outside the gateway, no broad-privilege native tools). Processes the
  operator launches outside that identity — including additional unsupervised `opencode` instances — are outside bicz's
  scope and are disclosed as such; bicz does not claim to disable host-wide binaries.
- Config-based gates (`permission`, and experimental `policies`/`provider.use` from the dev-line
  [policies.mdx](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/policies.mdx)) are
  same-user editable and therefore cooperative aids only; the enforcing authority lives in bicz's protected ledger/policy
  and executor/gateway. **[EVIDENCE GAP: `experimental.policies` is documented on `dev` but absent from the `2.0` branch
  docs listing — verify presence/behavior on installed V2.]**
- Metering: `opencode stats` reports token usage/cost from local session data and Models.dev metadata (self-reported, not a
  trusted meter). Trusted usage must come from bicz's own gateway/executor observation.

## 6. Mandatory V1 + V2 matrix (implementation rows)

Release acceptance requires both generations (per user mandate; the plan may record engineering uncertainty, not omit V2).

| Axis | V1 row | V2 row |
| --- | --- | --- |
| Supported version(s) | Pinned tested 1.18.x (`1.18.29` is the playbook's V1 shared-entrypoint floor — VERIFIED as a tag, floor claim CANDIDATE) | Source target `v2.0.26` (pinned tag; plugin/client contracts VERIFIED in repo source; runtime fixture pending) |
| Install/detection | npm `opencode-ai`, curl installer, brew tap/formula, scoop/choco, pacman, mise, nix; `opencode --version` major `1` | curl installer (replaces V1 binary, CANDIDATE), desktop beta, `2.0` branch builds; `opencode --version` major `2`; **[EVIDENCE GAP: exact V2 artifact channel]** |
| Config roots | `~/.config/opencode/opencode.json` + `OPENCODE_CONFIG*`; `.opencode/` | same roots (VERIFIED); legacy V1 fields load; native V2 fields coexist (bounded) |
| Plugin fixtures | shared-entrypoint plugin (VERIFIED); `tool.execute.before/after` deny + observe; event subscription; server API v1 | `Plugin.define({id, setup})`, typed domain hooks (tool/session/permission/shell), `@opencode/client` — names VERIFIED at v2.0.26 (runtime fixture pending) |
| Coverage audit | observed tools; `--pure` bypass; desktop/ACP/web documented on both generations — every surface routed-or-disabled within the supervised deployment (see §5) | tools, sessions, permissions, compaction; ACP/desktop/web paths routed-or-disabled within the supervised deployment; model generation not plugin-metered → gateway |
| Fixture evidence | capture stdin/input/output JSON, allow/deny, replay, subagents, context restore | capture hook payloads, transform replay, session-request kinds, event typing, `/doc` parity |

## 7. Evidence gaps (explicit)

1. `opencode.ai/*` HTML is HTTP 403 to direct fetch; official `/v2/docs/*` pages (migrate-v1, plugins, sdk) were not read
   directly — migrate-v1 content is corroborated by search snippets and the third-party playbook (2026-09-25 review).
   Re-read before implementation.
2. **Remaining CANDIDATE V2 names** (not yet verified): config-mapping strings (playbook §17/§18), event-payload schema
   (`ctx.event.subscribe` type only), server endpoint path parity, and exact per-client-route hook coverage — verify against
   official v2.0.26 source/docs and installed-binary fixtures before adapter implementation.
3. V2 distribution channel: tags `v2.0.x` carry no GitHub release objects; the legacy npm `opencode-ai` channel shows no stable
   2.x (renamed `@opencode/*` registry package availability unverified); the install script is not fetchable — the actual
   artifact/URL for a pinned V2 install must be fixture-verified.
4. V2 CLI feature presence (`--pure`, `plugin` command, `--auto`, `--mdns`) unverified against an installed binary; docs
   omission is not removal evidence.
5. `experimental.policies` (provider.use) presence on V2 unverified (docs split dev vs `2.0`).
6. Desktop app (Electron, beta): config/plugin loading and metering behavior unverified.
7. Exact V2 plugin type exports on the installed binary (bundled `@opencode/plugin` vs npm `@opencode-ai/plugin` `v2/effect/*`)
   unverified.

## 8. Sources

| Source | URL | Status |
| --- | --- | --- |
| Repo metadata / branches / tags / releases | api.github.com/repos/anomalyco/opencode (repo, branches, tags, releases/tags/v2.0.0, /v2.0.26) | VERIFIED 2026-10-10 |
| README (dev, 2.0) | raw.githubusercontent.com/anomalyco/opencode/{dev,2.0}/README.md | VERIFIED 2026-10-10 |
| docs/*.mdx (plugins, cli, server, acp, ide, index, permissions, modes, go, policies, network) | raw.githubusercontent.com/anomalyco/opencode/{dev,2.0}/packages/web/src/content/docs/*.mdx | VERIFIED 2026-10-10 |
| npm opencode-ai latest + dist-tags; @opencode-ai/plugin latest | registry.npmjs.org | VERIFIED 2026-10-10 |
| V2 primary source: packages/plugin/src/{promise/index,plugin,session,tool,permission,shell,event}.ts, README.md; packages/{plugin,client}/package.json | raw.githubusercontent.com/anomalyco/opencode/v2.0.26/… | VERIFIED 2026-10-10 |
| V1→V2 migration playbook (yohi, third party) | gist.github.com/yohi/0c3d8edea98d86e7f5c8d6c2d9f93d2d | CANDIDATE source; 2026-09-25 update |
| Official migrate-from-V1 page (snippet) | opencode.ai/v2/docs/migrate-v1 | CANDIDATE; 2026-10-10 (403; snippet only) |
| Third-party corroboration (v2 rewrite facts) | byteiota.com, turboai.dev, zidooka.com articles | CANDIDATE 2026-10-10 |