# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Versions follow SemVer once a tag
exists. No release has been tagged or published yet.

## [Unreleased]

Pre-release. The mechanism is shipped; **its behavioral effect is unmeasured** (Phase B not run).

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

### Known limitations (release blockers for behavioral claims)

- **Phase B** (human, real-consequence, dedicated test identity) has not been run.
- **OpenCode V2**: the adapter typechecks against the published `@opencode/plugin@2.0.26` and loads
  through its real `Host.load` against a real daemon (`docs/RUNTIME_OPENCODE.md`). No V2 engine was
  available (`opencode-ai@2.0.26` is not on npm; the installed CLI is V1), so engine loading, hook
  dispatch, denial propagation and live context mutation are unverified.
- Codex hook field names are **unverified** against a running Codex (`codex-cli 0.162.1` recorded).
- Claude Code 2.1.286 was driven live in headless mode (`docs/RUNTIME_HOOKS.md`); Codex 0.162.1 parses
 the generated `hooks.json` but its hook-trust gate blocked a live run; OMP's `/bicz` command was run in a real `omp --mode rpc` 18.2.11 but its tool-call hooks and OpenCode have no real session.
- Windows binaries cross-compile and `go vet` cleanly; the Windows test suite has not been observed
  passing (CI job is advisory until it is).
- No code signing, notarization or SBOM.
