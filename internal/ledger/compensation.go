package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// CompensationResult is the receipt of an append-only reversal.
type CompensationResult struct {
	EventID         string
	AccountID       string
	OriginalEventID string
	Amount          int64
	Balance         int64
	OutstandingLoss int64
	Replayed        bool
}

// RecordCompensation reverses part or all of a prior event by appending a new
// `compensation` event. It is the only reversal path in the ledger.
//
// Rules that make this safe:
//
//   - Nothing is edited or deleted. The original event row and its `lessons` row
//     stay exactly as written (docs/CONTRACTS.md §1 I11); the reversal is visible
//     as its own row referencing originalEventID.
//   - Credits are restored at most once: a second compensation naming the same
//     original event is ErrAlreadyCompensated, even under a different idempotency
//     key. A replay under the same key and payload is a no-op.
//   - The amount may not exceed the original event's magnitude, so a
//     compensation can never mint credit.
//   - Role is untouched. Compensating a burn does not clear a warning, exit
//     probation, or lift a suspension: a role change is its own event through
//     SetRole.
func (l *Ledger) RecordCompensation(
	ctx context.Context,
	eventID, originalEventID, accountID string,
	amount int64,
	reason, evidence string,
) (CompensationResult, error) {
	if eventID == "" {
		return CompensationResult{}, errf(ErrInvalidArgument, "compensation event id is empty")
	}
	if originalEventID == "" {
		return CompensationResult{}, errf(ErrInvalidArgument, "compensation needs an original event id")
	}
	if accountID == "" {
		return CompensationResult{}, errf(ErrUnknownIdentity, "compensation account id is empty")
	}
	if amount <= 0 {
		return CompensationResult{}, errf(ErrInvalidAmount, "compensation amount %d must be positive", amount)
	}
	if reason == "" {
		return CompensationResult{}, errf(ErrReasonRequired, "compensation %q", eventID)
	}

	var result CompensationResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindCompensation {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindCompensation)
			}
			var payload compensationPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.AccountID != accountID || payload.OriginalEventID != originalEventID ||
				payload.Amount != amount || stored.Reason != reason+": "+evidence {
				return errf(ErrPayloadConflict, "compensation %q replayed with a different payload", eventID)
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
			result = CompensationResult{
				EventID:         eventID,
				AccountID:       accountID,
				OriginalEventID: originalEventID,
				Amount:          payload.Amount,
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
		original, found, err := loadEvent(ctx, tx, originalEventID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownEvent, "%q", originalEventID)
		}
		if original.AccountID == nil || *original.AccountID != accountID {
			return errf(ErrUnknownEvent, "%q does not belong to %q", originalEventID, accountID)
		}
		if original.Kind == kindCompensation {
			return errf(ErrNotCompensable, "%q is itself a compensation", originalEventID)
		}
		magnitude := absInt64(original.Amount)
		if magnitude == 0 {
			return errf(ErrNotCompensable, "%q moved no credit", originalEventID)
		}
		already, err := compensatedAlready(ctx, tx, accountID, originalEventID)
		if err != nil {
			return err
		}
		if already {
			return errf(ErrAlreadyCompensated, "%q", originalEventID)
		}
		if amount > magnitude {
			return errf(ErrExceedsOriginal, "compensation %d exceeds original %d for %q", amount, magnitude, originalEventID)
		}

		before, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return err
		}
		// A compensation of a burn repays that specific burn, so the shortfall
		// drops by exactly what that burn still had outstanding.
		reduction := minInt64(amount, before.Remaining[originalEventID])
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET balance = balance + ? WHERE id = ?`, amount, accountID); err != nil {
			return fmt.Errorf("ledger: restore compensation %s: %w", eventID, err)
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindCompensation,
			amount:    amount,
			payload: compensationPayload{
				AccountID:        accountID,
				OriginalEventID:  originalEventID,
				OriginalKind:     original.Kind,
				OriginalAmount:   original.Amount,
				Amount:           amount,
				BalanceBefore:    account.Balance,
				BalanceAfter:     account.Balance + amount,
				OutstandingAfter: before.Outstanding - reduction,
			},
			actor:  l.actor,
			reason: reason + ": " + evidence,
		}); err != nil {
			return err
		}
		result = CompensationResult{
			EventID:         eventID,
			AccountID:       accountID,
			OriginalEventID: originalEventID,
			Amount:          amount,
			Balance:         account.Balance + amount,
			OutstandingLoss: before.Outstanding - reduction,
		}
		return nil
	})
	if err != nil {
		return CompensationResult{}, err
	}
	return result, nil
}

func compensatedAlready(ctx context.Context, q querier, accountID, originalEventID string) (bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT payload FROM events WHERE account_id = ? AND kind = ?`, accountID, kindCompensation)
	if err != nil {
		return false, fmt.Errorf("ledger: read prior compensations for %s: %w", accountID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return false, fmt.Errorf("ledger: scan prior compensation: %w", err)
		}
		var decoded compensationPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return false, errw(ErrPayloadConflict, fmt.Errorf("prior compensation payload: %w", err))
		}
		if decoded.OriginalEventID == originalEventID {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("ledger: read prior compensations for %s: %w", accountID, err)
	}
	return false, nil
}
