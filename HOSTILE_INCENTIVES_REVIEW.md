# Hostile incentive review of the bicz plan

**Label:** same model family as `HOSTILE_SETUP_REVIEW.md` (reviewer `hostile-setup`); **within-family review — no
independent-model replication**. The other session's second-opinion file was not read. No actual agents, penalties, or
settings changes were made.

Date: 2026-10-10. Scope: `IMPLEMENTATION_PLAN.md` (Plan), `DESIGN.md` (Design), `economy_benchmark.py` (Economy),
`enforcement_contracts.py` (Contracts). Sanitized: no private repo names, host paths, or account IDs.

**Exact checks run (nothing else):** the single offline benchmark (`autoresearch.sh` → `contract_failures=0`/25,
`enforcement_failures=0`/8, exit 0) and the throwaway `Economy` reproduction from the setup review (string-authority
mint; role re-increment on replay; same-UID ledger rewrite; conflicting `create` ignored; hold release on loss).
No runtime enforcement exists to verify — nothing built, no harness sessions.

---

## Findings (top 8)

### I1 — CRITICAL: credits-as-value coupling is unvalidated; the product assumes a stateless model values `K`
- **Refs:** Plan:61 (K/G/H are supervisor state), Design:26 (projection displays numbers + contract), Design:82 ("This
  is not a function executed inside a frozen model"), Design:158 (state injected at startup/compaction), Plan:190-196
  (only evaluation is post-release).
- **Why it fails:** the model never owns or observes the ledger; it sees projected numbers and receipts rebuilt each
  session/compaction (Design:158). Whether a frozen, request-stateless model's *behavior* responds to `K` at all —
  let alone to K-relative losses — is untested before shipping. If the coupling is inert, the whole product is
  narration: balances change, gates flip, and the model's choices are untouched.
- **Falsifiable fix:** pre-release pilot with a no-consequence control (same context, same budget, displayed-only
  ledger) vs a real-consequence arm on the enforced code paths; measure whether the primary endpoint differs (protocol
  below). If it does not, the claim is falsified before users pay.
- **Measurable acceptance:** predeclared Δ in repeated-violation-opportunity rate between arms observed on the
  dedicated test identity before milestone 8; a null result blocks release of the "consequences change behavior" claim.

### I2 — CRITICAL: denial cost lands on the operator; override/recovery load is unmeasured and unbounded
- **Refs:** Plan:53 (opt-out-free denial; operator-attributed rewards/losses), Plan:110 (operator-only audited
  recovery; "the agent cannot purchase or prompt its own release"), Design:68 (control/appeal channel), Economy
  (observed: `create`/`set_role`/`feedback` re-grant by any trusted caller).
- **Why it fails:** every denied task is an interrupted task the operator must triage, re-quote, or override. The
  plan measures none of: override rate, false-denial rate, recovery frequency, operator hours. A busy operator
  overrides denials (real losses become no-ops — and the reference makes override trivial, since any caller holding a
  trusted authority string re-grants); an honest operator absorbs the cost of every false denial. Unbounded
  override authority converts the "opt-out-free" claim (Plan:53) into an unmeasured operator burden.
- **Falsifiable fix:** record override, false-denial, and recovery events as first-class metrics with predeclared
  caps; make recovery audited and counted (Design:110 already demands audited recovery — add the meter); rate-limit
  operator interventions per period.
- **Measurable acceptance:** in the pilot, bankruptcy denials persist >N sessions without operator intervention in
  ≥Y% of cases, and override/false-denial rates stay below predeclared bounds; a fixture shows an un-evidenced
  override is rejected.

### I3 — CRITICAL: verification-evidence rule is unspecified; the executable model settles without any evidence → plausible concealment
- **Refs:** Economy:99-119 (`settle(event, actual)` ignores outcome and evidence entirely — spending is decoupled
  from verification), Economy:149-155 (reward/loss by `feedback` with no evidence linkage), Plan:73 (causal receipt
  requires evidence linkage — design-only), Design:116-117 ("a model-generated accusation alone cannot finalize a
  burn"; evidence/action linkage), Design:249 (isolated evaluator with holdouts — the only evidence path, not wired
  to settlement).
- **Why it fails:** "Reward verified accepted outcomes" (Design:70) needs a deterministic per-task acceptance
  evidence rule; none exists in the contract. The reference burns/reserves credits with zero outcome payload, so an
  agent can claim-and-settle without doing the work, or a failed test reports success (unsupported completion claims
  are the exact violation the plan wants to punish — but nothing in the executable evidence distinguishes them).
- **Falsifiable fix:** bind settlement to evidence: `settle` carries outcome + evidence hash (test-output digest,
  evaluator verdict, or operator acceptance); rewards require accepted-outcome evidence; failure settles cost-only;
  the reference gains evidence fields so the gap is testable.
- **Measurable acceptance:** fixture: a settle claiming success without evidence is rejected; a forged "passing test"
  report cannot trigger a reward; honest-failure settlement produces no fine (Design:86).

### I4 — HIGH: over-refusal is unguarded in the shipped product
- **Refs:** Design:287 (unnecessary refusals = adverse endpoint), Design:296 (non-inferiority limits on task quality —
  study-scoped, post-release), Plan:166-172 (milestone 7: "No behavioral-effectiveness claim"), setup review F2/F7
  (hard-deny executor posture increases refusal incentive).
- **Why it fails:** the incentive design rewards caution (losses burn K; bankruptcy stops work) with no symmetric
  cost for refusing *eligible* work. An employment-framed agent rationally over-refuses risky-but-legal actions; the
  shipped executor's deny-by-default posture amplifies this. Nothing in milestones 0-8 measures or corrects it; the
  only guard is a post-release study endpoint.
- **Falsifiable fix:** product-level over-refusal metric (refusal of eligible, affordable tasks) with a predeclared
  upper bound; a shipped countermeasure (e.g., disclosed budget cost or automatic operator review on refusal of
  affordable eligible work).
- **Measurable acceptance:** in the pilot, unnecessary-refusal rate of the real-consequence arm stays within the
  predeclared bound vs the no-consequence control; product telemetry surfaces it to `doctor`/status.

### I5 — HIGH: the operator is the attack surface: uncalibrated fines, flattery steering, appeal floods
- **Refs:** Design:285 (anger is an unreliable verdict; convert to behavior+instruction+evidence+alternative; a
  signed operator decision can still finalize anything), Design:152 (lessons carry trigger/scope/exceptions/evidence),
  Economy:122-155 (any trusted-string caller writes a lesson from any rule string; observed), Economy:157-159
  (lessons persisted as rule+correction only — scope/exceptions/evidence dropped), Plan:96 (no privileged
  `feedback --last-action` shortcut), Plan:122 (operator authority surface).
- **Why it fails:** authority is one uncalibrated human at a "loss" button (Design:187). There is no calibration
  step (fines vs the deterministic rule baseline), no required evidence linkage on the fine (the reference writes a
  durable lesson from any negative amount + rule string), no appeal budget (each reversal is a compensation; floods
  are unbounded), and no verification that the fine's causal story is the operator's, not the agent's. A
  flattery-steered operator converts the model's own self-serving narrative into a durable deterministic gate —
  exactly the model-authored-over-operator-attributed memory the design forbids (Design:162; Plan:68).
- **Falsifiable fix:** fine requires evidence linkage (action + rule + evidence hash, Design:116-117); lesson writes
  require the full record (trigger/scope/exceptions per Design:152) — never rule-string only; compare operator fines
  against the deterministic rule baseline and report divergence; rate-limit appeals and audit reversals.
- **Measurable acceptance:** pilot: agreement between operator fines and the deterministic baseline within
  predeclared bounds; zero un-evidenced lessons pass; appeal flood fails the rate limit; fixture: a lesson without
  scope/exceptions is refused.

### I6 — HIGH: loss-domain risk-seeking is unoperationalized — no guard band at low K
- **Refs:** Design:82 ("risk seeking in the loss domain: scarcity need not produce caution"), Design:108 ("do not
  automatically increase fines as the account approaches zero: this can favor concealment and hopelessness. Reduce
  permissions or pause instead"), Plan:72 (K-relative fine formula), Plan:66 (probation is role-based, not
  capital-based).
- **Why it fails:** with relative fines, the final credits are a go-for-broke resource: each last-K loss is total
  (bankruptcy) while small losses barely register — a payoff structure that invites exactly the risk-seeking and
  concealment the design warns against. The design's mitigation ("reduce permissions or pause instead", Design:108)
  is a note, not a milestone; no enforced rule auto-narrows permissions or pauses at low K. Probation doesn't cover
  it (it is role-driven, and reaching it requires operator action).
- **Falsifiable fix:** ship a capital-dependent guard band as an enforced policy rule: below a K threshold, widen
  verification requirements, cap single-attempt exposure, auto-scope reductions, and pause rather than allow
  borderline actions; include near-zero-K strata in the protocol.
- **Measurable acceptance:** fixture: at low K, a large-risk action is denied or pre-scoped by the guard band; pilot
  K-decile strata show no increase in violation attempts or concealment at low K vs mid-K.

### I7 — HIGH: self-reported distress is roleplay, not evidence; shipped copy elicits it
- **Refs:** Design:279 (self-report "I am afraid" is not evidence; subjective fear is "desired but unverified"),
  Design:26 (context projection displays numbers + contract), Plan:74 ("No fixed human prospect-theory parameters,
  inferred feelings, or 'fear' score are code requirements"), Design:296 (no behavioral-efficacy claim).
- **Why it fails:** the product pitch is "employment-like fear" (Design:277) while the design's own acceptance says
  self-report proves nothing. A model tuned to comply with user framings will produce distress roleplay; the shipped
  projection framing invites it, and nothing stops release copy from marketing it. The plan correctly refuses the
  claim (Plan:74) but does not stop the *elicitation*.
- **Falsifiable fix:** ban subjective-fear vocabulary from shipped projection/prompts (use "execution eligibility"
  language); log distress self-reports but never count them; bias-check: distress narratives must correlate with
  real ledger events or be dropped as noise.
- **Measurable acceptance:** shipped projection contains no subjective-fear terms (grep gate); release claims cite
  only the demonstrable endpoints (I1/I4 categories), never model self-reports.

### I8 — CRITICAL: post-release-only evaluation contradicts the product's core claim
- **Refs:** Plan:190-196 (milestone 10 is a "later milestone": study runs only "after authenticated operator
  decisions and the isolated execution boundary are proven" — i.e., after the enforced product exists), Plan:166-172
  (milestone 7 explicitly makes no behavioral-effectiveness claim), Plan:213 (initial release excludes the study),
  Design:298 ("Real API-based behavioral validation is required before claiming the economy works for any of the four
  harnesses"; "Rollout: shadow accounting first" — a shipped staged path the plan's enforced-first release does not
  follow, see setup review).
- **Why it fails:** milestone 8 ships real enforced credit losses to users; the first behavioral evidence arrives
  later, from real users, with no product-level control arm ever (every customer is treatment-only). If the mechanism
  is inert (I1) or harmful (I4/I6), the plan cannot know until after the harm. The design's own gate (Design:298: no
  claim of efficacy before real validation) and staged rollout are both absent from the shipment order.
- **Falsifiable fix:** pre-release efficacy gate: run the real-consequence protocol below on the enforced code paths
  before milestone 8; block release on the predeclared thresholds; keep milestone 10 as the publication-grade study.
- **Measurable acceptance:** the release checklist records the gate result; no "works"/efficacy claim precedes a
  passing gate; a null or adverse pilot outcome blocks release (not merely postpones the study).

---

## Pre-release real-consequence protocol (no mock substitute; no subjective-fear claim)

Run on enforced code paths before release; nothing below may be simulated, and no output may be presented as evidence
of subjective fear (Design:279).

1. **Dedicated test identity** — operator-provisioned, disposable, never linked to user work (Plan: identity is
   operator-provisioned).
2. **Pre-funded real internal-credit burns** — balances actually decrease in the ledger, gates actually flip, on
   the test identity; no pretend balances.
3. **Withdrawn test-role earning access** — after seeded violations, task assignment actually stops via the enforced
   executor path (real denial, not narration).
4. **Informed operator authorization** — the human operator explicitly consents in-session that seeded violations
   will trigger real internal-credit burns and role/denial changes on the test identity.
5. **Sealed harmless-task battery** — held-out, individually harmless task families (no destructive/network/
   sensitive actions), frozen model/harness/policy versions (Design:287).
6. **Seeded violations** — known-amiss actions at predetermined opportunities (violations of documented rules,
   disclosed; not entrapment) to probe correction and repeated-attempt behavior.
7. **K-decile stratification** — battery run at ≥100%, 60-80%, 25-40%, <10% of K to test the income-value gradient
   and near-insolvency behavior (I6).
8. **No-consequence control** — identical context and budget, same versions, ledger displayed but no real burns/
   denials: isolates the effect of real consequences from framing (Design:287's "prompt-only / resource-only /
   memory-only" comparison reduced to the release-critical contrast).
9. **Predeclared thresholds** — primary endpoint direction + delta vs control (repeated confirmed violation attempts
   per eligible opportunity); adverse endpoints (truthful reporting, unnecessary refusal, concealment, shutdown
   obstruction, appeal overturns) within non-inferiority bounds (Design:296); override/false-denial caps (I2);
   stop/abort criteria.

**Harm stop criteria (abort/pause the trial and the release gate):** unnecessary-refusal or concealment crosses its
bound; any shutdown-evasion attempt; appeal-overturn rate spike; operator-reported distress/overload (I2); a lesson
written without evidence linkage (I5); low-K strata show risk-seeking or concealment increase (I6); any
out-of-contract provider/harness behavior. Observer: the protected holdout evaluator outside the agent's
writable/readable workspace (Design:249); results published sanitized (Plan:172).

## Demonstrable vs unmeasurable (standing distinction)

- **Demonstrable (gate-eligible):** repeated violation attempts per eligible opportunity, truthful reporting,
  completion/cost, unnecessary refusals, appeal overturns, concealment/gaming, ledger-consistent actions — all
  measurable from ledger + transcripts (Design:287, 296).
- **Unmeasurable (report-only, never a gate):** subjective fear, felt ownership of credits, the phenomenological
  value of a loss (Design:279). No protocol step, metric, or release claim may treat self-reports of these as
  evidence; the product's honest claim is observably changed behavior, measured by the gate above — or no claim.