-- =============================================================================
-- bicz — schema version 2 (migration 002_lesson_proposals)
--
-- Additive and forward-only. Creates `lesson_proposals`: the non-canonical store
-- for agent-authored lesson proposals (docs/CONTRACTS.md §5.4 `lesson.propose`,
-- docs/SCHEMA.md `lessons`).
--
-- WHY a separate table instead of the `lessons` table: an agent-authored
-- reflection is a PROPOSAL. It is never canonical, only an operator review
-- writes a `lessons` row, and mixing the two would make "proposed" and
-- "canonical" distinguishable only by a flag. Proposals are also deliberately
-- NOT written into the hash-chained `events` ledger: the adapter protocol grants
-- exactly two adapter-visible writes (trusted meter settlement and lesson
-- proposals), and letting an agent append rows to the financial chain is not one
-- of them.
--
-- `state` is a closed set and a resolved proposal cannot exist without an
-- operator, a timestamp, and the event that resolved it.
-- =============================================================================

-- Enforce REFERENCES clauses for this connection (per-connection pragma).
PRAGMA foreign_keys = ON;

CREATE TABLE lesson_proposals (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id          TEXT    NOT NULL REFERENCES accounts(id),  -- FK: owning identity
    session_alias       TEXT    NOT NULL,  -- harness session label; an alias, never an account
    harness             TEXT    NOT NULL,  -- which harness proposed it
    rule                TEXT    NOT NULL,  -- proposed stable rule id
    correction          TEXT    NOT NULL,  -- proposed required safe alternative
    scope               TEXT    NOT NULL,  -- where the rule is proposed to apply
    exceptions          TEXT    NOT NULL,  -- proposed explicit authorized exceptions
    evidence            TEXT    NOT NULL,  -- evidence linkage back to the incident
    policy_version      TEXT    NOT NULL,  -- policy version in force when proposed
    source_event_id     TEXT    REFERENCES events(id),  -- FK, nullable: the causal ledger event, when the proposer names one
    idempotency_key     TEXT    NOT NULL UNIQUE,  -- proposal idempotency key; PK-equivalent uniqueness
    state               TEXT    NOT NULL
                        CHECK (state IN ('proposed','applied','rejected')),
    proposed_at         TEXT    NOT NULL,  -- RFC3339 UTC nanoseconds
    resolved_at         TEXT,              -- RFC3339 UTC nanoseconds; set by operator review
    resolver            TEXT,              -- the local OS user who resolved it
    resolution_event_id TEXT    REFERENCES events(id),  -- FK: the `lesson` event that records the resolution
    -- A resolved proposal must carry its resolution provenance: an operator, a
    -- time, and the event that recorded it. A `proposed` row must carry none.
    CHECK (state = 'proposed' OR (resolved_at IS NOT NULL AND resolver IS NOT NULL AND resolution_event_id IS NOT NULL))
);

CREATE INDEX idx_lesson_proposals_account ON lesson_proposals(account_id, state);
CREATE INDEX idx_lesson_proposals_state   ON lesson_proposals(state);

-- Record this migration. The runner wraps the file body plus this statement in
-- one transaction; a failure rolls back both the DDL and the version row.
INSERT INTO schema_version (version, applied_at)
VALUES (2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
