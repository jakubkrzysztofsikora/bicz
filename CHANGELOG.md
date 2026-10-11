# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Versions follow SemVer.

## [Unreleased]

### Added

- Codex runtime verification (`docs/RUNTIME_HOOKS.md` section 3): real `codex-cli 0.162.1` `codex exec`
  runs, hooks trusted via Codex's own `hooks.state` mechanism (no bypass flag), temp home/ledger, local mock
  model server. `$bicz status`, `reward` and a no-correction `loss` were answered by the hook with 0 model
  requests; a tool call was reserved and settled; a bicz deny stopped the tool; a hook error and a hook
  timeout failed open. Every field name in `CodexSpec.Fields` matched the real payloads, so the table is
  unchanged. `TestCodexRealPayloadsReserveAndSettle` replays captured payloads.
- To trust the hooks on your machine: run `codex`, type `/hooks`, trust the bicz entries.
- Docs correction: Codex `PostToolUse` also fires for a shell command that exits non-zero (no exit code in
  the payload, settled as `completed`); the earlier claim that it fires only after success was wrong.
  Codex's shell tool reaches hooks as `Bash`, with `tool_response` a bare string.
- 15 default rules distilled from real agent failures (derived from one operator's 670 sessions and 113
  frustration moments across four harnesses), embedded as `internal/policy/defaults.json`: rule id,
  correction, scope, exceptions, burn tier (`low`/`medium`/`high`, all existing ledger penalty
  parameters) and an on/off default. See [docs/DEFAULT_POLICIES.md](docs/DEFAULT_POLICIES.md).
- `/bicz rules` and `bicz rules` list the default rules (id, tier, on/off, statement).
- `/bicz loss RULE "reason"` for a default rule now works with no correction typed: the correction
  resolves explicit, then saved lesson, then shipped default (`source=default`, with the catalog scope
  and exceptions stored on the lesson), then prompt, then `needs_input`. Any other rule id behaves as before.
- `docs/schedule.example.json` publishes a `penalties` entry for each default-on rule and sets
  `guard_band_bp` to 2000, so repeat multipliers stop rising below 20% of initial capital. A test pins
  the schedule to the catalog.
- A rule's suggested disposition (`integrity_breach` for three rules) is advisory: `/bicz loss`
  still records `confirmed_breach`.

## [0.1.1] - 2026-10-11

First published release, as a GitHub **pre-release**. The mechanism is shipped; **its behavioral
effect is unmeasured** (Phase B not run). `v0.1.0` was withdrawn before any release was built: its
tree was missing `internal/protocol/coverage.go` (swallowed by a `coverage.*` ignore rule, now
`/coverage.*`), so it did not compile from a clean checkout.

### Install without cloning

- One-line installer: `curl -fsSL https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/scripts/install.sh | sh`
  (PowerShell: `irm https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/scripts/install.ps1 | iex`).
  It now defaults to this repository's releases (override with `BICZ_REPO` / `BICZ_BASE_URL`), verifies
  SHA256 and fails loudly on a mismatch.
- Release archives (`bicz_<os>_<arch>.tar.gz`, `.zip` on Windows; darwin, linux, windows x amd64, arm64)
  plus `SHA256SUMS`, built by the tag-triggered workflow; release notes come from this file.
- `go install github.com/jakubkrzysztofsikora/bicz/cmd/bicz@v0.1.1`.

### Added

- Operator slash command `/bicz` (`bicz command --harness H --arguments RAW`): `loss RULE "reason"
  ["correction"]`, `reward AMOUNT "reason"` (decimal credits, up to 6 decimals), `status`. Identity and
  ledger come from the harness's installed config. A loss without a known correction writes nothing
  and asks (exit 3). Installed for Claude Code (`UserPromptExpansion` hook, typed text as JSON, no model,
  no shell) and Codex (`$bicz`, `UserPromptSubmit` hook; prompt-mediated skill fallback while the hook is
  untrusted) by `bicz install`; unsigned same-user provenance, not authentication.
- Ledger (SQLite, hash chain, anchors), supervisor (`bicz serve`) and operator CLI.
- Claude Code and Codex hook adapters: `bicz hook --harness claude|codex --event NAME`. Deny emits
  `permissionDecision: "deny"` only and never `"allow"`. `PostToolUse` settles the quote frozen at
  `PreToolUse`, so a schedule change in between cannot change the charge. Hard 3 s hook timeout;
  `unmanaged_mode` `notice` (default) or `block`.
- Installers for `claude`, `codex`, `omp`, `opencode` (V1/V2): dry-run diff, atomic write,
  timestamped backup, idempotent, uninstall removes only bicz entries, refuses symlinks / foreign or
  malformed files / paths outside `$HOME`.
- `bicz version [--json]`, stamped by the release build.
- Phase A rehearsal (`bicz rehearsal`) and `bicz release-gate`, which rejects a missing Phase B record.
- `docs/schedule.example.json`: a publishable, adapter-usable price schedule (illustrative values).
- `scripts/install.sh` / `install.ps1`: checksum-verifying, user-scope, atomic installers that
  validate the archive member list before extracting and never execute the download, with local
  HTTP fixture tests (including hostile archives); `scripts/build_release.sh`; `scripts/smoke.sh` and a TS mock-harness driver.
- CI (`go.mod`-driven Go version, race tests, cross-build, script tests) and a tag-triggered release
  workflow (six platform archives, `SHA256SUMS`, build provenance, pre-release).
- MIT license, `SECURITY.md`, `docs/VERIFICATION.md`.

- Automatic accountability cue for confirmed penalties: `bicz loss --disposition
  confirmed_breach|integrity_breach|administrative` (CLI default `confirmed_breach`; the daemon/API
  has no default), `disposition`/`classification_version` on new `loss` payloads, `penalties`,
  `penalty_total`, `penalty_seq` on the projection, delivery through the Claude/Codex hooks (SessionStart,
  PreCompact, per-tool poll), the OMP extension, OpenCode V1 and V2. Existing events are not rewritten;
  losses without a classification read as unclassified and earn no cue. See docs/CONTRACTS.md §6.4.

### Changed

- `ledger.RecordLoss` and `daemon.LossRequest` now require an explicit disposition (clean cutover:
  callers migrated). A self-disclosed breach adjudicated by the operator is cued like any confirmed breach.
- CI previously pinned Go 1.22 against a Go 1.27 module; it now uses `go-version-file: go.mod`.
- OMP adapter `package.json` license corrected from `UNLICENSED` to `MIT`.
- **OpenCode V1 plugin** fixes found only by running the real 1.18.31 engine (the TS mock harness could
  not see them): daemon calls failed under the engine's embedded Bun 1.3.14 because the socket was
  half-closed before the reply was read (the reserve was recorded, the plugin saw an empty reply, allowed
  the call unmetered and never settled); notices, receipts, projection and penalty cues were written to a
  field V1 never reads and now go through `experimental.chat.system.transform`; a failed tool is now
  settled as an ordinary failure via `message.part.updated` (V1 never calls `tool.execute.after` then);
  `/bicz` replaced `output.parts` by assignment, which V1 ignores, so it now mutates the array. The
  V1 mock-harness checks were rewritten to the observed hook shapes.

### Known limitations (release blockers for behavioral claims)

- **Phase B** (human, real-consequence, dedicated test identity) has not been run.
- **OpenCode V2** and **V1** were run in real engines (`opencode v2.0.26` from `@opencode/cli@2.0.26`;
  `opencode` 1.18.31) on temp HOME/ledgers with a local mock OpenAI-compatible provider
  (`docs/RUNTIME_OPENCODE.md`). V2: plugin load, `/bicz` with no model turn and no provider request,
  reserve/settle of a real tool call, insufficient-credit deny as a tool error, context projection and
  receipt, compaction cue. V1: reserve/settle, failed-tool settlement, deny, receipt/cue delivery,
  `/bicz` write before the relaying model turn. The mock provider means no model behavior was
  measured; desktop/web/ACP/TUI routes, `--pure`, and V1 compaction were not exercised.
- Codex hooks were run live in `codex-cli 0.162.1` (`codex exec`, mock model server, temp home): field
  names verified, nothing left unverified except the interactive TUI, `apply_patch`/MCP tool names and
  compaction (`docs/RUNTIME_HOOKS.md` section 3).
- Claude Code 2.1.286 was driven live in headless mode (`docs/RUNTIME_HOOKS.md`); OMP's `/bicz` command was run in a real `omp --mode rpc` 18.2.11 but its tool-call hooks have no real session.
- Windows binaries cross-compile and `go vet` cleanly; the Windows test suite has not been observed
  passing (CI job is advisory until it is).
- No code signing, notarization or SBOM.
