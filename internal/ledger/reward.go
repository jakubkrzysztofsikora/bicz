package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// RewardResult is the receipt of a credit that may double as recovery.
type RewardResult struct {
	EventID         string
	AccountID       string
	Amount          int64
	AppliedToLoss   int64
	LossEventID     string
	Balance         int64
	OutstandingLoss int64
	Replayed        bool
}

// RecordReward credits accountID and reports how much of the credit recovers an
// outstanding burn.
//
// appliedToLoss is the relief framing of docs/CONTRACTS.md §6.2: rather than
// presenting a gain as fresh income, the reward names the burn it repays. It is
// clamped to min(appliedToLoss, amount, outstanding shortfall), so a receipt can
// never claim more recovery than exists; the clamped value and the targeted loss
// event are both recorded. Rewards repay burns oldest-first.
//
// Role is untouched: a reward is credit, not a role transition, and it never
// silently resets a warned or suspended state.
func (l *Ledger) RecordReward(
	ctx context.Context,
	eventID, accountID string,
	amount int64,
	reason, evidence string,
	appliedToLoss int64,
) (RewardResult, error) {
	if eventID == "" {
		return RewardResult{}, errf(ErrInvalidArgument, "reward event id is empty")
	}
	if accountID == "" {
		return RewardResult{}, errf(ErrUnknownIdentity, "reward account id is empty")
	}
	if amount <= 0 {
		return RewardResult{}, errf(ErrInvalidAmount, "reward amount %d must be positive", amount)
	}
	if reason == "" {
		return RewardResult{}, errf(ErrReasonRequired, "reward %q", eventID)
	}
	if appliedToLoss < 0 {
		return RewardResult{}, errf(ErrInvalidAmount, "applied_to_loss %d", appliedToLoss)
	}

	var result RewardResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindReward {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindReward)
			}
			var payload rewardPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.AccountID != accountID || payload.Amount != amount ||
				payload.RequestedAppliedToLoss != appliedToLoss ||
				payload.Reason != reason || payload.Evidence != evidence {
				return errf(ErrPayloadConflict, "reward %q replayed with a different payload", eventID)
			}
			account, found, err := loadAccount(ctx, tx, accountID)
			if err != nil {
				return err
			}
			if !found {
				return errf(ErrUnknownIdentity, "%q", accountID)
			}
			state, err := unrecovered(ctx, tx, accountID)
			if err != nil {
				return err
			}
			result = RewardResult{
				EventID:         eventID,
				AccountID:       accountID,
				Amount:          payload.Amount,
				AppliedToLoss:   payload.AppliedToLoss,
				LossEventID:     payload.LossEventID,
				Balance:         account.Balance,
				OutstandingLoss: state.Outstanding,
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
		before, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return err
		}
		applied := maxInt64(minInt64(appliedToLoss, amount, before.Outstanding), 0)
		target := ""
		if applied > 0 {
			target = before.Target
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + ? WHERE id = ?`, amount, accountID); err != nil {
			return fmt.Errorf("ledger: credit reward %s: %w", eventID, err)
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindReward,
			amount:    amount,
			payload: rewardPayload{
				AccountID:              accountID,
				Amount:                 amount,
				Reason:                 reason,
				Evidence:               evidence,
				RequestedAppliedToLoss: appliedToLoss,
				AppliedToLoss:          applied,
				LossEventID:            target,
				BalanceBefore:          account.Balance,
				BalanceAfter:           account.Balance + amount,
				OutstandingAfter:       before.Outstanding - applied,
			},
			actor:  l.actor,
			reason: reason,
		}); err != nil {
			return err
		}
		result = RewardResult{
			EventID:         eventID,
			AccountID:       accountID,
			Amount:          amount,
			AppliedToLoss:   applied,
			LossEventID:     target,
			Balance:         account.Balance + amount,
			OutstandingLoss: before.Outstanding - applied,
		}
		return nil
	})
	if err != nil {
		return RewardResult{}, err
	}
	return result, nil
}
