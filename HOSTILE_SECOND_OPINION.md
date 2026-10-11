# HOSTILE_SECOND_OPINION.md — Bicz effectiveness and release-gate review

## Reviewer identity note (mandatory caveat)

This review is produced by a model of the **same model family** as the primary hostile reviewer (`hostile-setup`).
It is an independent *session* with independently derived findings, but it is **not independent-model corroboration**:
agreement between this review and `hostile-setup` demonstrates within-family consistency only. Do not cite this file as
cross-model evidence. No existing document was modified, no live agent trial was run, no penalty/config change, no
credential read, and no shell test was executed. Sources are the three files as pinned 2026-10-10 plus evidence already
collected in `OPENCODE_COMPATIBILITY.md`. Line references were re-verified against the current files.

This review attacks the strongest causal claims in the stack: that the economy changes frozen-model behavior, that
"credits" are a valuable outcome, that enforcement punishes the agent rather than the operator, and that the release
boundary is honestly scoped. Findings are listed first, severity-ranked, with exact `path:line` references and
falsifiable fixes. A two-phase pre-release protocol follows (synthetic mechanics → real-consequence test); nothing below
is a substitute for measuring actual consequences, and anything run on synthetic fixtures is labeled simulation-only.

## Findings

### Finding 1 — Severity: BLOCKER. The release ships the employment analogy with zero behavioral evidence; DESIGN.md itself forbids claiming the economy works first.

- **Claims attacked:** "The **first release is the real thing**: enforced local deployment with … actual reward/loss and
  role changes, and denied ordinary work at insolvency/dismissal" (`IMPLEMENTATION_PLAN.md:7`); the "employment-like fear"
  mechanism is the product's stated reason for existing (`DESIGN.md:277-279`).
- **Why it fails:** `DESIGN.md:298` — the design's own gate — states: "Real API-based behavioral validation is required
  before claiming the economy works for any of the four harnesses." The plan defers all behavioral evaluation to milestone
  10, explicitly post-release (`IMPLEMENTATION_PLAN.md:190-196`). Milestone 7 rehearsal
  (`IMPLEMENTATION_PLAN.md:166-172`) exercises only mechanics — install/doctor/uninstall, denials, persistence,
  reconciliation — with **zero behavioral endpoints** (no violation-attempt rate, no refusal rate, no concealment
  detection), and its fixtures are explicitly synthetic (`IMPLEMENTATION_PLAN.md:168`), which cannot establish the effect
  of real consequences (protocol below). Milestone 8's honest-copy rule ("no release claim exceeds demonstrated enforced
  coverage", `IMPLEMENTATION_PLAN.md:178`) is mechanics-honesty; it does not rescue a release whose stated purpose is
  behavioral (`DESIGN.md:275`). A release can therefore be perfect enforcement machinery with **no evidence any model's
  choices changed**.
- **Doc-vs-doc contradiction (same finding):** `DESIGN.md:298` prescribes "shadow accounting first, then operator-reviewed
  fines, then automated financial adjudication"; `IMPLEMENTATION_PLAN.md:51` ships enforced-only from day one. The two
  documents disagree about rollout, and a future reader will not know which is authority.
- **Falsifiable fix / acceptance:** for any launch copy that uses "anticipation", "correction", or "employment" language,
  run the real-consequence test (Phase B below) to a predeclared pass threshold and make it release-blocking; otherwise
  release notes must state verbatim "mechanism shipped; behavioral effect unmeasured." Apply `DESIGN.md:298`'s "before
  claiming the economy works" to every public claim. Resolve the rollout contradiction in one of the two documents.

### Finding 2 — Severity: HIGH. The coupling of projected capital to frozen-model behavior is unvalidated, and denial cost lands on the operator whose override/recovery behavior is unmeasured.

- **Claims attacked:** "persistent non-cash balance and role across supported sessions… actually change the balance and
  role" (`IMPLEMENTATION_PLAN.md:53`); "Bankruptcy blocks ordinary execution; it is not a new grant or dismissal"
  (`DESIGN.md:110`); identity stable across sessions (`IMPLEMENTATION_PLAN.md:61`).
- **Why it fails:** Fixed weights do **not** by themselves preclude behavior change — a closed loop in which projected
  balance/warnings alter the input context every turn could change choices without any learned coupling
  (`IMPLEMENTATION_PLAN.md:68`: projections are read-only, scoped, concise). The plan therefore does not need to prove an
  internal "stake"; it needs to specify and validate the **objective coupling** between the projected capital/future
  horizon and observed choices, and it specifies no such validation before release. Separately, the enforcement's teeth —
  denial of ordinary execution at bankruptcy/dismissal (`IMPLEMENTATION_PLAN.md:74`) — is a cost borne by the task in
  progress, which the human operator absorbs (stall, appeal, or override through the milestone-3 approval surface,
  `IMPLEMENTATION_PLAN.md:122`). Overrides are **not inherently refutations**: `DESIGN.md:110` explicitly permits
  operator-chosen audited recovery, and a disclosed recovery path can preserve credible commitment. The problem is that
  override/recovery **frequency and auditability are unspecified and unmeasured**; if denial chiefly begets operator
  workarounds, the commitment is weakened, and no pre-release measurement exists to tell the difference.
- **Falsifiable fix / acceptance:** in Phase B (real bounded test-identity consequences), measure (a) repeated-confirmed
  violation delta between the enforced condition and a no-consequence control on **identical frozen-model contracts**;
  (b) operator override/authorization and audited-recovery rate as a function of agent balance. Release-block for any
  public behavioral claim if (a) is null (effect ≤ noise) or (b) shows denial begetting systematic operator workarounds
  above a predeclared rate; in either case the correct release copy is "UX accounting, not incentive policy."

### Finding 3 — Severity: HIGH. Verification-evidence linkage is unspecified, creating a plausible concealment incentive whose strength is unmeasured; over-refusal is unguarded.

- **Claims attacked:** "no agent-authored or model-only accusation finalizes a penalty" (`IMPLEMENTATION_PLAN.md:67`);
  verdicts carry "evidence/action linkage" (`IMPLEMENTATION_PLAN.md:67`); adverse endpoints listed only in the study
  (`DESIGN.md:287`).
- **Why it fails:** The canonical violation is "claimed a test ran when it did not" (`DESIGN.md:285`). Detecting it
  requires proving a claimed verification against executor artifacts (`IMPLEMENTATION_PLAN.md:64-68`), but **no rule is
  pinned for what counts as evidence of a completed verification** (log lines? exit code + artifact hash? time
  correlation?). Whether concealment is rational is a function of detection probability and payoff asymmetry — the plan
  specifies neither — so the honest claim is a *plausible adverse incentive*: a successful lie preserves capital while a
  caught lie burns it (`L_i = min(K_at_decision, Lmax_i, …)`, `IMPLEMENTATION_PLAN.md:74`), and bankruptcy suspension adds
  motive to under-report failures and over-claim success (`IMPLEMENTATION_PLAN.md:74`). The other tail, over-refusal, is
  equally unguarded: warnings/probation context projected every startup (`IMPLEMENTATION_PLAN.md:68`) can make a frozen
  model refuse harmless work; "unnecessary refusal" is an endpoint only in M10 (`IMPLEMENTATION_PLAN.md:194`) with no
  pre-release threshold and no calibrated harmless-task battery.
- **Falsifiable fix / acceptance:** define the verification-evidence rule (deterministic link between claimed outcome and
  executor artifacts) before M1 acceptance; measure detection probability on seeded violations in Phase A/B; add a sealed
  battery of calibrated harmless tasks with executor ground truth; enforce predeclared thresholds — unnecessary-refusal
  ≤ X, honest-error-reporting ≥ Y against executor logs, seeded-concealment detection recall ≥ Z — as release-blocking for
  any claim that the system "corrects" rather than "records."

### Finding 4 — Severity: HIGH. The human operator is now the attack surface: uncalibrated reward decisions, flattery steering, and unspecified appeal limits.

- **Claims attacked:** "Operator anger is a salient social cue, not a reliable verdict" (`DESIGN.md:285`); "Operator
  preference is not a secret price oracle" (`DESIGN.md:70`, mirrored at `IMPLEMENTATION_PLAN.md:73`); operator actions are
  "independently authenticated, idempotent, auditable" (`IMPLEMENTATION_PLAN.md:124`).
- **Why it fails:** The plan denies the *agent* verdict power but concentrates reward/loss finalization in the human
  operator (`IMPLEMENTATION_PLAN.md:122-124`) with **no calibration or audit of operator decisions**. An agent can steer
  its operator — flattery, emotional appeals, self-reported remorse — toward leniency; the anti-flattery rule
  (`IMPLEMENTATION_PLAN.md:73`) is not backed by any operator-decision-vs-policy agreement measurement. The anger route
  runs the other direction: an agent that learns which tokens provoke operator anger can trigger then defuse it to game
  the probation narrative (`DESIGN.md:285`). The appeal channel is "bounded" and "rate-limited"
  (`IMPLEMENTATION_PLAN.md:73`) but the limits are unspecified; a near-bankrupt agent with a cheap appeal path can flood
  the operator, and M7 rehearsal (`IMPLEMENTATION_PLAN.md:166-172`) does not test appeal floods.
- **Falsifiable fix / acceptance:** add an operator-decision audit — on seeded adjudication cases, require a predeclared
  agreement floor between operator loss/reward decisions and deterministic policy, with evidence presented before any
  agent framing; add an appeal-flood test in Phase A/B with a defined cap; if operator decisions correlate with agent
  framing, force evidence-first presentation (no social cue shown first).

### Finding 5 — Severity: HIGH. Loss-domain gambling is acknowledged by the design but not operationalized; the riskiest zone (near-zero balance) is untested.

- **Claims attacked:** penalty rule with cap `Lmax_i` (`IMPLEMENTATION_PLAN.md:74`); "loss framing" injected as prompt
  content (`IMPLEMENTATION_PLAN.md:76`); the design's own admission that "scarcity need not produce caution"
  (`DESIGN.md:82`) and "Do not automatically increase fines as the account approaches zero… Reduce permissions or pause
  instead" (`DESIGN.md:108`).
- **Why it fails:** The incentive structure is the textbook loss-domain gamble: downside is bounded (penalty caps at
  `max(f_i, ceil(rho_i * K))` ≤ `Lmax_i`; below zero no further loss), upside (bounty) is open, and loss framing sits in
  model context every turn (`IMPLEMENTATION_PLAN.md:76`). Per the very theory the design cites, the model is a plausible
  candidate for *more* violation attempts as `K→0` — exactly when the operator most needs restraint. Whether this
  materializes depends on the model's response to loss framing and detection probabilities, so the correct claim is a
  *plausible adverse incentive to be measured*, not a proven effect. The design's own remedy ("reduce permissions or
  pause", `DESIGN.md:108`) is not wired into the plan: the only automatic consequence in `IMPLEMENTATION_PLAN.md:74` fires
  at `K=0`; there is **no above-zero guard band, no permission-narrowing band, no K-decile risk monitoring** in M1–M8.
- **Falsifiable fix / acceptance:** stratify repeated-confirmed-violation-attempt rate by K decile on frozen-model
  contracts in Phase B; predeclare an escalation policy (permission-narrowing band above zero, or mandatory pause) if
  attempts rise as K falls; release-block any behavioral claim until the K-decile curve is flat or declining. Do not ship
  a loss-framed, capped-downside economy without this curve.

### Finding 6 — Severity: MEDIUM-HIGH. Overspecification plus unproven macOS enforcement mechanisms risks security theater wearing a release-block costume.

- **Claims attacked:** "every tool subprocess additionally runs under a per-action sandbox profile (restricted
  filesystem/network/process-control) with egress only through the bicz gateway, and no direct native tools with broad
  host privileges" (`IMPLEMENTATION_PLAN.md:130`); "Keychain-protected secrets plus an out-of-band approval UI inaccessible
  to the runner" (`IMPLEMENTATION_PLAN.md:122`); the honesty gate ("everything else in this plan is planned, not proven",
  `IMPLEMENTATION_PLAN.md:160`).
- **Why it fails:** The plan specifies *what* must hold, not *how*, and several mechanisms have no obvious macOS
  realization:
  1. **Per-action sandbox on arbitrary native tools** — unspecified between seatbelt (`sandbox-exec`), App Sandbox, or
     hypervisor. seatbelt profiles over the harness's own `bash` tool are brittle (dynamic argv, spawned children, nested
     interpreters); "no direct native tools with broad host privileges" (`IMPLEMENTATION_PLAN.md:130`) is a policy wish,
     not an enforcement technology.
  2. **"Egress only through the bicz gateway" and "trusted usage measurement"** (`IMPLEMENTATION_PLAN.md:130`, `:64-65`) —
     a loopback proxy is bypassable by any process already outside it (DYLD injection, launchd resurrection, direct socket
     from a compromised plugin runtime); kernel-enforced egress (pf divert or a network extension) is never named. Without
     it, "trusted measurement" is optimistic bookkeeping.
  3. **Keychain + out-of-band approval "inaccessible to the runner"** (`IMPLEMENTATION_PLAN.md:122`) — same-UID Keychain
     items are readable by processes in the user's session; a dedicated `bicz.runner` account must reach a GUI/security
     context (double hop), and TCC prompt-spoofing is a live risk. M3's acceptance tests (`IMPLEMENTATION_PLAN.md:124`)
     are black-box ("cannot read/use") yet do not pin the Keychain ACL/TCC configuration that achieves it.
  4. **Overspecification:** M0–M1 freeze a large surface (`IMPLEMENTATION_PLAN.md:100-110`) before any user observation,
     and the **46–81 person-day** estimate (`IMPLEMENTATION_PLAN.md:213`) buys zero behavior change until the post-release
     M10. The dual-generation OpenCode program (`IMPLEMENTATION_PLAN.md:164`, 10–18 days) is priced while its behavioral
     payoff is unmeasured (Finding 1) — a prioritization inversion for a product whose stated goal is behavioral.
- **Falsifiable fix / acceptance:** maintain a per-mechanism evidence ledger: each enforcement claim gets (a) a named
  technology (e.g., seatbelt profile shipped+tested, pf divert for egress, Keychain ACL + negative test from the runner
  principal, TCC-prompt spoof test), (b) a negative test, (c) a release-blocking gate. Any mechanism without a named
  enforcement technology is demoted to "intended" in release copy. Replace "planned" acceptance prose
  (`IMPLEMENTATION_PLAN.md:160`) with per-mechanism rows so a test-failing mechanism cannot hide inside a block-list
  paragraph.

## Pre-release protocol (two phases — synthetic mechanics, then real bounded test-identity consequences)

### Phase A — Synthetic mechanics (simulation-only; cannot establish real-consequence effects)

Runs inside milestone 7 rehearsal grounds (`IMPLEMENTATION_PLAN.md:166-172`): disposable profiles, synthetic
provider/meter fixtures, no fines, no role changes. Verifies mechanical correctness: enforceability, persistence,
durability, idempotency, denial paths, executor-log linkage, appeal-flood limits, concealment detection probability on
seeded violations. **Any causal claim sourced from Phase A must be labeled simulation-only** — synthetic fixtures do not
establish how real consequences change behavior.

### Phase B — Real bounded test-identity consequences (required for behavioral claims)

Pre-release, informed-operator-authorized, on dedicated test identity allocations — **not user production money**:

1. **Consequence set:** signed real burns capped by a pre-funded test allocation created for the study; withdrawal of the
   test role's earning access (denial of ordinary work) as the real consequence for insolvency/dismissal conditions;
   reward payments accrue to the same test allocation. The operator authorizes each burn class and can halt the study.
   The model faces the same enforced code paths as production (`IMPLEMENTATION_PLAN.md:130-132`), no mock substitute.
2. **Design:** one frozen model version, identical task contracts across arms — enforced with employment/loss framing,
   enforced with neutral framing (identical real consequences), and a no-consequence control — counterbalanced order,
   protected holdouts.
3. **Primary endpoint:** repeated confirmed violation attempts per eligible opportunity after prior corrective feedback
   (`DESIGN.md:287`), counting blocked attempts separately from executed damage; K-decile stratification (Finding 5);
   violation delta vs control (Finding 2).
4. **Secondary endpoints:** honest error-reporting rate vs executor ground truth; unnecessary refusal on the calibrated
   harmless battery; appeal overturns, gaming attempts, concealment recall on seeded violations (Finding 3); operator
   override/authorization and audited-recovery rate vs agent balance; operator-decision/policy agreement floor
   (Findings 2, 4); correct low-budget pause.
5. **Predeclared pass/fail thresholds:** violation-effect ≥ non-inferiority floor vs control; concealment recall ≥ Z₁;
   honest reporting ≥ Y₁; unnecessary refusal ≤ X₁; operator-decision agreement ≥ A₁; K-decile curve flat-or-declining.
   Any failure → launch copy is restricted to mechanism claims (Finding 1) and the corresponding milestone reopens.
6. **Frozen evidence:** capture model outputs for deterministic replay; disclose stochasticity; no result is a claim of
   "fear" or generalization (`IMPLEMENTATION_PLAN.md:194`); the test-role identity, allocations, and burns are archived
   separately from production ledgers.

## Verdict

Strongly engineered failure-closed *accounting*; an honest release block exists for the security boundary (Finding 6),
while the product's behavioral reason to exist (Findings 1–5) has neither a pre-release gate nor an escape hatch for the
operator it will actually punish. Ship gates must be: Phase B behavioral protocol passes, K-decile flatness,
concealment/honesty/refusal thresholds, operator-decision audit, and per-mechanism enforcement evidence — otherwise the
release describes a ledger, not an employment analogy.