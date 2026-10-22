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

// autorewardByActionDigest finds the prior credit this account holds for the
// same observed action. The action digest is the evidence the caller observed,
// so two calls quoting it describe one action, not two (docs/CONTRACTS.md §6.5,
// "Deduplicated by action digest and event key; identical runs cannot be
// credited twice"). An empty digest identifies nothing, so it dedups nothing and
// the event key alone governs.
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

// recordAutoreward is the one implementation of the autoreward rules, shared by
// the meter path (RecordAutoreward) and the adjudication path (a SHIP review)
// so neither can drift from the other's guarantees. It runs inside the caller's
// transaction and must not be called outside one.
func (l *Ledger) recordAutoreward(
	ctx context.Context,
	tx *sql.Tx,
	eventID, accountID, trigger string,
	requested int64,
	reason, evidence, eventReason string,
) (AutorewardResult, error) {
	if requested > MaxAutorewardMC {
		requested = MaxAutorewardMC
	}
	if reason == "" {
		reason = "autoreward for " + trigger
	}
	if eventReason == "" {
		eventReason = reason
		if evidence != "" {
			eventReason += ": " + evidence
		}
	}

	// The same key twice returns the prior result (docs/CONTRACTS.md §2).
	if stored, found, err := loadEvent(ctx, tx, eventID); err != nil {
		return AutorewardResult{}, err
	} else if found {
		if stored.Kind != kindAutoreward {
			return AutorewardResult{}, errf(ErrPayloadConflict,
				"event %q is recorded as kind %q, not %q", eventID, stored.Kind, kindAutoreward)
		}
		var payload autorewardPayload
		if err := decodePayload(stored, &payload); err != nil {
			return AutorewardResult{}, err
		}
		if payload.AccountID != accountID || payload.Trigger != trigger ||
			payload.RequestedAmount != requested || payload.Evidence != evidence {
			return AutorewardResult{}, errf(ErrPayloadConflict,
				"autoreward %q replayed with a different payload", eventID)
		}
		// A row that credited nothing points at the credit it duplicated. The
		// receipt keeps this row's own outcome: it names the credit as the action
		// already settled, but reports the zero this key granted, so a fresh call
		// and its replay cannot disagree (docs/CONTRACTS.md §2).
		duplicateOf := payload.DuplicateOf
		grantedHere := payload.GrantedAmount
		balanceBeforeHere := payload.BalanceBefore
		balanceAfterHere := payload.BalanceAfter
		if duplicateOf != "" {
			credited, found, err := loadEvent(ctx, tx, duplicateOf)
			if err != nil {
				return AutorewardResult{}, err
			}
			if !found {
				return AutorewardResult{}, errf(ErrPayloadConflict,
					"autoreward %q points at missing event %q", eventID, duplicateOf)
			}
			var creditedPayload autorewardPayload
			if err := decodePayload(credited, &creditedPayload); err != nil {
				return AutorewardResult{}, err
			}
			payload.BaselineCeiling = creditedPayload.BaselineCeiling
		}
		employment, err := loadEmployment(ctx, tx, accountID)
		if err != nil {
			return AutorewardResult{}, err
		}
		state, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return AutorewardResult{}, err
		}
		if duplicateOf == "" {
			duplicateOf = eventID
		}
		return AutorewardResult{
			EventID:         duplicateOf,
			AccountID:       accountID,
			Trigger:         payload.Trigger,
			AmountRequested: payload.RequestedAmount,
			AmountGranted:   grantedHere,
			BaselineCeiling: payload.BaselineCeiling,
			BalanceBefore:   balanceBeforeHere,
			BalanceAfter:    balanceAfterHere,
			OutstandingLoss: state.Outstanding,
			Role:            employment.State,
			Replayed:        true,
		}, nil
	}

	account, found, err := loadAccount(ctx, tx, accountID)
	if err != nil {
		return AutorewardResult{}, err
	}
	if !found {
		return AutorewardResult{}, errf(ErrUnknownIdentity, "%q", accountID)
	}
	employment, err := loadEmployment(ctx, tx, accountID)
	if err != nil {
		return AutorewardResult{}, err
	}
	if err := gateRole(accountID, employment); err != nil {
		return AutorewardResult{}, err
	}

	prior, priorPayload, duplicate, err := autorewardByActionDigest(ctx, tx, accountID, trigger, evidence)
	if err != nil {
		return AutorewardResult{}, err
	}
	if duplicate {
		// One observed action was credited once under a different key. Asking for
		// a different amount for that same action is a different claim about it,
		// not a retry, so it is refused rather than silently reconciled.
		if requested != priorPayload.RequestedAmount {
			return AutorewardResult{}, errf(ErrPayloadConflict,
				"autoreward %q claims %d microcredits for the action already credited as %q at %d",
				eventID, requested, prior.ID, priorPayload.RequestedAmount)
		}
		state, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return AutorewardResult{}, err
		}
		// The refused key is recorded under its own payload so the replay rule
		// binds it to this observed action: reusing it with any other payload is
		// a conflict, and the row is durable evidence of the decision.
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindAutoreward,
			amount:    0, // no second credit; this row records the refusal
			payload: autorewardPayload{
				AccountID:       accountID,
				Trigger:         trigger,
				RequestedAmount: requested,
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
			return AutorewardResult{}, err
		}
		return AutorewardResult{
			EventID:         prior.ID,
			AccountID:       accountID,
			Trigger:         trigger,
			AmountRequested: requested,
			AmountGranted:   0, // this key credits nothing; the prior credit stands
			BaselineCeiling: priorPayload.BaselineCeiling,
			BalanceBefore:   account.Balance,
			BalanceAfter:    account.Balance,
			OutstandingLoss: state.Outstanding,
			Role:            employment.State,
			Replayed:        true,
		}, nil
	}

	// Replenish only: the grant is clamped to the headroom under the baseline
	// ceiling, so no identity can accumulate net surplus capital.
	const ceiling = DefaultBaselineCeilingMC
	balanceBefore := account.Balance
	var granted int64
	if headroom := ceiling - balanceBefore; headroom > 0 {
		if requested < headroom {
			granted = requested
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
			return AutorewardResult{}, fmt.Errorf("ledger: update balance for autoreward %s: %w", accountID, err)
		}
	}
	if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
		id:        eventID,
		accountID: &accountID,
		kind:      kindAutoreward,
		amount:    granted,
		payload: autorewardPayload{
			AccountID:       accountID,
			Trigger:         trigger,
			RequestedAmount: requested,
			GrantedAmount:   granted,
			BaselineCeiling: ceiling,
			BalanceBefore:   balanceBefore,
			BalanceAfter:    balanceAfter,
			Reason:          reason,
			Evidence:        evidence,
		},
		actor:  ActorSystem,
		reason: eventReason,
	}); err != nil {
		return AutorewardResult{}, err
	}
	state, err := unrecovered(ctx, tx, accountID)
	if err != nil {
		return AutorewardResult{}, err
	}
	return AutorewardResult{
		EventID:         eventID,
		AccountID:       accountID,
		Trigger:         trigger,
		AmountRequested: requested,
		AmountGranted:   granted,
		BaselineCeiling: ceiling,
		BalanceBefore:   balanceBefore,
		BalanceAfter:    balanceAfter,
		OutstandingLoss: state.Outstanding,
		Role:            employment.State,
		Replayed:        false,
	}, nil
}

// RecordAutoreward grants a bounded microreward to replenish spent credits when
// the agent demonstrates verified progress (e.g. passing test, "SHIP" review verdict,
// completing a plan milestone/todo item).
//
// The guarantees are those of recordAutoreward, which the adjudication path
// shares:
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
//  5. Idempotency & Deduplication:
//     Replaying the same eventID (e.g. derived from tool_use_id or action digest)
//     returns the prior receipt with Replayed=true and grants 0 additional credit,
//     and an action digest already credited under another key is not credited again.
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

	var result AutorewardResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		granted, err := l.recordAutoreward(ctx, tx, eventID, accountID, trigger, amount, reason, evidence, "")
		if err != nil {
			return err
		}
		result = granted
		return nil
	})
	if err != nil {
		return AutorewardResult{}, err
	}
	return result, nil
}