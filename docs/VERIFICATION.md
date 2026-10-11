# Verification record

What was run, on which machine, with what result, and what it does **not** prove. Reproduce every
item with the command shown. Host: macOS 26 (darwin/arm64), Go 1.27.2, Node 26.5.0, PowerShell
7.5.2, ShellCheck 0.11.0. Nothing here ran a model session, read a credential file, touched a real
harness config (`~/.claude`, `~/.codex`, `~/.config/opencode`, `~/.omp`), committed, pushed or
published.

## 1. Build, vet, tests

| Command | Result |
| --- | --- |
| `gofmt -l .` | no output |
| `go build ./...` | ok |
| `go vet ./...` | exit 0 |
| `go test -count=1 ./...` | 9 packages ok (adapter, canonical, daemon, hooks, install, integrity, ledger, protocol, rehearsal; `cmd/bicz` and `schema` have no test files) |
| `go test -race -count=1 ./...` | all packages ok (daemon 29.7 s, hooks 36.2 s, ledger 66.7 s, rehearsal 120.9 s) |
| `go test -count=1 -v ./... \| grep -c -- '^--- PASS'` | 195 top-level tests pass, 0 fail |

Cross-compile (`CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -trimpath ./cmd/bicz`): darwin/amd64,
darwin/arm64, linux/amd64, linux/arm64, windows/amd64, windows/arm64 all build.
`GOOS=windows GOARCH=amd64 go vet ./...` is clean. **The test suite has not been run on Windows.**

## 2. Phase A rehearsal and the release gate

```
$ bicz rehearsal
REHEARSAL simulation_only=true protocol=bicz.rehearsal/1 passed=10/10 failed=0
$ bicz release-gate --results <that report>
RELEASE-GATE pass=false
  OK Phase A passed 100% (10/10 scenarios, simulation_only)
  FAIL Phase B evidence: read .../phase-b-evidence.json: ... no such file or directory
bicz: release-gate: NOT passed; 1 requirement(s) unmet (a real human must record Phase B evidence)
exit 1
```

Run with `BICZ_CONFIG_DIR`, `HOME` and `BICZ_RUNTIME_DIR` pointed at a temp directory. Phase A is
**simulation only**. The gate correctly rejects a missing Phase B record, and **Phase B has not
been run** (`docs/REHEARSAL.md`: human, dedicated test identity, live model session). That is a
release blocker for any behavioral claim.

## 3. Adapter surfaces (`scripts/smoke.sh`)

`scripts/smoke.sh [bicz]` starts a real `bicz serve` on a temp ledger and socket under a temp `HOME`.
Result with the binary taken out of the release archive: **23 passed, 0 failed**.

Hook CLI (Claude Code and Codex), real daemon, schedule from `docs/schedule.example.json`:

| Check | Observed |
| --- | --- |
| `bicz price --version 1 --policy docs/schedule.example.json` publishes | ok |
| `install --dry-run` prints the diff, writes nothing; real install merges and keeps unrelated hook and `model` key; second run is a no-op | ok |
| `SessionStart` | `additionalContext` carries the projection ("identity alice") |
| `PreToolUse` Bash allow | stdout empty (never `permissionDecision: allow`); ledger `balance 120000, held 50000` |
| `PostToolUse` | `bicz receipt: completed, charged 50000 mc, fine 0`; ledger `70000 / 0` |
| duplicate `PostToolUse` (same `tool_use_id`) | `... (replayed settlement: no second charge)`; ledger unchanged |
| insufficient credit (`WebFetch` 100000 vs 70000) | `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"bicz: insufficient_credit — ... cannot reserve 100000 microcredits (spendable=70000)"}}`; nothing reserved |
| Codex `PreToolUse` / `PostToolUse` (Read 5000 mc) | allow emits no decision; settles 5000 mc |
| schedule republished between Pre and Post (Bash 50000 to 9000, policy 1 to 2) | settlement charges the frozen 50000 mc |
| daemon down, `unmanaged_mode: notice` | allow plus "this action is NOT metered, no charge recorded" |
| daemon down, `unmanaged_mode: block` | deny `daemon_unavailable`; both hook calls return well inside the 3 s bound |
| `bicz verify` | `INTEGRITY OK` |
| `uninstall` | claude `settings.json` restored byte for byte; codex `hooks.json` removed |

Probed by hand: a `bicz` path containing a space installs as `/tmp/e\ z/bicz hook ...` and runs
through `sh -c`; empty or garbage stdin exits 1 with a message and no output; `tool_input: null`
and non-UTF-8 names do not crash; an unknown `--event` exits 1.

TypeScript adapters, **mock harness plus real daemon** (`scripts/smoke_ts_harness.mjs`, imports
`adapters/omp/extension.ts` and `adapters/opencode/v1/bicz.ts` with Node type stripping): 7
checks pass (OMP reserve, settle, duplicate result charges once, block on insufficient credit;
OpenCode V1 reserve, settle, deny throws). The harness objects are fakes written from the adapters'
own call shapes.

**Runtime follow-up ([`docs/RUNTIME_HOOKS.md`](RUNTIME_HOOKS.md)):** a real `claude 2.1.286` headless
session did run the installer-generated hooks against a disposable ledger (allow, receipt, deny with
the tool not executing, failure settlement, daemon-down notice/block, and fail-open on a broken or
slow hook). The real `codex-cli 0.162.1` was also run: `codex exec` with the hooks trusted through
its own `hooks.state` mechanism and a local mock model ran all four bicz hooks, answered `$bicz` with
0 model requests, reserved and settled a tool call, honored a deny and failed open on a hook error and
timeout (`docs/RUNTIME_HOOKS.md` section 3).

**Still not verified:** the interactive Codex TUI, Codex `apply_patch`/MCP tool names and compaction;
Claude `PreCompact` and mutating tools; OMP tool-call hooks in a real OMP agent session (only the `/bicz` command was run in real `omp --mode rpc`) and any real OpenCode process; latency under real
harness load.

**OpenCode V2 ([`docs/RUNTIME_OPENCODE.md`](RUNTIME_OPENCODE.md)):** `adapters/opencode/v2/bicz.ts`
typechecks against the real `@opencode/plugin@2.0.26` declarations, and
`adapters/opencode/v2/abi-smoke.mjs` loads it with the package's real `Host.load` and `Plugin.define`
against a real temp `bicz serve`: allow/receipt, insufficient-credit throw, duplicate settle once,
same call id independent per session, `system` projection/receipt, compaction mutation, settlement
retry after daemon recovery, daemon-down notice/block. The embedded copy is byte-identical to the
source. **Engine run (`opencode v2.0.26` from `@opencode/cli@2.0.26`, temp HOME/ledger):** the engine loads
the plugin, lists `bicz`, and `/bicz status`, `reward 3 "x"` and a no-correction `loss` ran through the
engine's `POST /api/session/:id/command` route with matching ledger events (1 -> 2 events, only the
reward wrote), a synthetic inbox item, no message, zero tokens and no provider request (capture proxy saw
only the `models.opencode.ai` catalog fetch). **Not run in the engine:** `execute.before/after`,
`context`, `compaction`; they need a model turn, so denial propagation and live context mutation are
shown only by the callback-registry fixture. The `scripts/smoke.sh` 23-check pass predates this V2 change
(V1 + OMP scope); it was not re-run or recertified.

## 4. Installers and release build

| Command | Result |
| --- | --- |
| `shellcheck -s sh scripts/*.sh` | clean |
| `scripts/test_install.sh` | 21 passed, 0 failed (local loopback HTTP fixtures) |
| `pwsh scripts/test_install.ps1` | 14 passed, 0 failed (PowerShell 7 on macOS) |
| `scripts/build_release.sh v0.0.0-local /tmp/bicz-dist` | 6 archives + `install.sh` + `install.ps1` + `SHA256SUMS`; `shasum -c` ok; extracted binary reports `bicz v0.0.0-local (darwin/arm64, go1.27.2)` |
| workflow YAML | parses (Ruby `YAML.load_file`); `actionlint` is not installed, so semantics are unchecked, and **neither workflow has run on GitHub** |

Installer tests cover: verified install, idempotent re-run with `bicz.previous` rollback copy,
**checksum mismatch rejected with the old binary untouched**, tampered archive, missing checksum
entry, no source configured, non-https non-loopback source, malformed repo slug, symlinked target,
symlinked install dir, 404, and **hostile archives with an honest checksum** (`../` member,
absolute path, nested path, symlink member, hard-link member, extra member, duplicate `bicz`; zip:
traversal, backslash traversal, nested, extra, symlink, duplicate). Member lists are validated
**before** anything is extracted, only the one validated member is extracted, and the downloaded
binary is never executed by the installer (a fixture whose `bicz` writes a marker file proves it).

Limits: this guards common malicious-archive paths, not a containment boundary. Only the install
directory and the final target are checked for being symlinks; **a symlinked ancestor higher up is
not detected**. The PowerShell test ran on macOS: it proves download, checksum, validation and
atomic replace logic, not execution of `bicz.exe` or Windows path semantics. All downloads in tests
come from a local loopback server; **no real remote install was performed** and no GitHub repository
exists to install from.

## 5. Size and startup (this machine only)

Stripped `darwin/arm64` binary 8.6 MB, archive 3.6 MB (other targets 3.6 to 3.9 MB archives).
`bicz version` cold start about 0.15 s wall. These are single observations, not a calibrated budget.

## 6. Independent review

A read-only reviewer worker was started and returned no usable result (its transcript described
files and Go version that do not exist in this repository), so it was discarded. Review was done by
hand against the code with reproduction before fixing. Bugs reproduced and fixed in this pass:

- `scripts/build_release.sh` stamped `commit HEAD` into the binary when the repository has no
  commits (`git rev-parse HEAD` prints `HEAD` on failure). Now uses `rev-parse --verify --quiet`.
- `install.sh` / `install.ps1` extracted every archive member and ran the installed binary; now
  validated first and never executed (section 4).

## 7. Release blockers that need a human or live access

1. **Phase B** real-consequence run on a dedicated test identity (`docs/REHEARSAL.md`).
2. **OpenCode V2 live engine** run on a pinned V2 binary (the ABI fixture is done; the engine is not).
3. ~~Public repository owner/slug~~: resolved, `jakubkrzysztofsikora/bicz` is public and matches `go.mod`;
   the workflows use `github.repository` and the installers take `BICZ_REPO`, so nothing is hard-coded.
4. First green runs of `ci.yml` and `release.yml` on GitHub, including `windows-latest`.
5. Real Claude Code / Codex / Oh My Pi / OpenCode sessions (live credentials) to confirm hook field
   names and ordering. No signing/notarization or SBOM.
