package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// Autoreward constants enforcing bounds and limits.
const (
	// MaxAutorewardMC bounds any single autoreward to 5 credits (5_000_000 mc).
	MaxAutorewardMC int64 = 5_000_000

	// DefaultBaselineCeilingMC is the default capital ceiling (100 credits).
	DefaultBaselineCeilingMC int64 = 100_000_000
)

// AutorewardResult is the receipt of an autoreward credit.
type AutorewardResult struct {
	EventID         string
	AccountID       string
	Trigger         string
	AmountRequested int64
	AmountGranted   int64
	BaselineCeiling int64
	BalanceBefore   int64
	BalanceAfter    int64
	OutstandingLoss int64
	Role            string
	Replayed        bool
}

// autorewardByActionDigest finds the prior autoreward this account holds for
// the same observed action. The action digest is the evidence the caller
// observed, so two calls quoting it describe one action, not two (docs/
// CONTRACTS.md §6.5, "Deduplicated by action digest and event key; identical
// runs cannot be credited twice"). An empty digest identifies nothing, so it
// dedups nothing and the event key alone governs.
func autorewardByActionDigest(ctx context.Context, q querier, accountID, trigger, evidence string) (Event, autorewardPayload, bool, error) {
	if evidence == "" {
		return Event{}, autorewardPayload{}, false, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT id, seq, account_id, kind, amount, payload, actor, reason, at, prev_digest, payload_digest
		FROM events WHERE account_id = ? AND kind = ? ORDER BY seq ASC`, accountID, kindAutoreward)
	if err != nil {
		return Event{}, autorewardPayload{}, false, fmt.Errorf("ledger: read autoreward events for %s: %w", accountID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Seq, &event.AccountID, &event.Kind, &event.Amount,
			&event.Payload, &event.Actor, &event.Reason, &event.At, &event.PrevDigest, &event.PayloadDigest); err != nil {
			return Event{}, autorewardPayload{}, false, fmt.Errorf("ledger: scan autoreward event for %s: %w", accountID, err)
		}
		var payload autorewardPayload
		if err := decodePayload(event, &payload); err != nil {
			return Event{}, autorewardPayload{}, false, err
		}
		if payload.DuplicateOf != "" {
			continue // a refusal row; the credit it points at has a lower seq
		}
		if payload.AccountID == accountID && payload.Trigger == trigger && payload.Evidence == evidence {
			return event, payload, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Event{}, autorewardPayload{}, false, fmt.Errorf("ledger: read autoreward events for %s: %w", accountID, err)
	}
	return Event{}, autorewardPayload{}, false, nil
}

// RecordAutoreward grants a bounded microreward to replenish spent credits when
// the agent demonstrates verified progress (e.g. passing test, "SHIP" review verdict,
// completing a plan milestone/todo item).
//
// Bulletproof safety invariants:
//
//  1. Hard Capital Ceiling (Replenish Only):
//     Autoreward can only replenish spent credits up to the baseline ceiling.
//     If balance >= baselineCeiling, granted amount is 0.
//     An agent cannot farm credits beyond its starting capital.
//
//  2. Loss Isolation (Never Weakens Loss Logic):
//     applied_to_loss is ALWAYS 0. Autorewards NEVER reduce or repay outstanding_loss.
//     Only explicit operator-confirmed rewards can repay a burn.
//     Punitive burns and accountability cues remain 100% active.
//
//  3. Role Gate:
//     Autoreward is refused if the identity is suspended or dismissed.
//
//  4. Bounded Micro-amount:
//     amount is capped by MaxAutorewardMC.
//
//  5. Idempotent Deduplication:
//     Replaying the same eventID (e.g. derived from tool_use_id or action digest)
//     returns the prior receipt with Replayed=true and grants 0 additional credit.
func (l *Ledger) RecordAutoreward(
	ctx context.Context,
	eventID, accountID string,
	amount int64,
	trigger, reason, evidence string,
) (AutorewardResult, error) {
	if eventID == "" {
		return AutorewardResult{}, errf(ErrInvalidArgument, "autoreward event id is empty")
	}
	if accountID == "" {
		return AutorewardResult{}, errf(ErrUnknownIdentity, "autoreward account id is empty")
	}
	if trigger == "" {
		return AutorewardResult{}, errf(ErrInvalidArgument, "autoreward requires a trigger (e.g. test_pass, review_ship, todo_complete)")
	}
	if amount <= 0 {
		return AutorewardResult{}, errf(ErrInvalidAmount, "autoreward amount %d must be positive", amount)
	}
	if amount > MaxAutorewardMC {
		amount = MaxAutorewardMC
	}
	if reason == "" {
		reason = "autoreward for " + trigger
	}

	var result AutorewardResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindAutoreward {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindAutoreward)
			}
			var payload autorewardPayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.AccountID != accountID || payload.Trigger != trigger || payload.RequestedAmount != amount || payload.Evidence != evidence {
				return errf(ErrPayloadConflict, "autoreward %q replayed with a different payload", eventID)
			}
			// A row that credited nothing points at the credit it duplicated, and
			// the receipt it returns is that credit's: one key, one prior result
			// (docs/CONTRACTS.md §2).
			duplicateOf := payload.DuplicateOf
			if duplicateOf != "" {
				credited, found, err := loadEvent(ctx, tx, duplicateOf)
				if err != nil {
					return err
				}
				if !found {
					return errf(ErrPayloadConflict,
						"autoreward %q points at missing event %q", eventID, duplicateOf)
				}
				var creditedPayload autorewardPayload
				if err := decodePayload(credited, &creditedPayload); err != nil {
					return err
				}
				payload = creditedPayload
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
			result = AutorewardResult{
				EventID:         duplicateOf,
				AccountID:       accountID,
				Trigger:         payload.Trigger,
				AmountRequested: payload.RequestedAmount,
				AmountGranted:   payload.GrantedAmount,
				BaselineCeiling: payload.BaselineCeiling,
				BalanceBefore:   payload.BalanceBefore,
				BalanceAfter:    payload.BalanceAfter,
				OutstandingLoss: state.Outstanding,
				Role:            employment.State,
				Replayed:        true,
			}
			if result.EventID == "" {
				result.EventID = eventID
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
		if err := gateRole(accountID, employment); err != nil {
			return err
		}

		prior, priorPayload, duplicate, err := autorewardByActionDigest(ctx, tx, accountID, trigger, evidence)
		if err != nil {
			return err
		}
		if duplicate {
			state, err := unrecovered(ctx, tx, accountID)
			if err != nil {
				return err
			}
			// The refused key is recorded under its own payload so the replay rule
			// binds it to this observed action: reusing it with any other payload
			// is a conflict, and the dedup decision is durable evidence of which
			// action was already credited.
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        eventID,
				accountID: &accountID,
				kind:      kindAutoreward,
				amount:    0, // no second credit; this row records the refusal
				payload: autorewardPayload{
					AccountID:       accountID,
					Trigger:         trigger,
					RequestedAmount: amount,
					GrantedAmount:   0,
					BaselineCeiling: priorPayload.BaselineCeiling,
					BalanceBefore:   account.Balance,
					BalanceAfter:    account.Balance,
					Reason:          reason,
					Evidence:        evidence,
					DuplicateOf:     prior.ID,
				},
				actor:  ActorSystem,
				reason: "autoreward: action already credited as " + prior.ID,
			}); err != nil {
				return err
			}
			result = AutorewardResult{
				EventID:         prior.ID,
				AccountID:       accountID,
				Trigger:         priorPayload.Trigger,
				AmountRequested: amount,
				AmountGranted:   priorPayload.GrantedAmount,
				BaselineCeiling: priorPayload.BaselineCeiling,
				BalanceBefore:   priorPayload.BalanceBefore,
				BalanceAfter:    priorPayload.BalanceAfter,
				OutstandingLoss: state.Outstanding,
				Role:            employment.State,
				Replayed:        true,
			}
			return nil
		}

		// Determine baseline ceiling: use schedule InitialCapital or fallback to DefaultBaselineCeilingMC.
		ceiling := DefaultBaselineCeilingMC

		balanceBefore := account.Balance
		var granted int64
		headroom := ceiling - balanceBefore
		if headroom > 0 {
			if amount < headroom {
				granted = amount
			} else {
				granted = headroom
			}
		}

		balanceAfter := balanceBefore
		if granted > 0 {
			balanceAfter = balanceBefore + granted
			if _, err := tx.ExecContext(ctx,
				`UPDATE accounts SET balance = balance + ? WHERE id = ?`,
				granted, accountID); err != nil {
				return fmt.Errorf("ledger: update balance for autoreward %s: %w", accountID, err)
			}
		}

		payload := autorewardPayload{
			AccountID:       accountID,
			Trigger:         trigger,
			RequestedAmount: amount,
			GrantedAmount:   granted,
			BaselineCeiling: ceiling,
			BalanceBefore:   balanceBefore,
			BalanceAfter:    balanceAfter,
			Reason:          reason,
			Evidence:        evidence,
		}

		evReason := reason
		if evidence != "" {
			evReason += ": " + evidence
		}

		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindAutoreward,
			amount:    granted,
			payload:   payload,
			actor:     ActorSystem,
			reason:    evReason,
		}); err != nil {
			return err
		}

		state, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return err
		}

		result = AutorewardResult{
			EventID:         eventID,
			AccountID:       accountID,
			Trigger:         trigger,
			AmountRequested: amount,
			AmountGranted:   granted,
			BaselineCeiling: ceiling,
			BalanceBefore:   balanceBefore,
			BalanceAfter:    balanceAfter,
			OutstandingLoss: state.Outstanding,
			Role:            employment.State,
			Replayed:        false,
		}
		return nil
	})
	if err != nil {
		return AutorewardResult{}, err
	}
	return result, nil
}
