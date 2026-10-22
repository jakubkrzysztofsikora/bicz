package ledger

import (
	"errors"
	"fmt"
)

// Error is a typed ledger failure. Code is the stable adapter error code from
// docs/CONTRACTS.md §5.5, so callers map a failure onto the protocol vocabulary
// without string matching, and every distinct refusal is programmatically
// distinguishable (suspended vs dismissed vs insolvent, and so on).
type Error struct {
	Code string
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	switch {
	case e.Msg == "" && e.Err == nil:
		return e.Code
	case e.Err == nil:
		return e.Code + ": " + e.Msg
	case e.Msg == "":
		return e.Code + ": " + e.Err.Error()
	default:
		return e.Code + ": " + e.Msg + ": " + e.Err.Error()
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches on Code, so errors.Is(err, ErrInsolvent) is true for any value
// carrying that code however it was wrapped and re-messaged.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

// Code returns the stable error code of err, or "" when err is not a ledger
// error. It is the single mapping point from Go errors to the adapter contract.
func Code(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func errf(template *Error, format string, args ...any) *Error {
	return &Error{Code: template.Code, Msg: fmt.Sprintf(format, args...)}
}

func errw(template *Error, err error) *Error {
	return &Error{Code: template.Code, Msg: template.Msg, Err: err}
}

// Sentinel ledger failures. Codes marked "contract" come from the frozen error
// vocabulary in docs/CONTRACTS.md §5.5; the rest are internal invariants that
// have no adapter-visible code yet.
var (
	ErrInvalidArgument         = &Error{Code: "invalid_argument", Msg: "invalid argument"}
	ErrUnknownIdentity         = &Error{Code: "unknown_identity", Msg: "unknown identity"}                     // contract
	ErrInsufficientCredit      = &Error{Code: "insufficient_credit", Msg: "insufficient credit"}               // contract
	ErrInsolvent               = &Error{Code: "insolvent", Msg: "insolvent: no spendable credit"}              // contract
	ErrSuspended               = &Error{Code: "suspended", Msg: "identity is suspended"}                       // contract
	ErrDismissed               = &Error{Code: "dismissed", Msg: "identity is dismissed"}                       // contract
	ErrGuardBandNarrowed       = &Error{Code: "guard_band_narrowed", Msg: "identity is inside the guard band"} // contract
	ErrUnknownTask             = &Error{Code: "unknown_task", Msg: "unknown task"}
	ErrTaskRevoked             = &Error{Code: "task_revoked", Msg: "task lease is not open"}         // contract
	ErrUnknownReservation      = &Error{Code: "unknown_reservation", Msg: "unknown reservation"}     // contract
	ErrReservationRevoked      = &Error{Code: "reservation_revoked", Msg: "reservation was revoked"} // contract
	ErrReservationCanceled     = &Error{Code: "reservation_canceled", Msg: "reservation was canceled"}
	ErrReservationOrphaned     = &Error{Code: "reservation_orphaned", Msg: "reservation is orphaned"} // contract
	ErrReservationSettled      = &Error{Code: "reservation_settled", Msg: "reservation is already settled"}
	ErrReservationState        = &Error{Code: "reservation_state", Msg: "reservation is not in a settle-able state"}
	ErrUsageExceedsReservation = &Error{Code: "usage_exceeds_reservation", Msg: "observed usage exceeds reservation"}       // contract
	ErrPayloadConflict         = &Error{Code: "payload_conflict", Msg: "idempotency key replayed with a different payload"} // contract
	ErrReasonRequired          = &Error{Code: "reason_required", Msg: "a non-empty reason is required"}                     // contract
	ErrEvidenceRequired        = &Error{Code: "evidence_required", Msg: "a non-empty evidence digest is required"}
	ErrInvalidAmount           = &Error{Code: "invalid_amount", Msg: "amount must be a positive integer count of microcredits"}
	ErrInvalidClass            = &Error{Code: "invalid_class", Msg: "unknown settlement class"}
	ErrRuleRequired            = &Error{Code: "rule_required", Msg: "loss requires a non-empty rule"}
	ErrCorrectionRequired      = &Error{Code: "correction_required", Msg: "loss requires a non-empty correction"}
	ErrDismissalTerminal       = &Error{Code: "dismissal_terminal", Msg: "dismissal is terminal without an explicit operator reinstatement"}
	ErrInvalidRoleState        = &Error{Code: "invalid_role_state", Msg: "unknown employment state"}
	ErrUnknownPolicyVersion    = &Error{Code: "unknown_policy_version", Msg: "unknown price schedule version"}
	ErrPolicyBody              = &Error{Code: "invalid_policy_body", Msg: "price schedule body is not a valid policy"}
	ErrUnknownEvent            = &Error{Code: "unknown_event", Msg: "unknown event"}
	ErrNotCompensable          = &Error{Code: "not_compensable", Msg: "event has no compensable amount"}
	ErrAlreadyCompensated      = &Error{Code: "already_compensated", Msg: "event was already compensated; credits are restored at most once"}
	ErrExceedsOriginal         = &Error{Code: "exceeds_original", Msg: "compensation exceeds the original event amount"}
	ErrSchemaTooNew            = &Error{Code: "schema_too_new", Msg: "database schema is newer than this binary"}
	ErrNothingToAnchor         = &Error{Code: "nothing_to_anchor", Msg: "no new events since the last block"}
	ErrActorRequired           = &Error{Code: "actor_required", Msg: "actor is required"}
)
