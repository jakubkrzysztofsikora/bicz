# Security

## Threat model (read this first)

bicz is an **incentive-and-accountability** tool, **not** an anti-tamper or sandboxing tool. It
runs as your OS user and is cooperative:

- Anything running as that user, including the coding agent, can edit the ledger file, stop the
  supervisor, remove hooks, or set `disableAllHooks`. bicz makes tampering with *recorded history*
  **detectable** (hash chain, optional external anchor, `bicz verify`); it does not prevent it.
- Denial acts only through the adapter/hook path. Hosted tools, subagent and MCP internals, disabled
  hooks and Codex's fail-open on hook error or timeout are **unobserved** paths (`bicz doctor`).
- The "operator" is the local OS user at the CLI, recorded as an **unsigned** record. It is not
  authentication. There is no password, token or signature.
- Internal credits are not provider billing, and bicz does not see or limit provider spend.

Reports that restate these properties as vulnerabilities are expected and are not bugs.

## What is in scope

Please report anything that breaks a property bicz does claim:

- the installers or `bicz install/uninstall` modifying files they do not own, following symlinks,
  writing outside `$HOME`, or losing user settings;
- a hook emitting `permissionDecision: "allow"` (it must only ever deny or add context);
- a settlement charging a different amount than was reserved, charging twice, or releasing a live hold;
- ledger writes that bypass the supervisor's validation, or an integrity break `bicz verify` misses;
- `scripts/install.sh` / `install.ps1` accepting an archive whose checksum does not match, accepting
  an archive with members other than `bicz`/`LICENSE` (traversal, symlink, hard link, nested path),
  executing the downloaded binary, or installing from a non-https, non-loopback source. Known limit:
  only the install directory and target are checked for symlinks, not every ancestor directory;
- the daemon or a hook hanging beyond its documented bound (hooks: 3 s).

## How to report

No security contact is published yet: the project has no verified public repository owner. Once the
repository is published, use that repository's **private vulnerability reporting**
(GitHub Security Advisories) and do not open a public issue. Until then, report privately to the
maintainer who gave you this copy. Include the commit, the OS, and a minimal reproduction.

## Handling

Credentials are never read by bicz. It does not read `~/.claude/.credentials.json`,
`~/.codex/auth.json` or any provider credential, and install/uninstall touch only the
settings files named in the adapter READMEs.

## Release integrity

Release archives ship with `SHA256SUMS` and a GitHub build-provenance attestation produced by the
release workflow. Artifacts are **not** code-signed or notarized, and no SBOM is generated yet. The
installers' checksum check protects against corruption or a swapped archive relative to the
`SHA256SUMS` served from the same origin; it does not authenticate the publisher.
