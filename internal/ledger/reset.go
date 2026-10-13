package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// ResetResult is the receipt of a budget reset event.
type ResetResult struct {
	EventID         string
	AccountID       string
	TargetBalance   int64
	BalanceBefore   int64
	BalanceAfter    int64
	Delta           int64
	OutstandingLoss int64
	Role            string
	Replayed        bool
}

// ResetBudget restores an identity's balance to targetBalance without running
// reward logic.
//
// Key properties:
//   - It does NOT run reward logic and does NOT reduce or repay outstanding loss.
//     outstanding_loss is untouched.
//   - It does NOT alter lessons or penalties.
//   - It does NOT alter employment state or warnings.
//   - It replenishes spent credits up to targetBalance so an agent blocked by
//     an empty balance is unblocked.
//   - If targetBalance <= 0, the ledger uses the schedule's initial capital or
//     the default baseline (100 credits = 100_000_000 mc).
//   - If the account balance is already >= targetBalance, delta is 0 and no
//     credits are added.
//   - Exact replay on eventID is idempotent.
func (l *Ledger) ResetBudget(
	ctx context.Context,
	eventID, accountID string,
	targetBalance int64,
	reason, evidence string,
) (ResetResult, error) {
	if eventID == "" {
		return ResetResult{}, errf(ErrInvalidArgument, "reset event id is empty")
	}
	if accountID == "" {
		return ResetResult{}, errf(ErrUnknownIdentity, "reset account id is empty")
	}
	if reason == "" {
		reason = "reset budget to unblock agent"
	}
	if targetBalance <= 0 {
		targetBalance = DefaultBaselineCeilingMC
	}

	var result ResetResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindGrant {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindGrant)
			}
			var payload resetPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.Phase != "reset" || payload.AccountID != accountID || payload.TargetBalance != targetBalance {
				return errf(ErrPayloadConflict, "reset %q replayed with a different payload", eventID)
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
			state, err := unrecovered(ctx, tx, accountID)
			if err != nil {
				return err
			}
			result = ResetResult{
				EventID:         eventID,
				AccountID:       accountID,
				TargetBalance:   payload.TargetBalance,
				BalanceBefore:   payload.BalanceBefore,
				BalanceAfter:    payload.BalanceAfter,
				Delta:           payload.Delta,
				OutstandingLoss: state.Outstanding,
				Role:            employment.State,
				Replayed:        true,
			}
			return nil
		}

		account, found, err := loadAccount(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		employment, err := loadEmployment(ctx, tx, accountID)
		if err != nil {
			return err
		}

		balanceBefore := account.Balance
		var delta int64
		balanceAfter := balanceBefore
		if balanceBefore < targetBalance {
			delta = targetBalance - balanceBefore
			balanceAfter = targetBalance
		}

		if delta > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE accounts SET balance = balance + ? WHERE id = ?`,
				delta, accountID); err != nil {
				return fmt.Errorf("ledger: update balance for reset %s: %w", accountID, err)
			}
		}

		payload := resetPayload{
			Phase:         "reset",
			AccountID:     accountID,
			TargetBalance: targetBalance,
			BalanceBefore: balanceBefore,
			BalanceAfter:  balanceAfter,
			Delta:         delta,
		}

		evReason := reason
		if evidence != "" {
			evReason += ": " + evidence
		}

		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindGrant,
			amount:    delta,
			payload:   payload,
			actor:     l.actor,
			reason:    evReason,
		}); err != nil {
			return err
		}

		state, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return err
		}

		result = ResetResult{
			EventID:         eventID,
			AccountID:       accountID,
			TargetBalance:   targetBalance,
			BalanceBefore:   balanceBefore,
			BalanceAfter:    balanceAfter,
			Delta:           delta,
			OutstandingLoss: state.Outstanding,
			Role:            employment.State,
			Replayed:        false,
		}
		return nil
	})
	if err != nil {
		return ResetResult{}, err
	}
	return result, nil
}
