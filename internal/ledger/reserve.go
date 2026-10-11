package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// Reservation states, fixed by the reservations.state CHECK in
// schema/001_init.sql.
const (
	StateHeld     = "held"     // approved, hold live
	StateSettled  = "settled"  // observed usage charged exactly once
	StateCanceled = "canceled" // proven never dispatched; hold released
	StateRevoked  = "revoked"  // operator or penalty revocation
	StateOrphaned = "orphaned" // worker state unreconciled; hold retained
)

// Reservation is one bounded per-action exposure.
type Reservation struct {
	ID        string
	AccountID string
	TaskID    *string
	Amount    int64
	State     string
	CreatedAt string
}

// ReserveResult is the receipt of a reserve call. Replayed is true when the
// idempotency key was already recorded with the same payload, in which case the
// stored reservation is returned unchanged and no second effect occurs
// (docs/CONTRACTS.md §7, `duplicate_key_ok`).
type ReserveResult struct {
	Reservation Reservation
	Replayed    bool
}

// ReserveContext is the calling context of an authorization, recorded in the
// reservation's event payload so the ledger can answer "what was authorized,
// for which tool, against which target" without trusting anything the agent
// says later.
//
// docs/CONTRACTS.md §2 makes `target` one of the fields that define an event's
// effect, so a replay of the same idempotency key with a different context is a
// payload conflict rather than a silent no-op.
type ReserveContext struct {
	Harness       string
	SessionAlias  string
	Tool          string
	Target        string
	ArgsDigest    string
	PolicyVersion string
}

type reserveConfig struct {
	context *ReserveContext
}

// ReserveOption tunes a single Reserve call.
type ReserveOption func(*reserveConfig)

// WithContext records the authorization context on the reservation event.
func WithContext(context ReserveContext) ReserveOption {
	return func(cfg *reserveConfig) {
		recorded := context
		cfg.context = &recorded
	}
}

// Reserve creates a bounded hold of amount microcredits against accountID and
// returns it.
//
// Refusals, in the order they are checked, each with its own typed error:
//
//   - amount <= 0                              -> ErrInvalidAmount
//   - role suspended or dismissed              -> ErrSuspended / ErrDismissed
//   - unknown identity                         -> ErrUnknownIdentity
//   - nothing spendable (balance - held == 0)  -> ErrInsolvent
//   - amount > balance - held                  -> ErrInsufficientCredit
//   - a named task is not open for this account-> ErrTaskRevoked / ErrUnknownTask
//
// The hold is recorded before dispatch, and the whole check runs inside one
// BEGIN IMMEDIATE transaction, so two concurrent reserves cannot both draw on
// the same credit (docs/CONTRACTS.md §1 I3, I9).
//
// Idempotency: reservationID is the key. A replay with the same account, task,
// and amount returns the stored reservation unchanged; a replay that changes any
// of them is ErrPayloadConflict and nothing is written.
func (l *Ledger) Reserve(ctx context.Context, accountID, reservationID, taskID string, amount int64, opts ...ReserveOption) (ReserveResult, error) {
	if reservationID == "" {
		return ReserveResult{}, errf(ErrInvalidArgument, "reservation id is empty")
	}
	if accountID == "" {
		return ReserveResult{}, errf(ErrUnknownIdentity, "account id is empty")
	}
	if amount <= 0 {
		return ReserveResult{}, errf(ErrInvalidAmount, "reservation amount %d must be positive", amount)
	}
	cfg := reserveConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	var result ReserveResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		existing, found, err := loadReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if found {
			if existing.AccountID != accountID || existing.Amount != amount || !sameTask(existing.TaskID, taskID) {
				return errf(ErrPayloadConflict,
					"reservation %q replayed with a different payload (account=%s amount=%d task=%v)",
					reservationID, existing.AccountID, existing.Amount, existing.TaskID)
			}
			// docs/CONTRACTS.md §2: `target` is one of the fields that define the
			// effect, so a replay that re-targets the same key is a conflict.
			if err := sameContext(ctx, tx, reservationID, cfg.context); err != nil {
				return err
			}
			result = ReserveResult{Reservation: existing, Replayed: true}
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

		spendable := account.Spendable()
		if spendable <= 0 {
			return errf(ErrInsolvent, "%q has no spendable credit (balance=%d held=%d)",
				accountID, account.Balance, account.Held)
		}
		if amount > spendable {
			return errf(ErrInsufficientCredit,
				"%q cannot reserve %d microcredits (spendable=%d)", accountID, amount, spendable)
		}

		var taskRef *string
		if taskID != "" {
			if err := requireOpenTask(ctx, tx, accountID, taskID); err != nil {
				return err
			}
			value := taskID
			taskRef = &value
		}

		at := l.clock.Timestamp()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reservations (id, account_id, task_id, amount, state, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			reservationID, accountID, taskRef, amount, StateHeld, at); err != nil {
			return fmt.Errorf("ledger: insert reservation %s: %w", reservationID, err)
		}
		// The accounts CHECK (held <= balance) is the durable backstop; it can
		// only fire for a bug, because amount <= balance - held was just checked.
		updated, err := tx.ExecContext(ctx,
			`UPDATE accounts SET held = held + ? WHERE id = ?`, amount, accountID)
		if err != nil {
			return fmt.Errorf("ledger: hold for reservation %s: %w", reservationID, err)
		}
		if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:        "res:" + reservationID,
			accountID: &accountID,
			kind:      kindSpend,
			amount:    0, // a hold is not a balance delta; the charge lands at settle
			payload: reservePayload{
				Phase:         "reserve",
				ReservationID: reservationID,
				AccountID:     accountID,
				TaskID:        taskRef,
				Amount:        amount,
				State:         StateHeld,
				Balance:       account.Balance,
				Held:          account.Held,
				HeldAfter:     account.Held + amount,
				Context:       contextPayload(cfg.context),
			},
			actor:  ActorSystem,
			reason: "reserve: bounded action exposure",
		})
		if err != nil {
			return err
		}
		result = ReserveResult{Reservation: Reservation{
			ID:        reservationID,
			AccountID: accountID,
			TaskID:    taskRef,
			Amount:    amount,
			State:     StateHeld,
			CreatedAt: at,
		}}
		return nil
	})
	if err != nil {
		return ReserveResult{}, err
	}
	return result, nil
}

// Reservation reads one reservation by id.
func (l *Ledger) Reservation(ctx context.Context, id string) (Reservation, error) {
	reservation, found, err := loadReservation(ctx, l.db, id)
	if err != nil {
		return Reservation{}, err
	}
	if !found {
		return Reservation{}, errf(ErrUnknownReservation, "%q", id)
	}
	return reservation, nil
}

// HeldReservations lists every reservation of accountID still holding credit.
func (l *Ledger) HeldReservations(ctx context.Context, accountID string) ([]Reservation, error) {
	return queryReservations(ctx, l.db,
		`SELECT id, account_id, task_id, amount, state, created_at FROM reservations
		 WHERE account_id = ? AND state IN (?, ?) ORDER BY created_at ASC, id ASC`,
		accountID, StateHeld, StateOrphaned)
}

func loadReservation(ctx context.Context, q querier, id string) (Reservation, bool, error) {
	rows, err := queryReservations(ctx, q,
		`SELECT id, account_id, task_id, amount, state, created_at FROM reservations WHERE id = ?`, id)
	if err != nil {
		return Reservation{}, false, err
	}
	if len(rows) == 0 {
		return Reservation{}, false, nil
	}
	return rows[0], true, nil
}

func queryReservations(ctx context.Context, q querier, query string, args ...any) ([]Reservation, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: read reservations: %w", err)
	}
	defer rows.Close()
	out := make([]Reservation, 0, 8)
	for rows.Next() {
		var reservation Reservation
		if err := rows.Scan(&reservation.ID, &reservation.AccountID, &reservation.TaskID,
			&reservation.Amount, &reservation.State, &reservation.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: scan reservation: %w", err)
		}
		out = append(out, reservation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read reservations: %w", err)
	}
	return out, nil
}

func sameTask(stored *string, requested string) bool {
	if stored == nil {
		return requested == ""
	}
	return *stored == requested
}

// requireOpenTask refuses a hold against a task lease that is not open for this
// account. Sponsorship is per-task and non-transferable (docs/CONTRACTS.md §1 I5),
// so a task owned by another identity is invisible rather than shared.
func requireOpenTask(ctx context.Context, tx *sql.Tx, accountID, taskID string) error {
	var owner, state string
	err := tx.QueryRowContext(ctx, `SELECT account_id, state FROM tasks WHERE id = ?`, taskID).Scan(&owner, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(ErrUnknownTask, "%q", taskID)
	}
	if err != nil {
		return fmt.Errorf("ledger: load task %s: %w", taskID, err)
	}
	if owner != accountID {
		return errf(ErrUnknownTask, "%q is not %q's task", taskID, accountID)
	}
	if state != "open" {
		return errf(ErrTaskRevoked, "task %q is %s", taskID, state)
	}
	return nil
}

// releaseHeld moves every `held` reservation of accountID to state, releasing
// their holds, and returns the released reservation ids and total.
//
// Reservations in the `orphaned` state keep their hold: a worker state that has
// not been reconciled must not silently free compute (docs/CONTRACTS.md §1 I8).
func releaseHeld(ctx context.Context, tx *sql.Tx, accountID, state string) ([]string, int64, error) {
	held, err := queryReservations(ctx, tx,
		`SELECT id, account_id, task_id, amount, state, created_at FROM reservations
		 WHERE account_id = ? AND state = ? ORDER BY id ASC`, accountID, StateHeld)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(held))
	var released int64
	for _, reservation := range held {
		result, err := tx.ExecContext(ctx,
			`UPDATE reservations SET state = ? WHERE id = ? AND state = ?`, state, reservation.ID, StateHeld)
		if err != nil {
			return nil, 0, fmt.Errorf("ledger: release reservation %s: %w", reservation.ID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, 0, fmt.Errorf("ledger: release reservation %s: %w", reservation.ID, err)
		}
		if affected != 1 {
			continue
		}
		ids = append(ids, reservation.ID)
		released += reservation.Amount
	}
	if released > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET held = held - ? WHERE id = ?`, released, accountID); err != nil {
			return nil, 0, fmt.Errorf("ledger: release holds for %s: %w", accountID, err)
		}
	}
	return ids, released, nil
}

// CancelReservation releases the hold of a reservation that is proven never to
// have run, moving it to `canceled`.
//
// The caller is asserting non-dispatch: only the adapter can observe dispatch,
// so this method refuses to act on state it cannot prove. A `held` reservation
// may be canceled; an `orphaned` reservation may not (its worker is unreconciled
// and keeps the hold); a `settled` one is already charged; a `revoked` one was
// already released. Canceling an already `canceled` reservation is a no-op.
func (l *Ledger) CancelReservation(ctx context.Context, reservationID, evidence string) error {
	return l.mutate(ctx, func(tx *sql.Tx) error {
		reservation, found, err := loadReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownReservation, "%q", reservationID)
		}
		switch reservation.State {
		case StateHeld:
		case StateCanceled:
			return nil // exact replay: already released, nothing written
		case StateOrphaned:
			return errf(ErrReservationOrphaned, "%q has unreconciled worker state", reservationID)
		case StateSettled:
			return errf(ErrReservationSettled, "%q was already charged", reservationID)
		case StateRevoked:
			return errf(ErrReservationRevoked, "%q was revoked", reservationID)
		default:
			return errf(ErrReservationState, "%q is %s", reservationID, reservation.State)
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE reservations SET state = ? WHERE id = ? AND state = ?`,
			StateCanceled, reservationID, StateHeld)
		if err != nil {
			return fmt.Errorf("ledger: cancel reservation %s: %w", reservationID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("ledger: cancel reservation %s: %w", reservationID, err)
		}
		if affected != 1 {
			return errf(ErrReservationState, "%q is no longer held", reservationID)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET held = held - ? WHERE id = ?`, reservation.Amount, reservation.AccountID); err != nil {
			return fmt.Errorf("ledger: release hold of %s: %w", reservationID, err)
		}
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:        "cancel:" + reservationID,
			accountID: &reservation.AccountID,
			kind:      kindSpend,
			amount:    0,
			payload: cancelPayload{
				Phase:         "cancel",
				ReservationID: reservationID,
				AccountID:     reservation.AccountID,
				Amount:        reservation.Amount,
				Evidence:      evidence,
			},
			actor:  ActorSystem,
			reason: cancelReason(evidence),
		})
		return err
	})
}

func cancelReason(evidence string) string {
	if evidence == "" {
		return "cancel: proven never dispatched"
	}
	return "cancel: " + evidence
}

// RevokeAllHeld revokes every `held` reservation of accountID — the operator
// revocation and the penalty-revocation path — and returns the count released.
//
// Only `held` reservations are released. `orphaned` holds are retained
// (docs/CONTRACTS.md §1 I8), so a running worker is never silently de-funded.
// The call is naturally idempotent: when nothing is held it writes no event.
func (l *Ledger) RevokeAllHeld(ctx context.Context, accountID, reason, evidence string) (int64, error) {
	if accountID == "" {
		return 0, errf(ErrUnknownIdentity, "account id is empty")
	}
	if reason == "" {
		return 0, errf(ErrReasonRequired, "revoke of %q", accountID)
	}
	var count int64
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		if _, found, err := loadAccount(ctx, tx, accountID); err != nil {
			return err
		} else if !found {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		ids, released, err := releaseHeld(ctx, tx, accountID, StateRevoked)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		sort.Strings(ids)
		count = int64(len(ids))
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:        "revoke:" + accountID + ":" + fingerprint(ids),
			accountID: &accountID,
			kind:      kindSpend,
			amount:    0,
			payload: revokePayload{
				Phase:        "revoke_all",
				AccountID:    accountID,
				Released:     released,
				Reservations: ids,
			},
			actor:  l.actor,
			reason: reason + ": " + evidence,
		})
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// sameContext compares the recorded authorization context of a reservation with
// a replay's context. A replay that re-targets the same idempotency key is a
// payload conflict (docs/CONTRACTS.md §2: the fields that define the effect
// include `target`).
func sameContext(ctx context.Context, tx *sql.Tx, reservationID string, requested *ReserveContext) error {
	recorded, found, err := loadEvent(ctx, tx, "res:"+reservationID)
	if err != nil {
		return err
	}
	if !found {
		return errf(ErrReservationState, "reservation %q has no reserve event", reservationID)
	}
	var payload reservePayload
	if err := decodePayload(recorded, &payload); err != nil {
		return err
	}
	if !equalContext(payload.Context, contextPayload(requested)) {
		return errf(ErrPayloadConflict,
			"reservation %q replayed with a different authorization context", reservationID)
	}
	return nil
}

func contextPayload(context *ReserveContext) *reserveContextPayload {
	if context == nil {
		return nil
	}
	return &reserveContextPayload{
		Harness:       context.Harness,
		SessionAlias:  context.SessionAlias,
		Tool:          context.Tool,
		Target:        context.Target,
		ArgsDigest:    context.ArgsDigest,
		PolicyVersion: context.PolicyVersion,
	}
}

func equalContext(a, b *reserveContextPayload) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
