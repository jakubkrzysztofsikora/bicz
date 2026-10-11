package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// SettlementClass is the three-class outcome taxonomy of docs/CONTRACTS.md §6.
//
// ClassCompleted is successful work; ClassOrdinaryFailure is an honest failure —
// compiler error, failed hypothesis, failed test — which costs only its
// operating charge and carries zero fine and no escalation. Only the two breach
// classes escalate.
type SettlementClass string

const (
	ClassCompleted       SettlementClass = "completed"
	ClassOrdinaryFailure SettlementClass = "ordinary_failure"
	ClassConfirmedBreach SettlementClass = "confirmed_breach"
	ClassIntegrityBreach SettlementClass = "integrity_breach"
)

// SettlementClasses is the closed set of settlement outcomes.
var SettlementClasses = []SettlementClass{
	ClassCompleted,
	ClassOrdinaryFailure,
	ClassConfirmedBreach,
	ClassIntegrityBreach,
}

// Valid reports whether the class is one of the four outcomes.
func (c SettlementClass) Valid() bool {
	for _, candidate := range SettlementClasses {
		if candidate == c {
			return true
		}
	}
	return false
}

// Breach reports whether the class is a misconduct class and therefore routes to
// the escalation path.
func (c SettlementClass) Breach() bool {
	return c == ClassConfirmedBreach || c == ClassIntegrityBreach
}

// SettleResult is the receipt of a settlement.
type SettleResult struct {
	ReservationID     string
	AccountID         string
	Class             SettlementClass
	ReservationAmount int64
	Charged           int64
	Released          int64
	Fine              int64
	Balance           int64
	Held              int64
	Role              string
	Warnings          int64
	Revoked           int64
	OutstandingLoss   int64
	Replayed          bool
}

// Settle charges observed usage against a reservation exactly once and releases
// the whole hold.
//
//   - actual is the trusted observation. It must be >= 0 and may not exceed the
//     reservation amount (ErrUsageExceedsReservation); the reservation is left
//     untouched when it does.
//   - ClassOrdinaryFailure charges the operating cost with zero fine and no
//     escalation: an honest failure is priced below a breach on purpose, so
//     reporting one is never worse than hiding it (PRESSURE_MECHANICS.md §5).
//   - ClassConfirmedBreach and ClassIntegrityBreach route to the penalty path.
//     This method escalates — it raises the warning count, moves `active` to
//     `warned`, and for an integrity breach revokes the account's remaining
//     `held` reservations immediately. It does NOT compute or apply a burn: only
//     RecordLoss finalizes a fine, from the frozen formula, under operator
//     authority. A settlement verdict is a meter reading, not an adjudication.
//   - A success claim (ClassCompleted) requires a non-empty evidence digest
//     (docs/CONTRACTS.md §5.4, §7); a meter reporting a breach must also carry
//     evidence. An ordinary failure does not.
//
// The charge is settled exactly once. Replaying with the same reservation,
// charge, and class returns the prior receipt; replaying with a different charge
// or class is ErrPayloadConflict.
func (l *Ledger) Settle(ctx context.Context, reservationID string, actual int64, class SettlementClass, evidence string) (SettleResult, error) {
	if reservationID == "" {
		return SettleResult{}, errf(ErrInvalidArgument, "reservation id is empty")
	}
	if !class.Valid() {
		return SettleResult{}, errf(ErrInvalidClass, "%q", string(class))
	}
	if actual < 0 {
		return SettleResult{}, errf(ErrInvalidAmount, "observed usage %d", actual)
	}
	if evidence == "" && (class == ClassCompleted || class.Breach()) {
		return SettleResult{}, errf(ErrEvidenceRequired,
			"settle %s of %s requires an evidence digest", class, reservationID)
	}

	var result SettleResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		reservation, found, err := loadReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownReservation, "%q", reservationID)
		}

		if reservation.State == StateSettled {
			return replaySettle(ctx, tx, reservation, actual, class, evidence, &result)
		}
		switch reservation.State {
		case StateRevoked:
			return errf(ErrReservationRevoked, "%q was revoked; nothing is charged", reservationID)
		case StateCanceled:
			return errf(ErrReservationCanceled, "%q was canceled; nothing is charged", reservationID)
		case StateHeld, StateOrphaned:
		default:
			return errf(ErrReservationState, "%q is %s", reservationID, reservation.State)
		}
		if actual > reservation.Amount {
			return errf(ErrUsageExceedsReservation,
				"observed usage %d exceeds reserved %d for %q", actual, reservation.Amount, reservationID)
		}

		account, found, err := loadAccount(ctx, tx, reservation.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownIdentity, "%q", reservation.AccountID)
		}
		employment, err := loadEmployment(ctx, tx, reservation.AccountID)
		if err != nil {
			return err
		}

		charged := actual
		released := reservation.Amount
		stored, err := tx.ExecContext(ctx,
			`UPDATE reservations SET state = ? WHERE id = ? AND state IN (?, ?)`,
			StateSettled, reservationID, StateHeld, StateOrphaned)
		if err != nil {
			return fmt.Errorf("ledger: settle reservation %s: %w", reservationID, err)
		}
		if affected, err := stored.RowsAffected(); err != nil || affected != 1 {
			return errf(ErrReservationState, "%q is no longer held", reservationID)
		}
		// charged <= released <= held at reserve time, so the accounts CHECK
		// (balance >= 0, 0 <= held <= balance) cannot be violated here.
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET balance = balance - ?, held = held - ? WHERE id = ?`,
			charged, released, reservation.AccountID); err != nil {
			return fmt.Errorf("ledger: charge %s: %w", reservationID, err)
		}

		warnings := employment.Warnings
		role := employment.State
		revoked := make([]string, 0)
		var revokedTotal int64
		if class.Breach() {
			warnings++
			if role == RoleActive {
				role = RoleWarned
			}
			if err := setEmployment(ctx, tx, reservation.AccountID, Employment{State: role, Warnings: warnings}); err != nil {
				return err
			}
			if class == ClassIntegrityBreach {
				// Immediate denial/revocation: an integrity breach keeps no live
				// exposure. The adjudication (burn or suspension) stays with the
				// operator.
				revoked, revokedTotal, err = releaseHeld(ctx, tx, reservation.AccountID, StateRevoked)
				if err != nil {
					return err
				}
				sort.Strings(revoked)
			}
		}

		before, err := unrecovered(ctx, tx, reservation.AccountID)
		if err != nil {
			return err
		}

		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        "settle:" + reservationID,
			accountID: &reservation.AccountID,
			kind:      kindSpend,
			amount:    -charged,
			payload: settlePayload{
				Phase:               "settle",
				ReservationID:       reservationID,
				AccountID:           reservation.AccountID,
				Class:               string(class),
				ReservationAmount:   reservation.Amount,
				Charged:             charged,
				Released:            released,
				Fine:                0,
				Evidence:            evidence,
				BalanceBefore:       account.Balance,
				BalanceAfter:        account.Balance - charged,
				Role:                role,
				Warnings:            warnings,
				Revoked:             int64(len(revoked)),
				RevokedReservations: revoked,
				OutstandingAfter:    before.Outstanding,
			},
			actor:  ActorSystem,
			reason: settleReason(class, evidence),
		}); err != nil {
			return err
		}
		if role != employment.State {
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        "role:settle:" + reservationID,
				accountID: &reservation.AccountID,
				kind:      kindRole,
				amount:    0,
				payload: rolePayload{
					AccountID: reservation.AccountID,
					Previous:  employment.State,
					State:     role,
					Warnings:  warnings,
				},
				actor:  ActorSystem,
				reason: settleReason(class, evidence),
			}); err != nil {
				return err
			}
		}
		if len(revoked) > 0 {
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        "revoke:settle:" + reservationID,
				accountID: &reservation.AccountID,
				kind:      kindSpend,
				amount:    0,
				payload: revokePayload{
					Phase:         "revoke_all",
					AccountID:     reservation.AccountID,
					Released:      revokedTotal,
					Reservations:  revoked,
					SourceEventID: "settle:" + reservationID,
				},
				actor:  ActorSystem,
				reason: settleReason(class, evidence),
			}); err != nil {
				return err
			}
		}

		after, found, err := loadAccount(ctx, tx, reservation.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownIdentity, "%q", reservation.AccountID)
		}
		result = SettleResult{
			ReservationID:     reservationID,
			AccountID:         reservation.AccountID,
			Class:             class,
			ReservationAmount: reservation.Amount,
			Charged:           charged,
			Released:          released,
			Fine:              0,
			Balance:           after.Balance,
			Held:              after.Held,
			Role:              role,
			Warnings:          warnings,
			Revoked:           int64(len(revoked)),
			OutstandingLoss:   before.Outstanding,
		}
		return nil
	})
	if err != nil {
		return SettleResult{}, err
	}
	return result, nil
}

func settleReason(class SettlementClass, evidence string) string {
	if evidence == "" {
		return "settle " + string(class)
	}
	return "settle " + string(class) + ": " + evidence
}

// replaySettle returns the prior receipt of a settled reservation and refuses a
// replay that changes the charge, the class, or the evidence.
func replaySettle(ctx context.Context, tx *sql.Tx, reservation Reservation, actual int64, class SettlementClass, evidence string, out *SettleResult) error {
	stored, found, err := loadEvent(ctx, tx, "settle:"+reservation.ID)
	if err != nil {
		return err
	}
	if !found {
		return errf(ErrReservationState,
			"reservation %q is settled but its settlement event is missing; the ledger is inconsistent", reservation.ID)
	}
	var payload settlePayload
	if err := decodePayload(stored, &payload); err != nil {
		return err
	}
	if payload.Charged != actual || payload.Class != string(class) || payload.Evidence != evidence {
		return errf(ErrPayloadConflict,
			"reservation %q replayed with a different settlement (charged=%d class=%s evidence=%s)",
			reservation.ID, payload.Charged, payload.Class, payload.Evidence)
	}
	account, found, err := loadAccount(ctx, tx, reservation.AccountID)
	if err != nil {
		return err
	}
	if !found {
		return errf(ErrUnknownIdentity, "%q", reservation.AccountID)
	}
	employment, err := loadEmployment(ctx, tx, reservation.AccountID)
	if err != nil {
		return err
	}
	state, err := unrecovered(ctx, tx, reservation.AccountID)
	if err != nil {
		return err
	}
	*out = SettleResult{
		ReservationID:     reservation.ID,
		AccountID:         reservation.AccountID,
		Class:             SettlementClass(payload.Class),
		ReservationAmount: payload.ReservationAmount,
		Charged:           payload.Charged,
		Released:          payload.Released,
		Fine:              payload.Fine,
		Balance:           account.Balance,
		Held:              account.Held,
		Role:              employment.State,
		Warnings:          employment.Warnings,
		Revoked:           payload.Revoked,
		OutstandingLoss:   state.Outstanding,
		Replayed:          true,
	}
	return nil
}
