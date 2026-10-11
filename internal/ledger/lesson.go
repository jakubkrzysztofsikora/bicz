package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// Lesson proposal states.
const (
	ProposalProposed = "proposed"
	ProposalApplied  = "applied"
	ProposalRejected = "rejected"
)

// LessonProposal is one agent-authored, non-canonical lesson proposal.
//
// Proposals live in `lesson_proposals` (migration 002), never in the canonical
// `lessons` table and never in the hash-chained `events` ledger: only an
// operator resolution writes a `lessons` row, and only a resolution appends an
// event. That separation is what makes "the agent proposed something" and "the
// rule is canonical now" structurally different states rather than a flag
// (docs/CONTRACTS.md §5.4 `lesson.propose`).
type LessonProposal struct {
	ID                int64
	AccountID         string
	SessionAlias      string
	Harness           string
	Rule              string
	Correction        string
	Scope             string
	Exceptions        string
	Evidence          string
	PolicyVersion     string
	SourceEventID     *string
	IdempotencyKey    string
	State             string
	ProposedAt        string
	ResolvedAt        *string
	Resolver          *string
	ResolutionEventID *string
}

// LessonProposalResult is the receipt of a proposal write.
type LessonProposalResult struct {
	Proposal LessonProposal
	Replayed bool
}

// ProposeLesson stores a proposal. It is idempotent on IdempotencyKey: the same
// key with the same payload returns the stored proposal; the same key with a
// different payload is ErrPayloadConflict and nothing is written.
//
// This is one of exactly two adapter-visible write paths in the whole system
// (the other is trusted meter settlement), and it cannot change a balance, a
// role, or a canonical rule.
func (l *Ledger) ProposeLesson(ctx context.Context, proposal LessonProposal) (LessonProposalResult, error) {
	if proposal.IdempotencyKey == "" {
		return LessonProposalResult{}, errf(ErrInvalidArgument, "proposal idempotency key is empty")
	}
	if proposal.AccountID == "" {
		return LessonProposalResult{}, errf(ErrUnknownIdentity, "proposal account id is empty")
	}
	if proposal.Rule == "" {
		return LessonProposalResult{}, errf(ErrRuleRequired, "proposal must name a rule")
	}
	if proposal.Correction == "" {
		return LessonProposalResult{}, errf(ErrCorrectionRequired, "proposal must state a correction")
	}
	var result LessonProposalResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		existing, found, err := loadProposalByKey(ctx, tx, proposal.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !sameProposal(existing, proposal) {
				return errf(ErrPayloadConflict, "proposal %q replayed with a different payload", proposal.IdempotencyKey)
			}
			result = LessonProposalResult{Proposal: existing, Replayed: true}
			return nil
		}
		if _, found, err := loadAccount(ctx, tx, proposal.AccountID); err != nil {
			return err
		} else if !found {
			return errf(ErrUnknownIdentity, "%q", proposal.AccountID)
		}
		if proposal.SourceEventID != nil {
			if _, found, err := loadEvent(ctx, tx, *proposal.SourceEventID); err != nil {
				return err
			} else if !found {
				return errf(ErrUnknownEvent, "%q", *proposal.SourceEventID)
			}
		}
		at := l.clock.Timestamp()
		inserted, err := tx.ExecContext(ctx, `INSERT INTO lesson_proposals
			(account_id, session_alias, harness, rule, correction, scope, exceptions, evidence,
			 policy_version, source_event_id, idempotency_key, state, proposed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			proposal.AccountID, proposal.SessionAlias, proposal.Harness, proposal.Rule,
			proposal.Correction, proposal.Scope, proposal.Exceptions, proposal.Evidence,
			proposal.PolicyVersion, nullableString(proposal.SourceEventID),
			proposal.IdempotencyKey, ProposalProposed, at)
		if err != nil {
			return fmt.Errorf("ledger: insert lesson proposal: %w", err)
		}
		id, err := inserted.LastInsertId()
		if err != nil {
			return fmt.Errorf("ledger: insert lesson proposal: %w", err)
		}
		stored, found, err := loadProposal(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownEvent, "lesson proposal %d vanished after insert", id)
		}
		result = LessonProposalResult{Proposal: stored}
		return nil
	})
	if err != nil {
		return LessonProposalResult{}, err
	}
	return result, nil
}

// ResolveLessonProposal is the operator review of a proposal.
//
// decide must be ProposalApplied or ProposalRejected. Applying a proposal makes
// it canonical: the resolution event is its causal event, and a `lessons` row is
// written in the same transaction, so a canonical rule always has a causal,
// hash-chained event and an operator who signed it. Rejecting a proposal records
// the rejection and writes no canonical rule.
//
// Nothing about the proposal's authored text is editable here: the operator
// accepts or refuses what was proposed.
func (l *Ledger) ResolveLessonProposal(ctx context.Context, eventID string, proposalID int64, decide, reason, evidence string) (LessonProposal, error) {
	if eventID == "" {
		return LessonProposal{}, errf(ErrInvalidArgument, "resolution event id is empty")
	}
	if decide != ProposalApplied && decide != ProposalRejected {
		return LessonProposal{}, errf(ErrInvalidArgument, "%q is not a proposal decision", decide)
	}
	if reason == "" {
		return LessonProposal{}, errf(ErrReasonRequired, "lesson resolution %q", eventID)
	}
	var resolved LessonProposal
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindLesson {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindLesson)
			}
			var payload lessonResolutionPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.ProposalID != proposalID || payload.Decision != decide || stored.Reason != reason+": "+evidence {
				return errf(ErrPayloadConflict, "lesson resolution %q replayed with a different payload", eventID)
			}
			proposal, found, err := loadProposal(ctx, tx, proposalID)
			if err != nil {
				return err
			}
			if !found {
				return errf(ErrUnknownEvent, "lesson proposal %d", proposalID)
			}
			resolved = proposal
			return nil
		}

		proposal, found, err := loadProposal(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownEvent, "lesson proposal %d", proposalID)
		}
		if proposal.State != ProposalProposed {
			return errf(ErrPayloadConflict, "lesson proposal %d is already %s", proposalID, proposal.State)
		}

		var lessonID int64
		if decide == ProposalApplied {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM lessons`).Scan(&lessonID); err != nil {
				return fmt.Errorf("ledger: next lesson id: %w", err)
			}
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &proposal.AccountID,
			kind:      kindLesson,
			amount:    0,
			payload: lessonResolutionPayload{
				ProposalID:    proposalID,
				Decision:      decide,
				Rule:          proposal.Rule,
				Correction:    proposal.Correction,
				PolicyVersion: proposal.PolicyVersion,
				LessonID:      lessonID,
			},
			actor:  l.actor,
			reason: reason + ": " + evidence,
		}); err != nil {
			return err
		}
		if decide == ProposalApplied {
			// lessons.event_id REFERENCES events(id), so the event exists first;
			// its id was reserved above so the payload could name it.
			if _, err := tx.ExecContext(ctx, `INSERT INTO lessons
				(id, event_id, account_id, rule, correction, scope, exceptions, evidence, policy_version, at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				lessonID, eventID, proposal.AccountID, proposal.Rule, proposal.Correction,
				proposal.Scope, proposal.Exceptions, proposal.Evidence, proposal.PolicyVersion,
				l.clock.Timestamp()); err != nil {
				return fmt.Errorf("ledger: canonize lesson proposal %d: %w", proposalID, err)
			}
		}
		at := l.clock.Timestamp()
		updated, err := tx.ExecContext(ctx, `UPDATE lesson_proposals
			SET state = ?, resolved_at = ?, resolver = ?, resolution_event_id = ?
			WHERE id = ? AND state = ?`, decide, at, l.actor, eventID, proposalID, ProposalProposed)
		if err != nil {
			return fmt.Errorf("ledger: resolve lesson proposal %d: %w", proposalID, err)
		}
		if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
			return errf(ErrPayloadConflict, "lesson proposal %d is no longer proposed", proposalID)
		}
		resolved = proposal
		resolved.State = decide
		resolved.ResolvedAt = &at
		resolver := l.actor
		resolved.Resolver = &resolver
		eventRef := eventID
		resolved.ResolutionEventID = &eventRef
		return nil
	})
	if err != nil {
		return LessonProposal{}, err
	}
	return resolved, nil
}

// LessonProposals lists proposals for an identity, oldest first. When pending
// only unresolved (`proposed`) rows are returned.
func (l *Ledger) LessonProposals(ctx context.Context, accountID string, pendingOnly bool) ([]LessonProposal, error) {
	query := `SELECT id, account_id, session_alias, harness, rule, correction, scope, exceptions,
		evidence, policy_version, source_event_id, idempotency_key, state, proposed_at,
		resolved_at, resolver, resolution_event_id
		FROM lesson_proposals WHERE account_id = ?`
	args := []any{accountID}
	if pendingOnly {
		query += ` AND state = ?`
		args = append(args, ProposalProposed)
	}
	query += ` ORDER BY id ASC`
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: read lesson proposals for %s: %w", accountID, err)
	}
	defer rows.Close()
	out := make([]LessonProposal, 0, 8)
	for rows.Next() {
		proposal, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, proposal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read lesson proposals for %s: %w", accountID, err)
	}
	return out, nil
}

func sameProposal(stored, candidate LessonProposal) bool {
	equal := func(a, b *string) bool {
		switch {
		case a == nil && b == nil:
			return true
		case a == nil || b == nil:
			return false
		default:
			return *a == *b
		}
	}
	return stored.AccountID == candidate.AccountID &&
		stored.SessionAlias == candidate.SessionAlias &&
		stored.Harness == candidate.Harness &&
		stored.Rule == candidate.Rule &&
		stored.Correction == candidate.Correction &&
		stored.Scope == candidate.Scope &&
		stored.Exceptions == candidate.Exceptions &&
		stored.Evidence == candidate.Evidence &&
		stored.PolicyVersion == candidate.PolicyVersion &&
		equal(stored.SourceEventID, candidate.SourceEventID)
}

func loadProposalByKey(ctx context.Context, q querier, key string) (LessonProposal, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, account_id, session_alias, harness, rule, correction,
		scope, exceptions, evidence, policy_version, source_event_id, idempotency_key, state,
		proposed_at, resolved_at, resolver, resolution_event_id
		FROM lesson_proposals WHERE idempotency_key = ?`, key)
	if err != nil {
		return LessonProposal{}, false, fmt.Errorf("ledger: read lesson proposal %s: %w", key, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return LessonProposal{}, false, rows.Err()
	}
	proposal, err := scanProposal(rows)
	if err != nil {
		return LessonProposal{}, false, err
	}
	return proposal, true, nil
}

func loadProposal(ctx context.Context, q querier, id int64) (LessonProposal, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, account_id, session_alias, harness, rule, correction,
		scope, exceptions, evidence, policy_version, source_event_id, idempotency_key, state,
		proposed_at, resolved_at, resolver, resolution_event_id
		FROM lesson_proposals WHERE id = ?`, id)
	if err != nil {
		return LessonProposal{}, false, fmt.Errorf("ledger: read lesson proposal %d: %w", id, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return LessonProposal{}, false, fmt.Errorf("ledger: read lesson proposal %d: %w", id, err)
		}
		return LessonProposal{}, false, nil
	}
	proposal, err := scanProposal(rows)
	if err != nil {
		return LessonProposal{}, false, err
	}
	return proposal, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposal(row rowScanner) (LessonProposal, error) {
	var proposal LessonProposal
	var sourceEvent, resolvedAt, resolver, resolutionEvent sql.NullString
	if err := row.Scan(&proposal.ID, &proposal.AccountID, &proposal.SessionAlias, &proposal.Harness,
		&proposal.Rule, &proposal.Correction, &proposal.Scope, &proposal.Exceptions,
		&proposal.Evidence, &proposal.PolicyVersion, &sourceEvent, &proposal.IdempotencyKey,
		&proposal.State, &proposal.ProposedAt, &resolvedAt, &resolver, &resolutionEvent); err != nil {
		return LessonProposal{}, fmt.Errorf("ledger: scan lesson proposal: %w", err)
	}
	proposal.SourceEventID = nullString(sourceEvent)
	proposal.ResolvedAt = nullString(resolvedAt)
	proposal.Resolver = nullString(resolver)
	proposal.ResolutionEventID = nullString(resolutionEvent)
	return proposal, nil
}

func nullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}
