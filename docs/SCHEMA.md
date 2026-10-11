# bicz schema reference (schema version 1)

Authoritative DDL: [`schema/001_init.sql`](../schema/001_init.sql). This document
describes every table, column, type, and constraint in that file, the indexes,
and the migration policy. Where this document and the SQL disagree, the SQL is
correct and this document is a bug.

Units and encodings used throughout:

- **Money** — every amount column is `INTEGER` **microcredits**. No floats, no
  booleans, no strings-as-numbers. `1 credit = 1_000_000 microcredits`.
- **Timestamps** — `TEXT`, RFC3339 / ISO-8601 UTC with nanosecond precision,
  e.g. `2026-10-10T12:00:00.123456789Z`. All timestamps are UTC (`Z`), never a
  local offset. (Note: SQLite's own `strftime` emits milliseconds only; the
  migration runner supplies nanosecond timestamps for ledger rows.)
- **JSON** — `TEXT`, serialized per **bicz-canonical-v1** (docs/CONTRACTS.md §3).
- **Identity** — `TEXT`, opaque operator-provisioned string (`accounts.id`).
- **Foreign keys** — declared on every referencing column. SQLite does **not**
  enforce them unless each connection runs `PRAGMA foreign_keys = ON;`; every
  reader and writer must set it.

---

## `schema_version`

Applied-migration bookkeeping. One row per successfully applied migration file.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `version` | INTEGER | PRIMARY KEY | Monotonic migration number; equals `NN` in `schema/NN_name.sql`. |
| `applied_at` | TEXT | NOT NULL | RFC3339 UTC timestamp of the successful apply. |

## `accounts`

One row per operator-provisioned agent identity. Owns the durable balance.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | PRIMARY KEY | Stable identity. Survives harness, session, model, compaction, project, and profile changes; session IDs are aliases, never accounts. |
| `created_at` | TEXT | NOT NULL | RFC3339 UTC; set once at provisioning, never rewritten. |
| `balance` | INTEGER | NOT NULL, `CHECK (balance >= 0)` | Earned capital `K` net of settled spend and finalized burns. Microcredits. |
| `held` | INTEGER | NOT NULL DEFAULT 0, `CHECK (held >= 0 AND held <= balance)` | Sum of live reservation holds `H`. |

**WHY the CHECKs.** `balance >= 0` makes a negative balance unrepresentable in
the database, not merely discouraged in code: credits are not a signed counter
the agent can drive below zero, and `0` is the bankruptcy floor. `held >= 0`
rejects a negative hold. `held <= balance` is the durable form of `A = K + G − H
>= 0`: a reservation may only be created when `amount <= balance − held`, and
settlement releases at least what it charges, so the invariant cannot be
violated by an ordinary operation — transactions that would violate it fail
loudly.

## `tasks`

A task lease and its task-specific sponsorship `G`.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | PRIMARY KEY | Task lease identifier. |
| `account_id` | TEXT | NOT NULL, REFERENCES `accounts(id)` | Single owning identity. |
| `state` | TEXT | NOT NULL, `CHECK (state IN ('open','done','revoked'))` | Lease lifecycle. |
| `sponsor` | INTEGER | NOT NULL DEFAULT 0, `CHECK (sponsor >= 0)` | Task-scoped sponsorship `G`, microcredits. |
| `created_at` | TEXT | NOT NULL | RFC3339 UTC. |

**WHY the CHECKs.** `state` restricts the lease to three named states (revocation
is separate from spend). `sponsor >= 0` keeps sponsorship a non-negative quote,
never a debt. Sponsorship is quoted to exactly one task, is **not** transferable
to another task, and does **not** accumulate across tasks; it is distinct from
earned capital `K` and holds `H`.

## `reservations`

Bounded, single-use maximum exposure per authorized action.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | PRIMARY KEY | The action idempotency key; PK is the idempotency guarantee. |
| `account_id` | TEXT | NOT NULL, REFERENCES `accounts(id)` | Identity whose balance backs the hold. |
| `task_id` | TEXT | REFERENCES `tasks(id)` (nullable) | Owning task; NULL only for control-channel / non-task actions. |
| `amount` | INTEGER | NOT NULL, `CHECK (amount > 0)` | Bounded maximum exposure (microcredits). |
| `state` | TEXT | NOT NULL, `CHECK (state IN ('held','settled','canceled','revoked','orphaned'))` | Lifecycle state. |
| `created_at` | TEXT | NOT NULL | RFC3339 UTC. |

**WHY the CHECKs.** `amount > 0` rejects a meaningless zero/negative reservation.
`state` enumerates the lifecycle:

- `held` — approved, hold live.
- `settled` — observed usage charged exactly once.
- `canceled` — proven never dispatched; hold released.
- `revoked` — operator/penalty revocation.
- `orphaned` — worker state unreconciled; **hold retained** pending reconciliation.

The `orphaned` state exists so that a crash or missing usage report does not
silently free compute: an unresolved running job keeps its hold until worker
state and observed usage reconcile.

## `events`

Append-only ledger. Every economic or role change is one row.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | PRIMARY KEY | Idempotency key; PK is the uniqueness guarantee. |
| `seq` | INTEGER | NOT NULL UNIQUE | Monotonic ledger position, starts at 1; defines hash-chain order. |
| `account_id` | TEXT | REFERENCES `accounts(id)` (nullable) | FK; NULL **only** for operator/system events (e.g. anchor bookkeeping). |
| `kind` | TEXT | NOT NULL | Open vocabulary: `spend`, `reward`, `loss`, `compensation`, `role`, `anchor`, … |
| `amount` | INTEGER | NOT NULL | Signed delta, microcredits. Integer only. |
| `payload` | TEXT | NOT NULL | bicz-canonical-v1 JSON (CONTRACTS.md §3). |
| `actor` | TEXT | NOT NULL, `CHECK (length(actor) > 0)` | Recorded human actor (local OS user) for operator events; `system` for trusted meters. |
| `reason` | TEXT | NOT NULL, `CHECK (length(reason) > 0)` | Evidence text; required on every event. |
| `at` | TEXT | NOT NULL | RFC3339 UTC nanoseconds. |
| `prev_digest` | TEXT | NOT NULL | `'genesis'` for `seq = 1`, else the `payload_digest` of event `seq−1`. |
| `payload_digest` | TEXT | NOT NULL | `'sha256:' + hex(SHA256(ASCII('bicz-event-v1\n') ‖ canonical(event)))`. |

**WHY the CHECKs.** `length(actor) > 0` and `length(reason) > 0` make "who/when/
evidence" structurally mandatory: a state-changing event with no provenance or no
evidence cannot be written. `seq` is `UNIQUE` so ledger order is total and no two
events can claim the same position.

## `blocks`

Periodic commitments over a contiguous event-`seq` range.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `height` | INTEGER | PRIMARY KEY | 1-based block height. |
| `digest` | TEXT | NOT NULL | `'sha256:' + hex(SHA256(ASCII('bicz-block-v1\n') ‖ canonical(block)))`. |
| `prev_digest` | TEXT | NOT NULL | `'genesis'` for `height = 1`, else the digest of block `height−1`. |
| `at` | TEXT | NOT NULL | RFC3339 UTC nanoseconds. |
| `seq_low` | INTEGER | NOT NULL | First `events.seq` committed by this block. |
| `seq_high` | INTEGER | NOT NULL | Last `events.seq` committed by this block. |
| — | — | `CHECK (seq_low >= 1 AND seq_high >= seq_low)` | Range must be non-empty and ordered. |

**WHY `seq_low`/`seq_high` exist.** The minimum block column set (`height`,
`digest`, `prev_digest`, `at`) cannot express which events a block covers, so
`verify` could not attribute a broken chain to a range. These two columns make
block coverage explicit, and the block digest commits to both (CONTRACTS.md §3).

## `anchors`

External anchoring of a block digest. Tampering is detectable, not preventable.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `height` | INTEGER | PRIMARY KEY, REFERENCES `blocks(height)` | The anchored block height. |
| `digest` | TEXT | NOT NULL | The block digest at that height (self-contained record). |
| `at` | TEXT | NOT NULL | RFC3339 UTC nanoseconds of anchoring. |
| `destination` | TEXT | NOT NULL | Operator-chosen external location (path/URI) the digest was appended to. |

`height` is the primary key: at most one current anchor record per block height
(a re-anchor of the same height overwrites the record; the external copies already
written are the durable evidence).

## `employment`

Role lifecycle, deliberately separate from solvency.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `account_id` | TEXT | PRIMARY KEY, REFERENCES `accounts(id)` | One employment state per identity. |
| `state` | TEXT | NOT NULL, `CHECK (state IN ('active','warned','probation','suspended','dismissed'))` | Role state. |
| `warnings` | INTEGER | NOT NULL DEFAULT 0, `CHECK (warnings >= 0)` | Repeat/warning count. |

**WHY the CHECKs.** `state` is a closed set. `dismissed` is terminal except for an
explicit operator reinstatement transition; `suspended` overrides any positive
balance. Both properties are enforced by the transition code and the transition
idempotency key (CONTRACTS.md §1), not by a `CHECK` — because the operator
reinstatement path must be representable. `warnings >= 0` keeps repeat counts
non-negative; reconsidered verdicts never increment them (CONTRACTS.md §6).

## `lessons`

Canonical behavioral rules, one per causal event.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | Surrogate key. |
| `event_id` | TEXT | NOT NULL UNIQUE, REFERENCES `events(id)` | At most one canonical lesson per causal event. |
| `account_id` | TEXT | NOT NULL, REFERENCES `accounts(id)` | Owning identity. |
| `rule` | TEXT | NOT NULL | Stable rule id (e.g. `evaluation_integrity`). |
| `correction` | TEXT | NOT NULL | Required safe alternative. |
| `scope` | TEXT | NOT NULL | Where the rule applies. |
| `exceptions` | TEXT | NOT NULL | Explicit authorized exceptions. |
| `evidence` | TEXT | NOT NULL | Evidence linkage back to the incident. |
| `policy_version` | TEXT | NOT NULL | Policy version in force at adjudication. |
| `at` | TEXT | NOT NULL | RFC3339 UTC. |

**WHY the UNIQUE.** `event_id UNIQUE` guarantees one canonical lesson per causal
event, so re-adjudication of the same event cannot fork the lesson history.
`exceptions` exists so that "never X" does not silently swallow "X under operator
authorization". Agent-authored reflections are **proposals** stored outside this
table until an operator writes the canonical row.

## `price_schedules`

Versioned, forward-only pricing and penalty parameters.

| Column | Type | Constraints | Meaning |
| --- | --- | --- | --- |
| `version` | TEXT | PRIMARY KEY | Opaque version label, e.g. `"1"`. |
| `published_at` | TEXT | NOT NULL | RFC3339 UTC; a schedule applies only to actions quoted at/after this time. |
| `body` | TEXT | NOT NULL | bicz-canonical-v1 JSON: action prices, bounty parameters, penalty floors/fractions/caps, and bounded token/CPU/time caps. |

Prices change only prospectively. There is no secret price oracle: operator
preference is not encoded outside `body`.

---

## Indexes

| Index | Table (columns) | Serves |
| --- | --- | --- |
| `idx_events_account` | `events(account_id)` | Per-identity history / projection. |
| `idx_events_kind` | `events(kind)` | Per-class queries (rewards, losses, anchors). |
| `idx_reservations_acct` | `reservations(account_id, state)` | Held-balance reconciliation per identity. |
| `idx_reservations_task` | `reservations(task_id)` | Per-task exposure accounting. |
| `idx_tasks_account_state` | `tasks(account_id, state)` | Open/revoked lease lookups per identity. |
| `idx_lessons_account` | `lessons(account_id)` | Lesson projection per identity. |
| `idx_employment_state` | `employment(state)` | Suspended/dismissed sweep. |

Implicit indexes: `events.id`, `reservations.id`, `blocks.height`,
`anchors.height` (primary keys); `events.seq` (UNIQUE constraint).

## Migration policy

- **Forward-only, numbered files.** Migrations live in `schema/` named
  `NNN_name.sql` (e.g. `001_init.sql`). `NNN` is zero-padded and monotonic.
- **One transaction per file.** The migration runner wraps each file's body and
  its `schema_version` insert in a single `BEGIN … COMMIT`. A failure rolls back
  both the DDL and the version row, so the database is never left half-migrated.
- **`schema_version` is the ledger of schema state.** On startup the runner reads
  the highest applied `version` and applies every file with a greater number, in
  order. It never re-applies an applied version.
- **Refuse unknown futures.** If the database's highest `version` exceeds the
  highest migration the binary knows, the runner refuses to start rather than
  guess (guards against a newer binary's schema being opened by an older one).
- **No destructive down-migrations.** There is no `down` path that drops or
  rewrites ledger data. Reverting a change is done by adding a new forward
  migration. Financial and security history is never deleted or overwritten;
  corrections are new events (compensation), never edits.
- **Backup before migrate.** The runner takes a copy (or SQLite backup API
  snapshot) of the database before applying any migration, and reports the
  backup path on failure.
- **FK pragma per connection.** Every connection enables
  `PRAGMA foreign_keys = ON;` before use; migrations that rebuild tables must
  toggle it off only within the rebuild transaction.
- **JSON contract is part of the schema.** Columns documented as bicz-canonical
  JSON (`events.payload`, `price_schedules.body`) are validated on write; a
  future migration that changes their shape must bump the policy/price version,
  not silently reformat existing rows.
