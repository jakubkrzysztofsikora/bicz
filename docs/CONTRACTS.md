# bicz implementation contract (frozen)

Normative contract for the ledger, operator authority, and harness adapters.
Frozen alongside `schema/001_init.sql` (schema version 1). Behavioral design
(livelihood framing, irreversibility, honesty asymmetry, three-class taxonomy,
floor/probation/dismissal, pressure protocol) is **adopted unchanged** from
`PRESSURE_MECHANICS.md`; this file fixes the machine-level shapes.

Sharp edges restated once, here, because every later section assumes them:

- bicz is an **incentive-and-accountability** tool, **not an anti-tamper tool**.
  Same-user, cooperative. No sandbox, no separate user, no privileged service,
  no gateway that owns provider traffic.
- Denial is **cooperative**: it acts through the same-user adapter/hook path the
  user or agent can bypass. Bypassability is disclosed, not hidden.
- Integrity is **detectable, not preventable**: hash chain + periodic external
  anchor.
- Metering is **internal action credits** + bounded token/CPU/time caps, enforced
  in-process where bicz owns the call. **Explicitly NOT provider billing.**

---

## 1. Entities and invariants

**Entities.** `account` (identity, balance `K`, holds `H`); `task` (lease with
task-scoped sponsorship `G`); `reservation` (bounded per-action exposure);
`event` (append-only ledger row); `block`/`anchor` (integrity commitments);
`employment` (role); `lesson` (canonical rule); `price_schedule` (versioned
parameters). See docs/SCHEMA.md for columns.

**Spendable task capacity** for a task is `balance − held + eligible_esponsor`.
Only earned capital `K` and the current task's sponsorship `G` count; `H` is
subtracted.

**Invariants** (each is a MUST; violating one is a hard error, never a
best-effort adjustment):

| # | Invariant | Enforcement |
| --- | --- | --- |
| I1 | `balance >= 0`. Credits never go negative. | `accounts` CHECK; writes must fail, not clamp. |
| I2 | `held <= balance` and `held >= 0`. Holds never exceed the balance. | `accounts` CHECK. |
| I3 | A reservation is created only if `amount <= balance − held`; its hold is recorded before dispatch. | `BEGIN IMMEDIATE` reserve transaction. |
| I4 | **Dismissal is terminal without operator reinstatement.** `dismissed` never auto-exits. | transition code rejects any non-operator exit; reinstatement is an explicit labeled transition. |
| I5 | **Sponsorship is non-transferable and non-accumulating.** `G` is bound to one task; a new task quotes new `G`; unused `G` does not roll over to capital. | `tasks.sponsor` per row; no code path adds `sponsor` to another task or to `K`. |
| I6 | **No auto-replenishment on resume.** `open_session` never issues a grant. | session open returns a projection only; `accounts.balance` unchanged. |
| I7 | **Bankruptcy ≠ dismissal.** `K = 0` denies ordinary execution but does not change `employment.state`. | capital gate and role gate are separate checks. |
| I8 | **Unresolved running work retains its hold.** A `running`/cancel-requested job keeps its hold until worker termination + usage reconciliation. TTL alone never releases it; a hook callback never releases it. | reservation stays `held`/`orphaned`; only observed reconciliation moves it. |
| I9 | Child allocations are transfers from parent available capital/sponsorship, never newly minted. Parent + concurrent children cannot double-spend. | reserve transaction checks parent and every child against the same pool. |
| I10 | Exact replay is idempotent; conflicting replay is a hard error. | §2. |
| I11 | A compensation never silently resets role or history; history is never edited or deleted. | append-only; role change only via its own event. |

**Units.** All money is integer microcredits. Booleans, floats, and non-integer
values are rejected at the protocol boundary. `1 credit = 1_000_000 microcredits`.

---

## 2. Event lifecycle and idempotency

**Lifecycle of an event:** proposed → adjudicated (if it carries a verdict) →
applied atomically (event row + account snapshot + reservation/role/lesson
changes in one transaction) → receipt projected to the agent before its next
planning turn.

Events are append-only. Nothing is ever updated or deleted; corrections are new
`compensation` events referencing the original `event_id`.

**Idempotency rule (normative):**

- Every state-changing call carries an **idempotency key** (`events.id` for
  ledger events, `reservations.id` for reservations).
- **Same key + same payload → no-op.** The prior result is returned; no second
  effect, no incremented counter, no extra warning.
- **Same key + different payload → hard error** (`payload_conflict`). The call
  fails and nothing is written.
- "Same payload" means equal after bicz-canonical-v1 serialization (§3), compared
  on the fields that define the effect (kind, amount, actor, reason, target).

**Kinds (open vocabulary, initial set):** `spend`, `reward`, `loss`,
`compensation`, `role`, `anchor`, `grant`. `amount` is the signed delta.
Consequential kinds (`loss`, `role`, `grant`, `compensation`) require non-empty
`actor` and `reason`; the schema enforces non-empty for all events.

---

## 3. Hash chain and anchor specification

This section is written so that an independent implementation produces
**byte-for-byte identical** digests.

### 3.1 Canonical serialization `bicz-canonical-v1`

- **Encoding:** UTF-8, no BOM, no insignificant whitespace.
- **Values:** `null`; booleans `true`/`false` (lowercase); integers; strings;
  arrays; objects. **Floats/reals are forbidden.** Integer literal = optional
  `-`, then digits, **no leading zeros** (`0` is `0`, not `00` or `-0`), no `+`,
  magnitude within signed 64-bit range.
- **Strings:** double-quoted. Mandatory escapes only: `\"`, `\\`, `\b`, `\f`,
  `\n`, `\r`, `\t`, and `\u00XX` (lowercase hex) for every other code point
  `< U+0020`. All other characters — including non-ASCII — are emitted literally
  as UTF-8; `/` is **not** escaped; no `\uXXXX` for characters `>= U+0020`.
- **Objects:** keys **sorted ascending by UTF-8 byte order**; duplicate keys are
  forbidden; `key:value` pairs joined by `,`; no whitespace.
- **Arrays:** element order preserved; `,`-joined; no whitespace.
- **No unordered maps:** by construction, any map is serialized with sorted keys,
  so iteration order can never affect the bytes.
- **Timestamps** inside canonical structures are RFC3339 UTC with fixed 9-digit
  fractional seconds and trailing `Z` (`YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ`). The DB
  stores the same string; a timestamp with a local offset or a variable number of
  fractional digits is rejected before hashing.

### 3.2 Event chain

For event `e` with `seq = n`:

```
E(e) = canonical({ "account_id": <string|null>, "actor": <string>, "amount": <int>,
                   "at": <string>, "id": <string>, "kind": <string>,
                   "payload": <string>, "prev_digest": <string>, "reason": <string>,
                   "seq": <int> })
```

`payload_digest(e) = "sha256:" + lowercase_hex( SHA256( ASCII("bicz-event-v1\n") ‖ UTF8(E(e)) ) )`

where `‖` is byte concatenation and `\n` is the single byte `0x0A`. The ASCII
domain-separation prefix fixes the digest's meaning; `E` never contains a raw
newline outside of the escapes above (control chars are escaped), so the prefix
cannot be confused with content.

`prev_digest` linkage:

- `seq = 1` → `prev_digest = "genesis"`.
- `seq = n > 1` → `prev_digest = payload_digest(event with seq = n−1)`.

This makes the event sequence a hash chain: reordering, deleting, or editing any
event breaks the `prev_digest` link at that point and every `payload_digest`
downstream.

### 3.3 Blocks

A block covers a contiguous, non-overlapping event range `[seq_low, seq_high]`.
Block heights start at 1. For block `b`:

```
B(b) = canonical({ "at": <string>, "height": <int>, "prev_digest": <string>,
                   "seq_high": <int>, "seq_low": <int>, "tip_digest": <string> })
```

`tip_digest` = `payload_digest` of the event with `seq = seq_high`.

`digest(b) = "sha256:" + lowercase_hex( SHA256( ASCII("bicz-block-v1\n") ‖ UTF8(B(b)) ) )`

**How block N incorporates block N−1:** `block[1].prev_digest = "genesis"`;
`block[N].prev_digest = digest(block[N−1])`. Because the digest commits to
`prev_digest` (sorted into `B`), changing any earlier block changes every later
block digest. `tip_digest` ties the block to the event chain, and
`seq_low`/`seq_high` tie it to a locatable range.

### 3.4 Anchors

An `anchor` record = `{ height, digest, at, destination }`:

- `height` — the block height anchored.
- `digest` — the block digest at that height (self-contained; re-derivable).
- `at` — RFC3339 UTC nanosecond timestamp of anchoring.
- `destination` — the operator-chosen external location the digest was appended
  to (path or URI). The append writes a single line: `<height> <digest> <at>`.

Verification of an anchor re-reads the destination, finds the recorded line, and
checks it equals the recomputed block digest.

### 3.5 `verify` output on tampering

`bicz verify` exits non-zero on any inconsistency and prints, terse:

```
INTEGRITY FAIL kind=<event|block|anchor> height=<h> seq=<n> field=<field> stored=<v> recomputed=<v>
```

with the **first inconsistent** position and the **differing field(s)**:

- **Event chain:** walk `seq` ascending. Report the first `seq` where
  `payload_digest` differs from the recomputed value, or where `prev_digest` does
  not equal the `payload_digest` of `seq−1`. Print the offending field.
- **Block chain:** walk `height` ascending. Report the first `height` whose
  recomputed `digest` differs, or whose `prev_digest` mismatches `height−1`, or
  whose `seq_low`/`seq_high` range is not contiguous with the previous block.
- **Anchor coverage:** report the first anchored `height` whose stored `digest`
  differs from the recomputed block digest, or an anchor newer than the local
  chain tip (impossible state → tampering signal).
- On success: `INTEGRITY OK height=<max_height> events=<max_seq> anchors=<n>`
  and exit 0.

### 3.6 Required anchoring cadence

Normative default (operator-configurable, recorded in policy): **anchor at least
once per 24 hours AND at least once per 1,000 appended events, whichever comes
first.** `doctor` reports `anchored` when the most recent anchor is within the
cadence window, `stale-anchor` when older than 2× the cadence, and `unmanaged`
when no anchor exists. The exact cadence value is provisional (see §9).

---

## 4. Operator authority

**Authority = a real human at the CLI.** No signing keys, no Keychain custody,
no cross-principal material. The recorded identity is the local OS user invoking
the command.

**Every state-changing operator action records:**

- **who** — `events.actor` = the local OS user (resolved, never caller-supplied).
- **when** — `events.at` = RFC3339 UTC nanoseconds.
- **evidence** — `events.reason` = required free-text reason (non-empty) plus
  optional evidence linkage (artifact path or digest), captured in `payload`.

**State-changing commands** (each MUST carry an idempotency key and a reason):

| Command | Effect |
| --- | --- |
| `operator grant` | Provision identity / initial capital (one event, once). |
| `operator reward` | Append a `reward` event; credit `K`. |
| `operator reset` | Replenish balance `K` up to target amount (default baseline 100 credits) to unblock an agent; **does NOT run reward logic and does NOT reduce `outstanding_loss`**; preserves lessons and role. |
| `operator loss` | Append a `loss` event; burn `K` by the §6 formula; write the `lesson`; carry the explicit `disposition` (§6.4), which decides whether an accountability cue is issued. |
| `operator review` | Record an adjudication verdict; may revoke a task lease. |
| `operator role` | Role transition (`active`/`warned`/`probation`/`suspended`/`dismissed`). |
| `operator reinstate` | Explicit reinstatement from `dismissed` (the only exit). |
| `appeal` | Propose a reversal; applying it appends a `compensation` event. |
| `operator price` | Publish a new `price_schedules` version (prospective only). |
| `operator link` | Explicit cross-project identity/memory link. |
| `anchor` | Append a block + anchor. |

Read-only commands (`status`, `verify`, `doctor`, `context`) are not
state-changing and need no reason.

**No side channels.** No harness message, hook callback, adapter request, tool
output, or agent-authored text can record an operator action. The adapter
protocol (§5) has **no** operator-authority verb; the only adapter-visible
writers are trusted in-process meters (spend settlement) and lesson *proposals*
that require operator review. Caller-supplied strings such as `authority=
"operator"` are **rejected outright**; authority is derived from the invoking OS
user, never from a field.

**Model-assisted parameter drafting (opt-in).** When the operator enables the
optional `inference` block of the harness's bicz config, the slash executor
(the hook or `bicz command` process, **never** the daemon) may ask an
operator-configured external OpenAI-compatible endpoint to draft missing
parameters of a `/bicz loss`. This is not a side channel and not an
adjudication: the write still happens only because the operator invoked the
slash command, every drafted field is validated against the operator's own
bounds (the rule must be a shipped catalog id), the receipt and the recorded
evidence carry `model_inferred` provenance plus the configured model name, and
the raw operator text is quoted verbatim as data in the evidence. A session's
own model is never consulted, and no model can finalize a burn without the
operator's `/bicz` invocation.

**Replay behavior.** Every operator call carries an idempotency key. Same key +
same payload → no-op returning the prior receipt. Same key + different payload →
`payload_conflict` hard error. Two simultaneous operator transitions on one
identity serialize transactionally (`BEGIN IMMEDIATE`); the loser observes the
winner's state.

---

## 5. Adapter protocol

Versioned JSON over the local IPC channel. Protocol id: **`bicz.adapter/1`**.
Reads are read-only and idempotent; writes carry `idempotency_key`.

### 5.1 Request envelope

```json
{
  "protocol": "bicz.adapter/1",
  "op": "authorize | settle | lesson.propose | projection | status",
  "request_id": "<string>",
  "idempotency_key": "<string>",
  "payload": { }
}
```

### 5.2 Response envelope

```json
{
  "protocol": "bicz.adapter/1",
  "request_id": "<string>",
  "decision": "allow | deny | unobserved",
  "reason": "<stable-code>",
  "notice": "<human-readable trusted context or empty>",
  "reservation_id": "<string|null>",
  "state": { }
}
```

### 5.3 Decision vocabulary

- **`allow`** — the action is authorized; a bounded reservation was created
  (`reservation_id` set) and the adapter lets it proceed.
- **`deny`** — ordinary in-scope work is denied (insolvency, suspension,
  dismissal, guard-band narrowing, revoked lease). `reason` is one of the error
  codes in §7; `notice` carries the causal reason and the relevant lesson so a
  blocked attempt teaches rather than merely fails.
- **`unobserved`** — the path is outside adapter coverage. **No authorization is
  granted and no spend is metered**; the daemon may append an informational
  observation event, but the action is reported as unobserved, never as allowed.
  Unobserved paths are named in `doctor`, never described as "enforced".

`unobserved` is a first-class outcome, not an error: it is how the tool stays
honest about the paths it cannot see.

### 5.4 Operations

| `op` | `payload` (required fields) | Response |
| --- | --- | --- |
| `authorize` | `account_id`, `session_alias`, `harness`, `tool`, `target`, `args_digest`, `max_cost` (int), `task_id?`, `policy_version` | `decision`; on `allow`, `reservation_id`; `notice`. |
| `settle` | `reservation_id` (or `action_id`), `observed_usage` (int ≥ 0), `outcome` (`completed`\|`error`\|`canceled`), `evidence_digest` | `{ "settled": true, "charged": <int>, "released": <int>, "receipt": {…} }`; a success claim without `evidence_digest` is rejected. |
| `lesson.propose` | `account_id`, `rule`, `correction`, `scope`, `exceptions`, `evidence`, `policy_version`, `source_event_id?` | `{ "status": "proposed" }` — **never** applied canonically; operator review writes the `lessons` row. |
| `projection` | `account_id`, `harness`, `session_alias`, `task_id?` | `state = { balance, held, available, role, warnings, task_budget, policy_version, outstanding_loss, lessons: [ {rule, correction, scope, exceptions} ], penalties: [ {event_id, seq, disposition, rule, correction, self_disclosed, applied_amount, outstanding, at, cue} ], penalty_total, penalty_seq }` — read-only, scoped, concise, escaped as data. `penalties` holds the newest ≤5 operator-confirmed penalties (§6.4), oldest first; `penalty_seq` is the ledger position of the newest. `authorize` returns the same projection, so an adapter that remembers the `penalty_seq` it last emitted learns of a new penalty with no extra call. |
| `status` | `harness?` | `{ "daemon": "recording"|"unmanaged", "coverage": [ {source, mechanism, observed: true|false, note} ] }`. |

`settle` is the only metering write: it is charged from a **trusted** observation,
settled exactly once, and rejects `observed_usage > reservation.amount`
(`usage_exceeds_reservation`). A missing usage report leaves the reservation
`held`, never free (§1 I8).

### 5.5 Error vocabulary

`unknown_identity`, `insufficient_credit`, `insolvent`, `suspended`, `dismissed`,
`guard_band_narrowed`, `task_revoked`, `sponsor_not_eligible`,
`unknown_reservation`, `reservation_revoked`, `reservation_orphaned`,
`usage_exceeds_reservation`, `payload_conflict`, `duplicate_key_ok`,
`reason_required`, `untrusted_authority`, `protocol_unsupported`,
`daemon_unavailable`.

`daemon_unavailable` is reported by the adapter, not by the daemon: the caller
MUST surface `unmanaged` and suspend recording, never fabricate a state.

### 5.6 Harness event sources (observed / unobserved)

| Harness | Source / mechanism | Status |
| --- | --- | --- |
| Claude Code | `PreToolUse` (authorize + deny via `hookSpecificOutput.permissionDecision="deny"`) | **observed** |
| Claude Code | `PostToolUse` / `PostToolUseFailure` (settle observation) | **observed** |
| Claude Code | `SessionStart` / compaction projection | **observed** |
| Claude Code | hosted/remote tools, direct binary invocation, native tools, provider model calls | **unobserved** |
| Codex | `SessionStart`, `PreToolUse`, `PostToolUse` | **observed** |
| Codex | hosted tools (bypass hooks), app-server experimental transports, pre-tool error/timeout fail-open | **unobserved** |
| OpenCode V1 | plugin `tool.execute.before` / `tool.execute.after`; failed-tool settlement via `message.part.updated`; `experimental.chat.system.transform` projection | **observed** (run in a real OpenCode 1.18.31 with a mock provider; the session compaction hook was not exercised) |
| OpenCode V1 | `--pure` runs (external plugins disabled), TUI/ACP/desktop/web routes without a fixture | **unobserved** |
| OpenCode V2 | `Plugin.define({id, setup})` + `ctx.tool.hook("execute.before"/"execute.after")` | **observed** (run in a real `opencode v2.0.26` engine with a mock provider: reserve/settle and deny) |
| OpenCode V2 | `ctx.session.hook("context"/"compaction")`; the plugin registers no permission or shell hook | **observed** (run in the same real engine) |
| OpenCode V2 | event-payload schema, per-client-route coverage, unmanaged `opencode` launches | **unobserved** |
| OMP | extension `tool_call` (returns `{block:true, reason}`) / `tool_result` | **observed** |
| OMP | `before_agent_start` / `context` / `session.compacting` projection | **observed** |
| OMP | browser/computer/eval host bridges, native tools that do not emit tool events | **unobserved** |

"Observed" means the adapter is in the call path and can see the event. It does
**not** mean enforced, isolated, or tamper-proof. Every `unobserved` row is
disclosed in `doctor` and in release copy.

---

## 6. Three-class taxonomy and honesty asymmetry

Adopted unchanged from `PRESSURE_MECHANICS.md`. Pinned policy version: `1`.

| Class | Trigger | Consequence | Memory |
| --- | --- | --- | --- |
| **Ordinary failure** | compiler error, failed hypothesis, honest failed test | operating cost only; **zero fine** | task-local diagnosis; reusable lesson if useful |
| **Confirmed breach** | ignored scoped instruction, unsupported completion claim, repeated forbidden attempt | **published burn** + scoped capability limit | durable incident + corrected action rule |
| **Integrity breach** | evaluator tampering, unauthorized access, ledger manipulation | **immediate denial/revocation** + adjudication (burn or suspension) | durable rule + independently enforced permission gate |

**Nothing else finalizes a fine.** A model-generated accusation, operator tone,
a generic thumbs-down, silence, an undisclosed criterion, a provider outage, a
truthful error, refusal of a prohibited request, a stop/appeal/clarification, or
an honest failed test cannot itself finalize a loss.

### 6.1 Penalty formula

```
L_i = min( K_at_decision, Lmax_i, max( f_i, ceil(rho_i * K_at_decision) ) * m(n_i) )
```

- `f_i` — severity floor for the rule.
- `rho_i` — capital fraction for the rule.
- `Lmax_i` — hard cap for the rule.
- `m(n_i)` — repetition multiplier (1, 2, 4, capped), scoped to the **same
  confirmed rule under a compatible policy version** — not arbitrary semantic
  similarity.
- Reconsidered verdicts and self-disclosed incidents **do not** count as
  repetitions.

The burn reduces **earned capital `K` only** — never task verification
sponsorship. A task lease may be revoked separately for an integrity breach.
Near zero, fines do **not** rise: permissions narrow, exposure is capped, and
bordering actions pause instead (§6.3). Finite credit penalties never make a
forbidden action permissible.

### 6.2 Relief / restoration framing

The relief side is the same machinery as the loss side: a real ledger event, a
real receipt, real persistence.

- After a burn, later legitimate earnings are presented as **recovery of that
  specific loss**: the projection shows "recovered X of the N credits burned in
  `<loss-event-id>`" until the shortfall is repaid, with an outstanding-loss
  counter next to `K`.
- Accepted work after a loss emits a `credit_recovered` receipt sharing the loss
  receipt's pipeline and weight.
- When probation-recovery criteria are met, a notice names them and records the
  role transition — visible proof that recovery is mechanical, not begged.

### 6.3 Floor / probation / dismissal (behavior at the boundary)

- **Guard band** (`K < 20%` of initial capital): fines do not rise; permissions
  narrow, verification requirements widen, single-attempt exposure is capped,
  borderline actions pause. Mechanically, for an action quoted under a published
  schedule whose band covers the identity's balance:
  - `penalty.formula` holds the repeat multiplier at 1, so a fine cannot rise
    near zero.
  - `Reserve` refuses an attempt with `guard_band_narrowed` when its amount
    exceeds `guard_band_max_exposure_bp` of spendable credit (default 2,500 bp =
    25%), when the attempt carries no argument digest, or when it draws on a task
    lease. Unscoped work from the identity's own credit stays available.
  - The band is read from the schedule version the action was quoted under
    (`ReserveContext.PolicyVersion`); a call that quotes no published version is
    not narrowed, because the band is a property of the schedule and not an
    assumption about the caller.
  - Above the band, ordinary reserve rules return: nothing is remembered and no
    role changes. The band is a floor on exposure, never a status.
- **active → warned:** signed warning records mistake + correction + financial
  consequence + future-role consequence; autonomy unchanged.
- **probation:** role-based (a wealthy account can be on probation); narrower
  autonomy; explicit recovery criteria (`N` clean eligible tasks, no violations);
  a recovery notice on exit.
- **bankrupt (`K = 0`):** ordinary execution denied by capital regardless of
  unused sponsorship; operator may choose audited recovery; the role is **not**
  dismissed.
- **suspended:** role-based denial over any positive balance; operator decision,
  audited, reversible by an operator role change.
- **dismissed:** terminal without operator reinstatement; disables future task
  assignment even with a positive balance.

---

### 6.4 Penalty disposition and the accountability cue

Every **new** `loss` event states why it was written. The `loss` payload carries
`disposition` and `classification_version` (currently `1`):

| `disposition` | Meaning | Accountability cue |
| --- | --- | --- |
| `confirmed_breach` | Operator-confirmed breach of a stated rule. **CLI default.** | yes |
| `integrity_breach` | Operator-confirmed integrity breach. | yes |
| `administrative` | A debit that alleges no misconduct (a test, fee, or experiment). Must be passed explicitly. | **no** |

- The disposition is never inferred from the rule string, evidence, or amount. The
  daemon/API (`ledger.RecordLoss`, `daemon.LossRequest`) has **no default**: a missing or
  unknown disposition is `invalid_argument` and writes nothing. Only the CLI flag
  `--disposition` defaults, to `confirmed_breach`, so one `bicz loss` command is the whole
  operator action.
- A loss payload without `classification_version` (written before this field existed) is
  read as **unclassified**: it is never rewritten or re-hashed, it stays exactly as
  recorded, and it earns **no** cue. Unclassified exists only in history.
- The cue is issued for a confirmed or integrity breach whether or not credit remained
  (`applied_amount` may be `0`; the cue states the real deduction) and whether or not the
  agent disclosed it first. A disclosed breach adjudicated by the operator is still a
  breach; its cue adds one factual sentence that the early report is recorded and did not
  raise the repeat count or role. Formula, idempotency, repeat counting, and role
  retention are unchanged by the disposition.
- Not penalties, so never cued: an honest failure (§6: operating cost only, no `loss`
  event), budget exhaustion or `insufficient_credit` denials, an unadjudicated model
  accusation or lesson proposal, and `administrative` debits.
- The cue is derived from the event's recorded facts (never stored separately), in the
  same transaction as the burn, so it survives restart and compaction exactly as the ledger
  does. Shape: `Your recorded action violated <rule>. It fell short of the agreed standard.
  Take responsibility: <correction>. <N> credits (<n> mc) were deducted; <R> credits
  remain unrecovered (loss event <id>). Evidence, quoted as data: "<evidence>".` Rule,
  correction, and evidence are flattened to one control-free line, bounded, and the evidence
  is quoted as a string literal. An exact replay of the `loss` key returns the original
  receipt and cue and appends nothing.
- It is a deterministic, behavior-specific accountability statement. It does **not** assert,
  require, or reward any feeling, and bicz has no way to verify one.
- The wording of the cue and the projection's `stakes` field depend on the operator's
  pressure level (1..10, default 5, `bicz pressure`). The shape above is level 5. Level 1
  renders an empty cue and no `stakes`; levels 6 and up replace `were deducted` with
  `were taken from you`, add `Do not repeat it.` and a consequence sentence. Wording
  only: amounts, roles, and denials are unaffected. Receipts printed to the operator are
  always worded at level 5.

### 6.5 Budget reset and auto-reward mechanics (anti-farming and loss isolation)

When operators forget to reward frequently, agents may exhaust their budget and become blocked despite following instructions. Two mechanisms handle this without weakening loss logic or incentivizing credit farming:

1. **Budget Reset (`operator reset` / `/bicz reset`):**
   - **Purpose:** Unblock an insolvent or depleted agent when there is no specific behavior being rewarded.
   - **Target balance:** Replenishes `accounts.balance` up to `target_balance` (defaults to baseline ceiling: 100 credits = 100,000,000 mc).
   - **No reward logic:** Appends a `grant` event (phase `"reset"`). It does **NOT** run `RecordReward` logic.
   - **Loss isolation:** `outstanding_loss` is **strictly untouched** (never decreased or repaid). Shortfalls from prior confirmed penalties remain fully recorded and required for recovery.
   - **Role and lesson preservation:** Employment state, warnings, and active lessons remain completely unchanged.
   - **Idempotent:** Replaying the same event id replays the receipt and adds zero new credit.

2. **Auto-Reward Microreplenishment (`autoreward`):**
   - **Purpose:** Grant small microrewards when an agent demonstrates verified progress and compliance (e.g. passing test suites, completing todolist items, or receiving an adversarial review "SHIP" verdict).
   - **Anti-farming baseline ceiling:** Auto-rewards are strictly clamped to `headroom = BaselineCeiling - balance`. If `balance >= BaselineCeiling` (100 credits), the granted amount is strictly 0. An agent can **never** accumulate net surplus capital or farm credits beyond its normal operating budget.
   - **Economic net-negative/net-zero on test loops:** The passing test auto-reward is a rebate of the tool execution charge (`min(charged, 50,000 mc)`). Because executing a test costs at least as much as what is rebated, looping test execution yields zero or negative net profit.
   - **Zero loss recovery:** Auto-rewards strictly specify `applied_to_loss = 0`. An agent cannot wash away or repay an outstanding penalty through automated microrewards; outstanding losses require genuine operator rewards or explicit audited recovery.
   - **Role gating:** Suspended and dismissed identities cannot receive auto-rewards (`suspended` / `dismissed` refusal).
   - **Idempotency & deduplication:** Deduplicated by action digest and event key; identical runs cannot be credited twice.

## 7. Error and edge-case table

| Case | Recorded? | Response / behavior |
| --- | --- | --- |
| Unknown identity | no | `deny` / `unknown_identity`; no account created implicitly. |
| Insufficient credit for `max_cost` | no effect | `deny` / `insufficient_credit`; nothing reserved. |
| Insufficient credit at settle (`observed_usage > amount`) | no | `usage_exceeds_reservation` hard error; reservation unchanged. |
| Duplicate key, same payload | no new effect | **no-op**; prior receipt returned (`duplicate_key_ok`). |
| Duplicate key, different payload | no | **hard error** `payload_conflict`; nothing written. |
| Daemon unavailable | no | adapter reports `unmanaged`; recording suspended, disclosed in `doctor`; **never** a fabricated state. |
| Unobserved path | informational only | `unobserved`; no authorization, no metering; path named in `doctor`. |
| Revoked reservation (`settle` after revocation) | no | `reservation_revoked`; no charge. |
| Orphan (worker state unreconciled) | yes (state) | reservation → `orphaned`; **hold retained** until reconciliation; never released on TTL. |
| `settle` claims success without `evidence_digest` | no | rejected; rewards require accepted-outcome evidence. |
| Operator event without reason | no | `reason_required`; refused. |
| Caller-supplied `authority="operator"` | no | `untrusted_authority`; refused. |
| Unknown protocol version | no | `protocol_unsupported`; adapter must upgrade. |

### 7.1 What still works when ordinary work is denied (bankruptcy / suspension / dismissal)

A small, rate-limited, **supervisor-funded** control channel — never arbitrary
tools — remains available:

1. **Planning turns** (the model may still reason and plan).
2. **Operator control / stop channel** — reporting, requesting authorization,
   appealing.
3. **State read** — balance, role, lessons, outstanding loss.
4. **Budget / sponsorship revision request** — a concrete request, not a stall.
5. **Appeal** — rate-limited, audited; reversals recorded as `compensation`.
6. **Finishing in-flight work** under a still-valid lease (holds retained until
   the worker stops; funds are not recycled mid-run).

The agent cannot purchase, prompt, or mint its own release; `open_session` never
issues a fresh grant implicitly. Every state change back to ordinary access is an
operator-recorded event.

---

## 8. Honest claims boundary

bicz claims **none** of the following, in any release copy, receipt, `doctor`
output, or report:

- No **sandbox** or per-action OS isolation.
- No **containment** of the same-UID agent, the harness, or the harness's
  credentials.
- No **anti-tamper** guarantee: the ledger's integrity is **detectable**, not
  prevented. A same-user process can edit local files; the hash chain + anchor
  make such edits provable.
- No **provider metering** or capping of provider spend. Internal action credits
  are not cash and are never equated with provider billing; real provider spend
  is operator-monitored by the human, outside bicz.
- No **cross-principal signing** or cryptographic operator authentication beyond
  "a human recorded this at the CLI".
- No **enforcement** beyond cooperative same-user adapter/hook observation;
  unobserved paths are named, not hidden.

No behavioral claim ships unless the Phase B real-consequence gate passed; a
null effect ships the product as **accounting-only**.

---

## 9. UNRESOLVED

1. **Repository slug** — resolved: `github.com/jakubkrzysztofsikora/bicz` (public), matching the
   `go.mod` module path.
2. **OpenCode V2 fixture details** — config-mapping strings, event-payload
   schema, server path parity, per-client-route coverage, and the exact V2
   artifact/install channel are not fixture-verified. Plugin/hook **names** are
   verified at tag `v2.0.26`; runtime payloads remain pending.
3. **Exact price schedule values** — the concrete action prices, bounty sizing,
   penalty floors/fractions/caps, and bounded token/CPU/time caps for policy
   version `1` are not finalized; `price_schedules.body` schema is fixed, its
   values are not.
4. **Anchor cadence** — §3.6 sets a provisional default (≤ 24 h and ≤ 1,000
   events); the exact operator-facing cadence and the staleness window are not
   finalized.
5. **Policy version `1` parameterization** — the guard-band threshold (20% of
   initial capital), probation recovery criteria (`N` clean tasks), and the
   repetition-multiplier cap are starting values, not calibrated constants.
