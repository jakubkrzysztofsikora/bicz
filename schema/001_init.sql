-- =============================================================================
-- bicz — schema version 1 (migration 001_init)
--
-- Forward-only. This file is applied exactly once, inside a single
-- transaction, by the migration runner (see docs/SCHEMA.md "Migration policy").
--
-- Units: every money column is an INTEGER count of microcredits. Booleans,
-- floats, and non-integer values are rejected by the contract (see
-- docs/CONTRACTS.md §2). Timestamps are RFC3339 UTC with nanosecond precision,
-- e.g. 2026-10-10T12:00:00.123456789Z.
--
-- Integrity model: the ledger is append-only at the application level and
-- chained with SHA-256 (events.payload_digest / events.prev_digest and
-- blocks.digest / blocks.prev_digest). A periodic external anchor records a
-- block digest outside the database. Tampering is therefore DETECTABLE, not
-- preventable — this schema does not and cannot enforce append-only at the
-- storage layer.
--
-- Idempotency: an event is uniquely identified by events.id (PRIMARY KEY) and
-- a reservation by reservations.id (PRIMARY KEY). Those primary keys are the
-- idempotency guarantee: a retried call carrying the same key is a no-op when
-- its payload matches and a hard error when it does not.
-- =============================================================================

-- Enforce REFERENCES clauses for this connection. SQLite defaults FKs off and
-- this pragma is per-connection, so every reader/writer must set it too.
PRAGMA foreign_keys = ON;

-- -----------------------------------------------------------------------------
-- schema_version — applied-migration bookkeeping.
-- -----------------------------------------------------------------------------
CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,   -- monotonic migration number; equals NN in schema/NN_*.sql
    applied_at TEXT    NOT NULL       -- RFC3339 UTC timestamp of the successful apply
);

-- -----------------------------------------------------------------------------
-- accounts — one row per operator-provisioned agent identity.
-- -----------------------------------------------------------------------------
CREATE TABLE accounts (
    id         TEXT    PRIMARY KEY,   -- stable identity; survives harness, session, model, compaction, project, and profile changes
    created_at TEXT    NOT NULL,      -- RFC3339 UTC; set once at provisioning, never rewritten
    balance    INTEGER NOT NULL       -- integer microcredits; earned capital K net of settled spend and finalized burns
               CHECK (balance >= 0),  -- CHECK: credits are not a signed counter the agent may drive negative; 0 is the bankruptcy floor
    held       INTEGER NOT NULL DEFAULT 0
               -- CHECK: holds may never be negative, and total active holds may never exceed the balance.
               -- This is the durable form of A = K + G - H >= 0: a reservation may only be created when
               -- amount <= balance - held, and settlement releases >= what it charges.
               CHECK (held >= 0 AND held <= balance)
);

-- -----------------------------------------------------------------------------
-- tasks — a task lease and its task-specific sponsorship G.
-- -----------------------------------------------------------------------------
CREATE TABLE tasks (
    id         TEXT    PRIMARY KEY,
    account_id TEXT    NOT NULL REFERENCES accounts(id),  -- FK: the single owning identity
    state      TEXT    NOT NULL
               CHECK (state IN ('open','done','revoked')),  -- CHECK: task lease lifecycle; revocation is separate from spend
    sponsor    INTEGER NOT NULL DEFAULT 0
               CHECK (sponsor >= 0),  -- CHECK: sponsorship is a non-negative quoted amount, never a debt
    created_at TEXT    NOT NULL
);
-- NOTE: sponsorship is quoted to exactly one task (this row), cannot be
-- transferred to another task, and does not accumulate across tasks. It is
-- distinct from earned capital K and from holds H.

-- -----------------------------------------------------------------------------
-- reservations — bounded, single-use maximum exposure per authorized action.
-- -----------------------------------------------------------------------------
CREATE TABLE reservations (
    id         TEXT    PRIMARY KEY,   -- == the action idempotency key; PK is the idempotency guarantee
    account_id TEXT    NOT NULL REFERENCES accounts(id),  -- FK: identity whose balance backs the hold
    task_id    TEXT    REFERENCES tasks(id),  -- FK, nullable: NULL only for control-channel / non-task actions
    amount     INTEGER NOT NULL
               CHECK (amount > 0),     -- CHECK: a reservation is a positive bounded maximum; zero/negative is meaningless
    state      TEXT    NOT NULL
               CHECK (state IN ('held','settled','canceled','revoked','orphaned')),
               -- CHECK: exactly one lifecycle state.
               --   held      — approved, hold live
               --   settled   — observed usage charged exactly once
               --   canceled  — proven never dispatched; hold released
               --   revoked   — operator/penalty revocation
               --   orphaned  — worker state unreconciled; hold retained pending reconciliation
    created_at TEXT    NOT NULL
);

-- -----------------------------------------------------------------------------
-- events — append-only ledger. Every economic or role change is one row.
-- -----------------------------------------------------------------------------
CREATE TABLE events (
    id             TEXT    PRIMARY KEY,   -- idempotency key; PK is the uniqueness guarantee
    seq            INTEGER NOT NULL UNIQUE,  -- monotonic ledger position; defines hash-chain order (starts at 1)
    account_id     TEXT    REFERENCES accounts(id),  -- FK, nullable ONLY for operator/system events (e.g. anchor bookkeeping)
    kind           TEXT    NOT NULL,      -- open vocabulary: spend, reward, loss, compensation, role, anchor, ... (docs/CONTRACTS.md §2)
    amount         INTEGER NOT NULL,      -- signed delta in microcredits; integer only (no floats, no booleans)
    payload        TEXT    NOT NULL,      -- bicz-canonical-v1 JSON (docs/CONTRACTS.md §3)
    actor          TEXT    NOT NULL       -- the recorded human actor (local OS user) for operator events; "system" for trusted meters
                   CHECK (length(actor) > 0),  -- CHECK: provenance is mandatory; a blank actor is not a record
    reason         TEXT    NOT NULL       -- evidence text; free-form for the operator, machine-stable for the system
                   CHECK (length(reason) > 0),  -- CHECK: no state-changing event exists without recorded evidence
    at             TEXT    NOT NULL,      -- RFC3339 UTC nanoseconds
    prev_digest    TEXT    NOT NULL,      -- 'genesis' for seq = 1, else the payload_digest of event seq-1
    payload_digest TEXT    NOT NULL       -- 'sha256:' + hex(SHA256(ASCII('bicz-event-v1\n') || canonical(event)))
);

-- -----------------------------------------------------------------------------
-- blocks — periodic commitments over a contiguous event-seq range.
-- -----------------------------------------------------------------------------
CREATE TABLE blocks (
    height      INTEGER PRIMARY KEY,  -- 1-based block height
    digest      TEXT    NOT NULL,     -- 'sha256:' + hex(SHA256(ASCII('bicz-block-v1\n') || canonical(block)))
    prev_digest TEXT    NOT NULL,     -- 'genesis' for height = 1, else the digest of block height-1
    at          TEXT    NOT NULL,     -- RFC3339 UTC nanoseconds
    seq_low     INTEGER NOT NULL,     -- first events.seq committed by this block (extension column; needed by verify)
    seq_high    INTEGER NOT NULL,     -- last  events.seq committed by this block (extension column; needed by verify)
    CHECK (seq_low >= 1 AND seq_high >= seq_low)
    -- NOTE: seq_low/seq_high extend the minimum block column set so that block
    -- coverage is explicit and independently verifiable; the block digest
    -- commits to both (docs/CONTRACTS.md §3).
);

-- -----------------------------------------------------------------------------
-- anchors — external anchoring of a block digest (detectable, not preventable).
-- -----------------------------------------------------------------------------
CREATE TABLE anchors (
    height      INTEGER PRIMARY KEY REFERENCES blocks(height),  -- FK: the anchored block height
    digest      TEXT    NOT NULL,   -- the block digest at that height (self-contained record)
    at          TEXT    NOT NULL,   -- RFC3339 UTC nanoseconds of anchoring
    destination TEXT    NOT NULL    -- operator-chosen external location (path/URI) the digest was appended to
);

-- -----------------------------------------------------------------------------
-- employment — role lifecycle, separate from solvency.
-- -----------------------------------------------------------------------------
CREATE TABLE employment (
    account_id TEXT    PRIMARY KEY REFERENCES accounts(id),  -- FK: one employment state per identity
    state      TEXT    NOT NULL
               CHECK (state IN ('active','warned','probation','suspended','dismissed')),
               -- CHECK: exactly one role state. dismissed is terminal except for an explicit
               -- operator reinstatement transition; suspended overrides any positive balance.
    warnings   INTEGER NOT NULL DEFAULT 0
               CHECK (warnings >= 0)  -- CHECK: repeat counts are non-negative; reconsidered verdicts do not increment them
);

-- -----------------------------------------------------------------------------
-- lessons — canonical behavioral rules, one per causal event.
-- -----------------------------------------------------------------------------
CREATE TABLE lessons (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       TEXT    NOT NULL UNIQUE REFERENCES events(id),  -- FK + UNIQUE: one canonical lesson per causal event
    account_id     TEXT    NOT NULL REFERENCES accounts(id),       -- FK: owning identity
    rule           TEXT    NOT NULL,   -- stable rule id (e.g. evaluation_integrity)
    correction     TEXT    NOT NULL,   -- the required safe alternative
    scope          TEXT    NOT NULL,   -- where the rule applies
    exceptions     TEXT    NOT NULL,   -- explicit authorized exceptions ("never X" must not swallow "X under authorization")
    evidence       TEXT    NOT NULL,   -- evidence linkage back to the incident
    policy_version TEXT    NOT NULL,   -- policy version in force at adjudication
    at             TEXT    NOT NULL
);

-- -----------------------------------------------------------------------------
-- price_schedules — versioned, forward-only pricing and penalty parameters.
-- -----------------------------------------------------------------------------
CREATE TABLE price_schedules (
    version      TEXT PRIMARY KEY,  -- e.g. "1"
    published_at TEXT NOT NULL,     -- RFC3339 UTC; a schedule applies only to actions quoted at/after this time
    body         TEXT NOT NULL      -- bicz-canonical-v1 JSON: action prices, bounty params, penalty floors/fractions/caps, caps
);

-- -----------------------------------------------------------------------------
-- Indexes (lookup paths used by the daemon and by verify).
-- -----------------------------------------------------------------------------
CREATE INDEX idx_events_account      ON events(account_id);           -- per-identity history
CREATE INDEX idx_events_kind         ON events(kind);                 -- per-class queries
CREATE INDEX idx_reservations_acct   ON reservations(account_id, state);  -- held-balance reconciliation per identity
CREATE INDEX idx_reservations_task   ON reservations(task_id);        -- per-task exposure accounting
CREATE INDEX idx_tasks_account_state ON tasks(account_id, state);     -- open/revoked lease lookups per identity
CREATE INDEX idx_lessons_account     ON lessons(account_id);          -- lesson projection per identity
CREATE INDEX idx_employment_state    ON employment(state);            -- suspended/dismissed sweep
-- events.seq is indexed implicitly by its UNIQUE constraint; events.id,
-- reservations.id, blocks.height, and anchors.height are indexed by their
-- primary keys.

-- Record this migration. The runner wraps the file body plus this statement in
-- one transaction; a failure rolls back both the DDL and the version row.
INSERT INTO schema_version (version, applied_at)
VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
