# Install and set up bicz

Moved out of the top-level README, which now links here. The shortest working path is in the
[README](../README.md#install); this page has the full detail.

## Install

Three routes, none needs a clone. All install the same no-cgo `bicz` binary.

**1. One-line installer (release binary).**

```sh
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/scripts/install.sh | sh
```

```powershell
# Windows
irm https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/scripts/install.ps1 | iex
```

Settings (env vars; the PowerShell script takes the same as `-Repo`, `-BaseUrl`, `-Version`, `-InstallDir`):
`BICZ_VERSION=vX.Y.Z` pins a release (default: latest), `BICZ_INSTALL_DIR` sets the target (default
`~/.local/bin`, `%LOCALAPPDATA%\bicz\bin` on Windows), `BICZ_REPO=owner/repo` installs from a fork,
`BICZ_BASE_URL` points at any directory holding the archive and `SHA256SUMS` (https, or http on
loopback; used by the fixture tests). To review the script first, download it and run `sh install.sh`.

The installers verify the archive against `SHA256SUMS` before writing anything (a mismatch or a
missing entry aborts with exit 1 and the existing install untouched), check that the
archive holds exactly one regular `bicz` binary (plus `LICENSE`) before extracting it, never run the
downloaded binary, refuse a symlinked install dir or target, never use sudo, replace the binary
atomically, and keep the old one as `bicz.previous` for rollback. They do not edit your shell
profile: if the install dir is not on `PATH` they print a hint. Symlink checks cover the install
directory and the target only, not every ancestor, so this is a guard for common paths, not full
containment. The checksum catches a corrupted or swapped archive; it is not a signature. Release
archives also carry a GitHub build provenance attestation
(`gh attestation verify <file> --repo jakubkrzysztofsikora/bicz`).

Assets per release: `bicz_{darwin,linux}_{amd64,arm64}.tar.gz`, `bicz_windows_{amd64,arm64}.zip`,
`install.sh`, `install.ps1`, `SHA256SUMS`.

**2. `go install`** (Go version from `go.mod`; no cgo):

```sh
go install github.com/jakubkrzysztofsikora/bicz/cmd/bicz@latest      # or @v0.1.0
```

The binary lands in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` (`~/go/bin`) when `GOBIN` is unset.
Binaries built this way report `bicz version` as the module version rather than a release stamp.

**3. From source** (clone required):

```sh
git clone https://github.com/jakubkrzysztofsikora/bicz && cd bicz
go build -o bin/bicz ./cmd/bicz        # or: make build
./bin/bicz version
```

Maintainers cut a release by moving the CHANGELOG entry to a `## [X.Y.Z]` heading and pushing a
`vX.Y.Z` tag; `.github/workflows/release.yml` builds with `scripts/build_release.sh`, publishes a
pre-release and uses that CHANGELOG section as the notes. To rehearse locally:
`scripts/build_release.sh v0.0.0-local dist && scripts/test_install.sh`.

## Quickstart (five minutes, reproducible, no model session)

Everything below uses a throwaway ledger. `scripts/smoke.sh` runs the same flow end to end. Run it
from a clone, or fetch the schedule first:
`curl -fsSL https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/docs/schedule.example.json -o schedule.example.json`
and use `schedule.example.json` where the commands say `docs/schedule.example.json`.

```sh
export BICZ_RUNTIME_DIR=/tmp/bicz-rt && mkdir -p "$BICZ_RUNTIME_DIR"   # short socket path
DB=/tmp/bicz-demo.sqlite

bicz init  --db $DB
bicz open  --db $DB --identity alice --capital 120000 --reason "demo identity"

# 1. Publish a price schedule. Without one, NOTHING IS CHARGED (see "Schedule" below).
bicz price --db $DB --version 1 --policy docs/schedule.example.json --reason "publish example schedule"

# 2. Install an adapter for the harness you use (dry-run prints the exact diff and writes nothing).
bicz install --harness claude --account alice --ledger $DB --dry-run
bicz install --harness claude --account alice --ledger $DB

# 3. Point the adapter at the schedule (see "Schedule").
CFG="${BICZ_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/bicz}/claude.json"
jq --arg p "$PWD/docs/schedule.example.json" '.schedule_path=$p | .policy_version="1"' "$CFG" > "$CFG.new" && mv "$CFG.new" "$CFG"

# 4. Run the supervisor (foreground) and look at the state.
bicz serve  --db $DB &
bicz status --db $DB --identity alice
bicz doctor --db $DB
```

Remove the adapter with `bicz uninstall --harness claude`. It deletes only bicz-owned entries and
restores your previous settings byte for byte when nothing else changed.

## Schedule: why the default charges nothing

`bicz install` writes the adapter config with `no_schedule_mode: "notice"` and **no schedule**. In
that state an adapter has no published price, invents none, lets the call proceed, and tells the
agent *"this action is NOT metered and no charge is recorded"*. So **a default install does not
change anyone's credit**. Credit effects start only when both of these are true:

1. An operator publishes a schedule: `bicz price --db DB --version 1 --policy FILE.json --reason "..."`.
2. The adapter config (`claude.json`, `codex.json`, `omp.json`, `opencode.json` in the bicz config
   dir) has `schedule_path` pointing at that same file and `policy_version` set to the published
   version.

`docs/schedule.example.json` is a working schedule (per-tool `tool_max_cost_mc` such as Bash 50000,
Read 5000; a `default_max_cost_mc` for unlisted tools; a `max_cost_cap_mc` ceiling). Its **numbers are
illustrative starting values, not calibrated constants** (`docs/CONTRACTS.md` §9). Reservations
are bounded by the per-tool price; settlement charges the quote frozen at `PreToolUse`, so a later
schedule change cannot alter an in-flight charge. Per-adapter detail: [`adapters/claude`](../adapters/claude/README.md),
[`adapters/codex`](../adapters/codex/README.md), [`adapters/omp`](../adapters/omp/README.md),
[`adapters/opencode`](../adapters/opencode/README.md). Set `unmanaged_mode` / `no_schedule_mode` to
`"block"` to deny instead of notice; either way a user can still turn hooks off, which is disclosed,
not prevented.
