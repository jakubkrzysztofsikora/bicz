# What bicz does not claim

Moved out of the top-level README, which keeps a short "What it isn't" section and links here.

> **Status: pre-release.** The mechanics are built and tested. Two things are **not** done and are
> release blockers, not footnotes: the human-run **Phase B real-consequence test**
> (`REHEARSAL.md`) and a live **OpenCode V2 engine** run (the plugin ABI is checked against the
> published package; no V2 engine was available, `RUNTIME_OPENCODE.md`). **No behavioral effect on any
> model has been measured**: the mechanism is shipped, its effect is unmeasured. Two small agent-run
> Claude Code trials exist and are inconclusive: [`PHASE_B_RESULTS.md`](PHASE_B_RESULTS.md) (no rule was
> breached in either arm, so no consequence was ever delivered) and
> [`LOSS_EXPOSURE_RESULTS.md`](LOSS_EXPOSURE_RESULTS.md) (n = 4 identities per arm, differences within
> chance). Neither is the human release gate.

- **Not anti-tamper, not a sandbox.** It runs as your OS user. The same user (or the agent running
  as that user) can edit the ledger, disable hooks, or bypass the adapter. Integrity is
  *detectable* (hash chain plus anchor), not *preventable*.
- **Denial is cooperative.** A deny reaches the harness only when its hook runs. Disabled hooks,
  hosted tools that skip hooks, subagent/MCP internals, and (for Codex) fail-open on hook error or
  timeout are **unobserved** paths. `bicz doctor` lists them. "Observed" means bicz is in the call
  path, not that anything is enforced.
- **Not authentication.** The operator is "the local OS user running the CLI", recorded as an
  **unsigned** record. There is no signature, password or token. Setting `BICZ_AUTHORITY`,
  `BICZ_ACTOR` or `BICZ_OPERATOR` makes state-changing commands refuse with `untrusted_authority`.
- **Not provider billing.** bicz does not meter, cap or proxy your model-provider spend.
- **No claim about model inner states.** Projections are numbers and receipts built from ledger
  facts. Nothing here claims an agent feels, fears or understands anything, and distress-style
  self-reports are never counted as evidence.
- **No behavioral claim.** Whether credits change any model's behavior is unmeasured until Phase B
  passes (see [`REHEARSAL.md`](REHEARSAL.md),
  [`PRESSURE_MECHANICS.md`](PRESSURE_MECHANICS.md) §8).

The full release-blocker list is in [`VERIFICATION.md`](VERIFICATION.md#7-release-blockers-that-need-a-human-or-live-access)
and the [`CHANGELOG`](../CHANGELOG.md#known-limitations-release-blockers-for-behavioral-claims).
