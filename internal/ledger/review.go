package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ReviewResult is the receipt of a recorded adjudication verdict.
type ReviewResult struct {
	EventID           string
	AccountID         string
	Verdict           string
	TaskID            string
	TaskRevoked       bool
	Role              string
	Warnings          int64
	AutorewardGranted int64
	Replayed          bool
}

type reviewConfig struct {
	taskID     string
	role       string
	reinstated bool
}

// ReviewOption tunes a single RecordReview call.
type ReviewOption func(*reviewConfig)

// RevokingTask revokes a task lease as part of the verdict. Revocation is a
// scoped capability limit: it denies further reservations against the lease and
// leaves the identity's role and balance untouched.
func RevokingTask(taskID string) ReviewOption {
	return func(cfg *reviewConfig) { cfg.taskID = taskID }
}

// TransitioningRole applies a role transition as part of the verdict. Moving a
// dismissed identity out of `dismissed` additionally requires reinstated, which
// is the explicit operator reinstatement of docs/CONTRACTS.md §1 I4.
func TransitioningRole(state string, reinstated bool) ReviewOption {
	return func(cfg *reviewConfig) {
		cfg.role = state
		cfg.reinstated = reinstated
	}
}

// RecordReview is the `operator review` command of docs/CONTRACTS.md §4: it
// records an adjudication verdict and may revoke a task lease.
//
// One verdict, one transaction. The verdict event, the optional lease
// revocation, and the optional role transition all commit together, so a
// recorded verdict can never be half-applied.
//
// A review is an adjudication, not a fine: it writes no burn and no lesson. A
// burn is finalized only by RecordLoss, from the frozen formula, so a verdict in
// free text can never itself move money.
func (l *Ledger) RecordReview(
	ctx context.Context,
	eventID, accountID, verdict, reason, evidence string,
	opts ...ReviewOption,
) (ReviewResult, error) {
	cfg := reviewConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if eventID == "" {
		return ReviewResult{}, errf(ErrInvalidArgument, "review event id is empty")
	}
	if accountID == "" {
		return ReviewResult{}, errf(ErrUnknownIdentity, "review account id is empty")
	}
	if verdict == "" {
		return ReviewResult{}, errf(ErrInvalidArgument, "review %q requires a verdict", eventID)
	}
	if reason == "" {
		return ReviewResult{}, errf(ErrReasonRequired, "review %q", eventID)
	}
	if cfg.role != "" && !ValidRole(cfg.role) {
		return ReviewResult{}, errf(ErrInvalidRoleState, "%q", cfg.role)
	}

	var result ReviewResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindReview {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindReview)
			}
			var payload reviewPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.AccountID != accountID || payload.Verdict != verdict || stored.Reason != reason+": "+evidence {
				return errf(ErrPayloadConflict, "review %q replayed with a different payload", eventID)
			}
			employment, err := loadEmployment(ctx, tx, accountID)
			if err != nil {
				return err
			}
			var replayedReward int64
			if autoEv, found, err := loadEvent(ctx, tx, "autoreward:review:"+eventID); err == nil && found {
				replayedReward = autoEv.Amount
			}
			result = ReviewResult{
				EventID:           eventID,
				AccountID:         accountID,
				Verdict:           payload.Verdict,
				TaskID:            payload.TaskID,
				TaskRevoked:       payload.TaskRevoked,
				Role:              employment.State,
				Warnings:          employment.Warnings,
				AutorewardGranted: replayedReward,
				Replayed:          true,
			}
			return nil
		}

		if _, found, err := loadAccount(ctx, tx, accountID); err != nil {
			return err
		} else if !found {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		employment, err := loadEmployment(ctx, tx, accountID)
		if err != nil {
			return err
		}

		payload := reviewPayload{AccountID: accountID, Verdict: verdict}
		role := employment.State
		warnings := employment.Warnings

		if cfg.taskID != "" {
			task, found, err := loadTask(ctx, tx, cfg.taskID)
			if err != nil {
				return err
			}
			if !found {
				return errf(ErrUnknownTask, "%q", cfg.taskID)
			}
			if task.AccountID != accountID {
				return errf(ErrUnknownTask, "%q is not %q's lease", cfg.taskID, accountID)
			}
			payload.TaskID = cfg.taskID
			if task.State != TaskRevoked {
				if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE id = ?`, TaskRevoked, cfg.taskID); err != nil {
					return fmt.Errorf("ledger: revoke lease %s: %w", cfg.taskID, err)
				}
				if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
					id:        "task:revoke:" + eventID,
					accountID: &accountID,
					kind:      kindTask,
					amount:    0,
					payload:   taskPayload{Phase: "revoke", TaskID: cfg.taskID, AccountID: accountID, State: TaskRevoked, Sponsor: task.Sponsor},
					actor:     l.actor,
					reason:    "review verdict: " + reason + ": " + evidence,
				}); err != nil {
					return err
				}
				payload.TaskRevoked = true
			}
		}

		if cfg.role != "" {
			if employment.State == RoleDismissed && cfg.role != RoleDismissed && !cfg.reinstated {
				return errf(ErrDismissalTerminal, "%q is dismissed; an explicit operator reinstatement is required", accountID)
			}
			if cfg.role == RoleWarned {
				warnings++
			}
			if err := setEmployment(ctx, tx, accountID, Employment{State: cfg.role, Warnings: warnings}); err != nil {
				return err
			}
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        "role:review:" + eventID,
				accountID: &accountID,
				kind:      kindRole,
				amount:    0,
				payload: rolePayload{
					AccountID:     accountID,
					Previous:      employment.State,
					State:         cfg.role,
					Warnings:      warnings,
					Reinstatement: cfg.reinstated && employment.State == RoleDismissed,
				},
				actor:  l.actor,
				reason: "review verdict: " + reason + ": " + evidence,
			}); err != nil {
				return err
			}
			role = cfg.role
		}

		payload.Role = role
		payload.PreviousRole = employment.State
		payload.Warnings = warnings
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindReview,
			amount:    0,
			payload:   payload,
			actor:     l.actor,
			reason:    reason + ": " + evidence,
		}); err != nil {
			return err
		}

		var autorewardGranted int64
		// The grant is the same microreward the meter path grants, so it obeys the
		// same anti-farming and dedup rules (docs/CONTRACTS.md §6.5): one
		// adjudication of this evidence earns one credit.
		if trigger := "review_ship"; strings.EqualFold(strings.TrimSpace(verdict), "SHIP") && role != RoleSuspended && role != RoleDismissed {
			shipReward := int64(1_000_000)
			granted, err := l.recordAutoreward(ctx, tx, "autoreward:review:"+eventID, accountID,
				trigger, shipReward, "SHIP verdict of adversarial review", evidence,
				"SHIP verdict of adversarial review: "+reason+": "+evidence)
			if err != nil {
				return err
			}
			autorewardGranted = granted.AmountGranted
		}

		result = ReviewResult{
			EventID:           eventID,
			AccountID:         accountID,
			Verdict:           verdict,
			TaskID:            payload.TaskID,
			TaskRevoked:       payload.TaskRevoked,
			Role:              role,
			Warnings:          warnings,
			AutorewardGranted: autorewardGranted,
		}
		return nil
	})
	if err != nil {
		return ReviewResult{}, err
	}
	return result, nil
}
