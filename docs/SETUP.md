# Install and set up bicz

Moved out of the top-level README, which now links here. The shortest working path is in the
[README](../README.md#install); this page has the full detail.

## Install

There is no published release yet, so building from source is the path that works today. Binaries
for macOS, Linux and Windows (amd64, arm64) are built by the release workflow once a tag is pushed.
The release source is **not assumed**: you name the repository that publishes them.

From source (Go version is read from `go.mod`):

```sh
go build -o bin/bicz ./cmd/bicz        # or: make build
./bin/bicz version
```

From a published release (once one exists):

```sh
# macOS / Linux. Replace OWNER/REPO with the repository that actually publishes releases.
curl -fsSL https://github.com/OWNER/REPO/releases/latest/download/install.sh -o install.sh   # review it
BICZ_REPO=OWNER/REPO sh install.sh                 # BICZ_VERSION=vX.Y.Z to pin
```

```powershell
# Windows
.\install.ps1 -Repo OWNER/REPO                     # -Version vX.Y.Z to pin
```

The installers verify the archive against `SHA256SUMS` before writing anything, check that the
archive holds exactly one regular `bicz` binary (plus `LICENSE`) before extracting it, never run the
downloaded binary, refuse a symlinked install dir or target, never use sudo, install to
`~/.local/bin` (`%LOCALAPPDATA%\bicz\bin` on Windows), replace the binary atomically, and keep the
old one as `bicz.previous` for rollback. Symlink checks cover the install directory and the target
only, not every ancestor, so this is a guard for common paths, not full containment. The checksum catches a
corrupted or swapped archive; it is not a signature. Release archives also carry a GitHub build
provenance attestation (`gh attestation verify <file> --repo OWNER/REPO`).

`go install github.com/jakubkrzysztofsikora/bicz/cmd/bicz@<tag>` works only once the repository is
published at exactly that module path. The repository is public at
`https://github.com/jakubkrzysztofsikora/bicz`, matching `go.mod`; no tag has been cut yet.

## Quickstart (five minutes, reproducible, no model session)

Everything below uses a throwaway ledger. `scripts/smoke.sh` runs the same flow end to end.

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
