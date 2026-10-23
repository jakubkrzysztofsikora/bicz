package ledger

import (
	"context"
	"database/sql"
)

// RoleResult is the receipt of a role transition.
type RoleResult struct {
	EventID       string
	AccountID     string
	Previous      string
	State         string
	Warnings      int64
	Reinstatement bool
	Replayed      bool
}

type roleConfig struct {
	reinstated bool
}

// RoleOption tunes a single SetRole call.
type RoleOption func(*roleConfig)

// Reinstate is the explicit operator reinstatement that is the only exit from
// `dismissed` (docs/CONTRACTS.md §1 I4, §4 "operator reinstate"). Without it, a
// transition of a dismissed identity to any other state is refused.
func Reinstate() RoleOption {
	return func(cfg *roleConfig) { cfg.reinstated = true }
}

// SetRole records an operator role transition for accountID.
//
//   - `dismissed` is terminal. Moving a dismissed identity to any other state
//     fails with ErrDismissalTerminal unless Reinstate() is passed; nothing in
//     this package, and no ledger event, can exit dismissal implicitly.
//   - `warnings` accumulate: every transition into `warned` increments the count,
//     so a repeat escalation is visible as a raised count rather than an
//     unchanged state.
//   - transitionKey is the idempotency key and becomes the event id. A replay
//     with the same target state that is also a no-op; a replay naming a
//     different state, or a key already used for another kind of event, is
//     ErrPayloadConflict.
//
// Changing a role never changes a balance, and a compensation never changes a
// role: the two are separate events by construction.
func (l *Ledger) SetRole(
	ctx context.Context,
	accountID, state, reason, evidence, transitionKey string,
	opts ...RoleOption,
) (RoleResult, error) {
	cfg := roleConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if transitionKey == "" {
		return RoleResult{}, errf(ErrInvalidArgument, "role transition key is empty")
	}
	if accountID == "" {
		return RoleResult{}, errf(ErrUnknownIdentity, "role account id is empty")
	}
	if !ValidRole(state) {
		return RoleResult{}, errf(ErrInvalidRoleState, "%q", state)
	}
	if reason == "" {
		return RoleResult{}, errf(ErrReasonRequired, "role transition %q", transitionKey)
	}

	var result RoleResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, transitionKey)
		if err != nil {
			return err
		}
		if found {
			if stored.Kind != kindRole {
				return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", transitionKey, stored.Kind, kindRole)
			}
			var payload rolePayload
			if err := decodePayload(stored, &payload); err != nil {
				return err
			}
			if payload.AccountID != accountID || payload.State != state || stored.Reason != reason+": "+evidence {
				return errf(ErrPayloadConflict, "role transition %q replayed with a different payload", transitionKey)
			}
			employment, err := loadEmployment(ctx, tx, accountID)
			if err != nil {
				return err
			}
			result = RoleResult{
				EventID:       transitionKey,
				AccountID:     accountID,
				Previous:      payload.Previous,
				State:         payload.State,
				Warnings:      employment.Warnings,
				Reinstatement: payload.Reinstatement,
				Replayed:      true,
			}
			return nil
		}

		employment, err := loadEmployment(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if employment.State == RoleDismissed && state != RoleDismissed && !cfg.reinstated {
			return errf(ErrDismissalTerminal, "%q is dismissed; an explicit operator reinstatement is required", accountID)
		}
		warnings := employment.Warnings
		if state == RoleWarned {
			warnings++
		}
		// Reinstate clears the recorded pain: the warnings that raised it are
		// operator-cleared history, and the penalty record itself stays (the loss
		// events and any outstanding shortfall are untouched).
		if cfg.reinstated || state == RoleActive && employment.State != RoleActive {
			warnings = 0
		}
		if err := setEmployment(ctx, tx, accountID, Employment{State: state, Warnings: warnings}); err != nil {
			return err
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        transitionKey,
			accountID: &accountID,
			kind:      kindRole,
			amount:    0,
			payload: rolePayload{
				AccountID:     accountID,
				Previous:      employment.State,
				State:         state,
				Warnings:      warnings,
				Reinstatement: cfg.reinstated && employment.State == RoleDismissed,
			},
			actor:  l.actor,
			reason: reason + ": " + evidence,
		}); err != nil {
			return err
		}
		result = RoleResult{
			EventID:       transitionKey,
			AccountID:     accountID,
			Previous:      employment.State,
			State:         state,
			Warnings:      warnings,
			Reinstatement: cfg.reinstated && employment.State == RoleDismissed,
		}
		return nil
	})
	if err != nil {
		return RoleResult{}, err
	}
	return result, nil
}

// Employment reads an identity's role state.
func (l *Ledger) Employment(ctx context.Context, accountID string) (Employment, error) {
	return loadEmployment(ctx, l.db, accountID)
}
