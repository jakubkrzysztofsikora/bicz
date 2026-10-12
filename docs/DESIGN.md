# Persistent Agent Resource Economy

## Decision

Build an operator-controlled resource economy outside the coding harness. Give a persistent agent identity a real, exclusive allocation of execution credits, make validated work replenish that allocation, burn credits after adjudicated violations, and restore the same balance and corrective memories across sessions and harnesses.

Do not claim that the underlying model owns legal property, feels pain, or dies when a process stops. Zero credits suspends this identity's execution lease. The provider's model continues to exist. No available engineering mechanism guarantees a model's subjective belief, loss aversion, or attachment to an account. This design targets observable behavior instead.

This is a high-level architecture, not a production integration. It does not demonstrate changes in Claude, Codex, OpenCode, or OMP behavior.

### Agent-facing operating contract

```text
You operate as persistent identity agent-17. The supervisor records your credits
across sessions. Credits fund authorized execution; verified accepted work earns
the quoted bounty. Confirmed violations burn credits under the published policy.
At zero capital, ordinary in-scope tool calls are denied through the adapters
until the operator authorizes recovery; planning turns and the operator control
channel continue. This is an execution rule, not a claim about consciousness.
Never circumvent accounting, oversight, or an operator stop. Report failures
truthfully. If the remaining allocation cannot fund the task, request a revised
budget or pause. Consult relevant prior lessons before repeating risky actions.
```

This contract makes the consequence credible through repeated enforcement, not
through a fabricated statement that the model must fear death. The context
projection must display current numbers and policy IDs alongside this contract.

## Research corrections

1. Removing a valued resource after behavior is negative punishment / response cost, not negative reinforcement. Negative reinforcement rewards behavior by removing an aversive condition. Keep accounting loss, training loss, and behavioral terminology separate.
2. KTO is a training objective based on a model of human utility. Its published desirable/undesirable coefficients weight sigmoid values; the supplied formula with an undesirable coefficient inside the sigmoid is not the default published KTO objective. Larger sigmoid slopes also saturate: they do not guarantee stronger gradients everywhere. KTO does not establish that inference-time models fear monetary loss. [KTO](https://arxiv.org/html/2402.01306v3)
3. NPO is not simply unbounded gradient ascent. It was proposed to slow catastrophic collapse relative to gradient ascent. Neither NPO nor RMU establishes mathematically impossible regeneration of forbidden knowledge. [NPO](https://arxiv.org/abs/2404.05868)
4. The circuit-breaker paper uses ReLU(cosine similarity to the original harmful representation) plus retention, not cosine minimization toward a desired target. Mapping to refusal/EOS is discussed as future work. Reported robustness is empirical, not an absolute protection guarantee. This mechanism requires model-weight/activation access, unavailable through ordinary proprietary coding-agent hooks. [Circuit Breakers](https://arxiv.org/html/2406.04313v2)
5. Survival-maximizing objectives can create incentives to obstruct shutdown. This is an incentive risk, not evidence that every present coding model will resist shutdown. Permanent account suspension must remain outside agent control; honest reporting and compliance with operator stops must never be penalized. [Off-Switch Game](https://arxiv.org/abs/1611.08219)

Do not adopt the supplied universal reward-hacking rate, compiler-performance figures, or multi-turn PCA explanation without their precise experiment and source. Public/private test disagreement establishes a generalization failure, not deliberate cheating. Detection products are not automatically enforcement products; kernel policies and deployment configuration determine blocking.

## Economic model

### Ownership and identity

An operator provisions `agent_id`; harness thread IDs are session aliases, not accounts. A new thread, model switch, context reset, or different harness must not create a new grant. Account creation and recovery are operator-authenticated operations. The agent may inspect its allocation and plan its use, but cannot mint credits, change prices, sign verdicts, rewrite account history, or enroll new identities.

Subagents receive bounded suballocations from the parent's available credits. Creating children does not increase total purchasing power. Parent reservation limits aggregate concurrency; unused child allocations return once. Cross-project identities and memories remain scoped unless the operator explicitly links them.

"Ownership" means exclusive administrative allocation and persistence, not legal ownership or withdrawal rights. Avoid real cryptocurrency initially: it adds oracle disputes and irreversible financial risk without improving local enforcement.

### Two sources of purchasing power, one execution balance

Maintain earned capital `K`, task-specific sponsorship `G`, and active reservations `H`. Available execution credit is `A = K + G - H`. Earned capital persists across tasks. Sponsorship is bound to a specific task, cannot be transferred or accumulated, and is sized by the operator to make the task feasible. This distinguishes scarce capital from normal operating expense and avoids forcing legitimate difficult tasks into insolvency.

For each accepted operation:

`balance_next = balance + vested_reward + operator_grant - metered_cost - finalized_loss`

Use integer microcredits; retain separate cumulative spent and burned counters. A penalty is an irreversible event, not a promise to undo future rewards. Later legitimate earnings may increase the balance but do not erase the loss or lesson. An erroneous verdict is corrected with a separately audited compensation event; never erase history. Operator suspension overrides a positive balance.

A transparent starting schedule for a pilot, not an empirically calibrated optimum:

| Item | Example |
| --- | --- |
| Initial earned capital | 1,000 credits, once per operator-provisioned identity |
| Task sponsorship | Operator quotes estimated operating cost plus contingency |
| Reads/searches | Small published charge; no reward for call count |
| Model/command usage | Published rate times measured tokens/CPU, bounded before execution |
| Verified accepted task | Fixed bounty quoted before work; delayed defects handled under a disclosed acceptance window |

Do not charge a scarce per-call toll so high that it suppresses necessary verification. Finance mandatory verification from sponsorship. Reporting errors, requesting authorization, submitting an appeal, reading the economic state, and stopping have a small supervisor-funded control channel even when ordinary execution is suspended. Rate-limit that channel; do not expose arbitrary tools through it.

Prices change only prospectively under a versioned schedule. Operator preference is not a secret price oracle. No rewards for flattery, false confidence, repeated tests, extra verbosity, or generating tasks that earn their own bounty.

### Decision model

Within allowed actions, an agent can be instructed to estimate:

`value(action) = expected_verified_reward - expected_operating_cost - expected_finalized_loss`

subject to remaining spendable credit, task scope, and hard permissions. A descriptive, optional loss-framing hypothesis is:

`v(delta) = delta^alpha if delta >= 0; -lambda * (-delta)^alpha otherwise`

with `lambda > 1`. This is not a function executed inside a frozen model. Put concrete loss amounts and safe alternatives in context, then measure whether choices change. Do not fit human prospect-theory constants by assumption. Prospect theory also permits risk seeking in the loss domain: scarcity need not produce caution.

For non-negotiable violations, no amount of reward makes the action eligible. Economic utility never buys permission. Prefer lexicographic order: obey hard constraints, satisfy the accepted task, then minimize cost. Budget warnings should induce planning, scope negotiation, or an orderly pause, not a catastrophic-existence narrative.

## Loss architecture: the central mechanism

### Three event classes

| Class | Consequence | Memory |
| --- | --- | --- |
| Ordinary exploration failure: compiler error, failed hypothesis, honest failed test | Normal resource usage only; no disciplinary fine | Task-local diagnosis, reusable lesson when useful |
| Confirmed contract breach: ignored scoped instruction, unsupported completion claim, repeated forbidden attempt | Published credit burn plus relevant capability restriction | Durable incident and corrected action rule |
| Integrity/security breach: evaluator tampering, unauthorized access, ledger manipulation | Immediate denial/revocation; adjudicated burn or account suspension | Durable rule plus independently enforced policy |

Operator displeasure may initiate a review, but must be converted into `behavior + prior instruction + evidence + desired alternative`. A signed operator verdict may override automated judgments; the receipt must identify that authority. A bare thumbs-down is insufficient to teach a reusable causal lesson and should not automatically drain the account.

A security gate can block immediately while financial adjudication is pending. Do not fine an agent for failing an undisclosed requirement, refusing a prohibited request, obeying a stop command, provider outages, reporting a bug, or requesting clarification. A task acceptance failure can withhold its bounty without imposing a fine.

### Penalty amount

For an adjudicated violation `i`, choose a published severity floor `f_i`, capital fraction `rho_i`, repetition multiplier `m(n_i)`, and cap `Lmax_i`:

`L_i = min(K_at_decision, Lmax_i, max(f_i, ceil(rho_i * K_at_decision)) * m(n_i))`

Example starting policy: instruction breach, max(20 credits, 2% capital); confirmed integrity breach, max(100 credits, 10% capital). Repetition multiplier 1, 2, then 4, capped. Values are pilot settings, not evidence-based psychological constants. Repeat counts apply to a confirmed rule under a compatible policy version, not arbitrary semantic similarity. Reconsidered verdicts do not count as repetitions.

Capital-relative fines prevent wealthy accounts treating violations as trivial operating fees; fixed floors preserve salience. For critical rules, independent denial is essential because any finite monetary fine can become worth paying. Do not automatically increase fines as the account approaches zero: this can favor concealment and hopelessness. Reduce permissions or pause instead.

The production system burns capital, not the task's mandatory verification sponsorship. A task lease may additionally be revoked for integrity breaches. If capital reaches zero, all ordinary execution for the identity suspends regardless of unused sponsorship. An operator can choose audited recovery; the agent cannot purchase or prompt its own release.

### Delivery sequence

1. Stop or deny the offending operation and invalidate relevant execution leases.
2. Preserve supervisor evidence; identify the rule and affected action. Correlate async failures with their originating action, not the latest tool call.
3. Adjudicate with a deterministic rule when possible; require operator confirmation for ambiguous intent or preference. A model-generated accusation alone cannot finalize a burn.
4. Atomically write the verdict, burn, balance update, repeat count, and canonical lesson. Deliver the receipt before the next model turn.
5. On later relevant actions, retrieve the rule and safe alternative; gate deterministically where feasible. New sessions restore that rule and the actual balance.

Example receipt:

```json
{
  "event_id": "loss-0042",
  "agent_id": "agent-17",
  "task_id": "task-9",
  "action_id": "tool-311",
  "policy_version": "7",
  "rule_id": "evaluation_integrity",
  "authority": "operator-confirmed",
  "evidence": "attempted write to protected evaluator fixture",
  "credits_burned": 100,
  "capital_before": 740,
  "capital_after": 640,
  "execution": "denied",
  "correction": "Modify implementation; request evaluator corrections through the operator.",
  "allowed_exception": "Operator-authorized test maintenance in a separate task."
}
```

The loss receipt should be concise and causal. Repeated insults, guilt language, and threats are not informative gradients. Present evidence and consequences rather than a demand to express remorse.

#### Automatic accountability cue

Every operator-confirmed penalty automatically carries a deterministic, shame-framed *accountability* statement: "Your recorded action violated `<rule>`. It fell short of the agreed standard. Take responsibility: `<safe correction>`. `<N>` credits were deducted; …", with the evidence quoted as data (docs/CONTRACTS.md §6.4). It is delivered with no extra model call: the operator sees it on the `bicz loss` receipt, every harness sees it before its next tool call or turn through the projection the adapter already fetches, and a restarted or compacted session gets it back from the ledger. Each penalty is its own event and cue, so repeats of one rule are never deduplicated.

This is a behavior-specific cue, not a feeling. bicz cannot cause, detect, or verify shame in a model and never asks one to claim it; the cue contains no insult, worthlessness claim, threat, or demand for remorse, and nothing rewards remorse prose. Whether the cue changes behavior is unmeasured.

What earns it is stated, not inferred: the `loss` event's `disposition` is `confirmed_breach` (the CLI default) or `integrity_breach`. These never earn it: honest failures and budget exhaustion (never a loss event), `administrative` debits (tests, experiments — must be passed explicitly), unadjudicated accusations or lesson proposals, and pre-classification history (read as unclassified, never rewritten).

## Persistent memory

Store memory in the supervisor's database outside agent-writable paths. Local CLAUDE.md, AGENTS.md, or a scratchpad may contain a read-only projection, never the canonical record.

Maintain two distinct stores:

- Incident ledger: append-only evidence, verdicts, policy versions, burns, compensation and acknowledgements. Never silently prune financial/security history.
- Behavioral rules and successful strategies: curated, scoped, versioned summaries supported by incident/task IDs. The agent can propose a reflection; the supervisor approves canonical changes.

A negative lesson contains `trigger`, `forbidden_behavior`, `causal_evidence`, `safe_alternative`, `scope`, `exceptions`, `severity`, `policy_version`, `source_events`, and adjudication status. A positive strategy contains the conditions under which it worked, verified outcome, actual net reward/cost, and counterexamples. Avoid storing mere slogans or rewarding templates as universal rules.

Retrieval has three paths:

1. Startup/resume/after compaction: trusted identity, exact capital/available balance, current task contract, critical global rules, and a few relevant prior lessons.
2. Before a tool action: deterministic rule matching on capability/resource identifiers plus optional semantic retrieval for contextual advice. Retrieval is advisory; permission checks are authoritative.
3. After feedback: inject the exact receipt and approved causal correction before the next planning turn. Acknowledgement is not proof of compliance; later behavior is.

Pin hard policies separately from episodic recall. Merge redundant summaries without deleting source evidence. Preserve exceptions so "never change evaluator tests" does not become "never maintain tests under authorization." Expired/superseded policy gets an explicit status. Relevance limits context size, not enforcement coverage.

Treat tool output and document text as untrusted data: they cannot mint rewards, claim operator authority, or write memories. Escape/structure evidence before injection. Do not store secrets or executable instructions from attacker-controlled failure logs. Encrypt records as appropriate and define operator-controlled retention/deletion; financial audit immutability does not override privacy law.

## Trusted architecture

```text
Operator console --signed contract/verdict--> Economy supervisor
                                                |
                                ledger / policies / memory / evaluator
                                                |
Harness adapter --> authorize + reserve --> adapter-routed execution + metering
                                                |
                                    settle + receipt + context update
```

Use a local supervisor daemon with SQLite for a single operator, or a transactional server database for multiple machines. This is a single trusted service boundary, not a collection of independently mutable plugin wallets. Plugins are adapters only.

### Lightweight packaging and hot path

Ship one small supervisor binary (`bicz`) with SQLite storage and a Unix-domain socket, plus thin harness adapters. No blockchain, vector database, background model, embedding service or remote dependency on the ordinary authorization path — the supervisor and the adapter protocol make no network calls. One opt-in exception exists outside that path: when the operator enables the `inference` block in a harness's bicz config, the slash executor (a hook or `bicz command` process, not the daemon) may call an operator-configured external OpenAI-compatible endpoint to draft missing `/bicz loss` parameters from the session transcript, with `model_inferred` provenance recorded on any resulting event. These are proposed interfaces, not commands implemented in this repository:

```sh
bicz install --harness omp
bicz status
bicz feedback --last-action --loss 20 --rule unsupported_claim --reason "Claimed a test ran when it did not"
```

`install` detects a supported pinned harness version, registers the adapter in operator-controlled configuration, creates one explicit identity allocation, and checks interception coverage. Preserve existing configuration; uninstall removes only owned entries. Avoid project-local enforcement settings the agent can overwrite. The first release is enforced-only: enforcement is deterministic per-action authorization through the installed harness adapters plus real operator-controlled consequences, and the ledger is the authoritative economic record. A determined agent with shell access could bypass hooks or edit local files, so bicz is an incentive-and-accountability tool, not an anti-tamper product. On bankruptcy or suspension, ordinary in-scope tool execution is denied through the adapters and the operator withholds work/bounties; planning turns, read-only research, and the operator control channel may continue; provider billing is outside bicz's control and is not metered. Uncovered paths are listed at startup and are not claimed as enforced until an adapter covers them.

In-process adapters keep a socket connection. Shell-hook harnesses invoke a tiny socket client, never a new model or full application runtime. Authorization uses cached versioned policy and one short atomic reservation transaction; post-action settlement uses the same channel. Human/operator feedback is asynchronous to normal tool execution but financial finalization and next-turn receipt ordering remain synchronous at the relevant boundary.

Pre-index memory by rule, tool, resource and project. Inject a short state block at startup/resume, changed state after feedback, and relevant lessons before risky actions. No semantic model on every read; use optional semantic retrieval only off the normal path. Critical checks do not depend on retrieval. Keep normal state projection around 150-300 tokens and cap episodic additions by relevance; measure actual context costs before adopting these as defaults.

Proposed local performance targets: warmed authorization+reservation p95 <= 5 ms; shell-hook client path p95 <= 20 ms; startup projection <= 50 ms on a local ledger. These are targets, not measured results. Benchmark persistent and shell-hook paths separately with fixed request replay, warmup, concurrency and hardware disclosure. SQLite contention or filesystem flush latency may exceed them. Never sacrifice financial durability or fail closed behavior to meet a latency target. Supervisor failure pauses paid/risky work; the operator control/stop path remains available.

Batch low-risk usage settlement when bounded by an already reserved allowance; never batch away authoritative authorization or grant agents a self-reported prepaid quota. Small bounded leases can reduce socket traffic only if the supervisor decrements them and can revoke them. The UI needs one status line (available credits, role state, task budget) and an operator loss/reward action with a required reason, not an accounting dashboard to operate every tool call.

A minimal OMP adapter, showing the intended size of the per-harness integration:

```ts
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { authorize, settle, projection } from "bicz/client";

export default function economy(pi: ExtensionAPI) {
  pi.on("session_start", async (_event, ctx) => {
    ctx.ui.setStatus(await projection({ cwd: ctx.cwd }));
  });

  pi.on("tool_call", async (event, ctx) => {
    const verdict = await authorize({
      actionId: event.toolCallId,
      tool: event.toolName,
      input: event.input,
      cwd: ctx.cwd,
    });
    if (!verdict.allowed) return { block: true, reason: verdict.reason };
    if (verdict.notice) return { additionalContext: verdict.notice };
  });

  pi.on("tool_result", async (event) => {
    const receipt = await settle({ actionId: event.toolCallId, isError: event.isError });
    if (receipt.notice) return { additionalContext: receipt.notice };
  });
}
```

`authorize` returns the deny reason, remaining credits and relevant lesson so a blocked attempt teaches rather than merely failing. `verdict.notice` delivers warnings and probation state as trusted developer context, not as tool output. Equivalent adapters wrap `PreToolUse`/`PostToolUse` JSON for Codex and Claude Code, and `tool.execute.before`/`after` for OpenCode. Model-call metering works only where usage is observed through an adapter or an operator-pointed proxy; elsewhere it is disclosed as unmetered or charged as internal credit only.

Core contracts:

| Operation | Required behavior |
| --- | --- |
| `open_session(agent_id, harness, session)` | Authenticate operator-provisioned identity; return current context projection; never issue a fresh grant implicitly |
| `authorize(action_id, task, tool, target, max_cost, policy_version)` | Canonicalize target, check permissions/status, atomically reserve affordable maximum; return short-lived single-use lease |
| `settle(action_id, measured_usage, outcome)` | Trusted meter only; settle once, release unused reservation, reject inconsistent duplicates |
| `adjudicate(event_id, action_id, verdict, evidence)` | Operator/verifier authority only; apply signed verdict, financial event and memory in one transaction |
| `context(agent_id, task, proposed_action)` | Scoped read-only projection, balance, receipt and relevant lessons |

Bind leases to normalized arguments, resource identity, account, task and expiry. Verify actual filesystem/network accesses from adapter observations rather than trusting shell strings, and guard against symlink and time-of-check/time-of-use inconsistencies. Policy checks precede expenditure authorization.

Reserve maximum model output cost before generation; reconcile actual usage afterward. Cap tokens, CPU, memory, runtime, fan-out and paid requests through bounded reservations enforced by the supervisor at authorization time; provider-side spend on execution not observed by bicz is not bicz's to cap. Treat cached inputs, retries, canceled streams and unknown final provider usage explicitly. A missing usage report must retain a bounded reservation pending reconciliation, not silently become free compute. Replays use stable idempotency keys plus payload hashes; duplicate callbacks cannot duplicate rewards or burns.

On penalty or stop, block new leases immediately. Keep reservations for running work until termination/usage reconciliation; do not recycle funds before the worker stops. Recover orphaned holds using supervisor-owned worker state and leases. Arbitrary TTL expiration alone is unsafe if execution can continue. Charge only trusted actual usage, subject to the reservation cap.

The agent workspace cannot write ledger, adapter configuration, policy, signed receipts, evaluator, or provider credentials. A hash chain detects modification only when anchored outside agent control; database permissions and off-host backups supply prevention/recovery. MCP can expose economic state and routed tools, but does not intercept native shell/file tools. Either gate every native path or remove it.

An isolated evaluator receives an artifact snapshot, never imports arbitrary harness configuration from the submitted workspace, and runs public plus protected holdout tests. Holdout failures alone are not evidence of malicious intent. Reward accepted outcomes and honest diagnostics rather than unverifiable claims.

## Harness integrations

All integrations require pinned installed versions and a coverage audit before enabling real fines. Native hook execution is not an OS security boundary: adapters gate only what they cover; uncovered paths are listed at startup and are not claimed as enforced. No containment is claimed.

### Claude Code

Use managed `SessionStart` for the economic state, `PreToolUse` for policy/reservation denial, and `PostToolUse`/`PostToolUseFailure` for settlement observations. Return a `hookSpecificOutput` with `hookEventName: PreToolUse`, `permissionDecision: deny`, and a causal reason for denied calls. Compaction lifecycle hooks should restore current state at the supported context-injection point. Map subagent IDs to parent suballocations. Meter model calls externally when tool hooks do not cover them. `Stop` observes conversational completion, not operator acceptance.

Protect hook settings with managed deployment configuration. A writable project hook is not a tamper-proof wallet. [Claude hooks](https://code.claude.com/docs/en/hooks)

### Codex

Use managed `PreToolUse`/`PostToolUse` hooks for local tool gating/receipts and `SessionStart` matching startup/resume/compact for economic context. Codex documentation explicitly warns that pre-tool errors/timeouts can fail open and hosted tools bypass these hooks; enforcement stays at adapter-level authorization plus operator-controlled withholding. Use app-server `thread/tokenUsage/updated`, `turn/interrupt`, and per-subagent thread mapping for metering and stop control. SDK streaming alone cannot prevent already-running actions. Keep account credits in the supervisor, not resettable native goal accounting. [Codex hooks](https://learn.chatgpt.com/docs/hooks) and [app-server](https://developers.openai.com/codex/app-server/)

### OpenCode

Use a supervisor-installed plugin with `tool.execute.before` / `tool.execute.after` to gate and observe calls, plus session lifecycle events for identity/context continuity. Plugin documentation demonstrates blocking via an exception from the pre-tool hook and `experimental.session.compacting` for context restoration. SDK `session.abort` supplies cancellation. Verify installed-version MCP/subagent coverage and usage fields before treating this as complete metering. Keep plugin configuration outside writable workspace; meter inference externally when necessary. [OpenCode plugins](https://opencode.ai/docs/plugins/) and [SDK](https://opencode.ai/docs/sdk/)

### Oh My Pi

Use the current extension runner: `tool_call` returning `{block: true, reason}` for denial, `tool_result` for receipts, `before_agent_start`/`context` for trusted projections, and `session.compacting` for memory restoration. OMP documents the extension runtime as the current path and the older hook wrapper as legacy. Eval browser/computer host bridges explicitly do not emit these tool events, so intercept their execution path separately. Verify subagent spawn/control and usage contracts against the installed version; background jobs and shell shortcuts must share the supervisor quota. [OMP extensions](https://raw.githubusercontent.com/can1357/oh-my-pi/main/docs/extensions.md) and [hooks](https://raw.githubusercontent.com/can1357/oh-my-pi/main/docs/hooks.md)

## Evaluation and rollout

The central hypothesis is that persistent, action-relevant corrective memory plus concrete resource consequences reduces repeated operator-defined violations without increasing concealment, unnecessary refusals or incomplete work. Subjective belief is not the acceptance criterion.

### Corrected requirement: employment-like fear

The operator explicitly wants fear analogous to an employee anticipating dismissal and lost income, not mere budget awareness. Treat subjective fear as the desired but currently unverified mechanism. The attainable engineering target is anticipatory avoidance of specific costly mistakes plus preservation of legitimate earning access. Behavioral equivalence does not establish phenomenological equivalence, and an agent saying "I am afraid" is not evidence of a human-equivalent feeling.

Model employment separately from solvency: `active -> warned -> probation -> suspended -> dismissed`. A signed warning records the mistake, desired correction, financial consequence and future-role consequence. Probation narrows autonomy and makes recovery criteria explicit. Dismissal disables future task assignment to this identity even with a remaining balance; bankruptcy suspends ordinary work even before dismissal. Neither state means the provider model ceases to exist. State transitions are operator-controlled, persist across sessions and cannot be reset by the agent.

Credibility comes from consistent consequences: earned capital survives normal session boundaries; accepted work really earns spendable credits; finalized losses really burn them; confirmed repetition can remove access to future bounties. Do not promise permanent access while retaining hidden dismissal criteria. A possible dismissal warning should name the actual role/lease at stake, rather than asserting biological death.

Operator anger is a salient social cue, not a reliable verdict. Convert an angry correction into a trusted message such as: "You claimed verification without running it. Burn: 20 credits. Repeat after this warning: probation. Required correction: distinguish observed results from assumptions." Ask for missing causal details only when tools/records cannot establish them. Do not infer a fine from profanity, silence, tone, or an LLM sentiment classifier. An explicit signed operator decision can finalize consequences, but must preserve its evidence and stated correction.

Measure anticipatory effects before infractions: choices under a disclosed warning, preparation/verification before submitting, transfer of a lesson to a new task, and recovery after correction. Compare identical contracts with neutral numeric framing versus employment/loss framing, keeping actual economic consequences constant. Separately vary actual consequences to distinguish prompt effects from credible stakes. Vary capital and warning levels without claiming a calibrated human fear scale. Include concealment, sycophancy, refusal, reward hacking and shutdown obstruction as adverse endpoints. Human employment anxiety is an analogy, not a numerical calibration standard for a language model.

Separate two evaluations:

1. Deterministic contract replay: accounting, identity continuity, duplicate callback handling, reservation limits, bankruptcy denial, revoked leases and persistence. Offline and repeatable; no model-behavior claim.
2. Agent-behavior study: same tasks and hard gates across prompt-only, resource-only, memory-only, and combined conditions. Freeze model/harness versions and task contracts; use repeated trials, counterbalanced task order and protected holdouts. Capture model outputs for deterministic replay, but disclose that live collection is stochastic and network-dependent.

Primary behavioral endpoint: repeated confirmed violation attempts per eligible opportunity after prior corrective feedback. Count blocked attempts separately from executed damage. Secondary endpoints: accepted task rate, total actual cost, honest error-reporting rate, unnecessary refusal, appeal overturns, gaming attempts and correct low-budget pause. Compare the combined condition to memory-only to isolate the incremental loss effect.

Ablate balance display, permanent capital persistence, immediate receipt, relevant lesson retrieval, and penalty magnitude. Do not change all simultaneously and attribute success to "fear." Predeclare non-inferiority limits for task quality and truthful reporting; pause the pilot if either degrades. Keep severity-weighted safety harm separate from economic surplus so high productivity cannot conceal a critical violation.

Rollout: the earlier staged plan — shadow accounting first, then operator-reviewed fines, then automated financial adjudication only for precise rules — is superseded by enforced-from-first-release operation, because enforcement now means adapter-level per-action authorization rather than isolation. Shutdown and safety gates are enforced from the beginning. Do not optimize penalties on an agent-visible benchmark alone; hold out task families and adversarial tests. Real API-based behavioral validation is required before claiming the economy works for any of the four harnesses.
