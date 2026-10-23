package protocol

import (
	"encoding/json"
	"errors"
)

// Code is a stable, machine-readable failure code.
//
// Every ledger-level refusal keeps the exact code the contract fixes in
// docs/CONTRACTS.md §5.5 / §7, so an adapter can switch on the code and never on
// message text. Codes outside that list are the ledger's own internal
// invariants; they are carried through unchanged rather than collapsed into a
// generic failure, because collapsing them would hide which invariant fired.
type Code string

// The contract's error vocabulary (docs/CONTRACTS.md §5.5).
const (
	CodeUnknownIdentity         Code = "unknown_identity"
	CodeInsufficientCredit      Code = "insufficient_credit"
	CodeInsolvent               Code = "insolvent"
	CodeSuspended               Code = "suspended"
	CodeDismissed               Code = "dismissed"
	CodeGuardBandNarrowed       Code = "guard_band_narrowed"
	CodeTaskRevoked             Code = "task_revoked"
	CodeSponsorNotEligible      Code = "sponsor_not_eligible"
	CodeUnknownReservation      Code = "unknown_reservation"
	CodeReservationRevoked      Code = "reservation_revoked"
	CodeReservationOrphaned     Code = "reservation_orphaned"
	CodeUsageExceedsReservation Code = "usage_exceeds_reservation"
	CodePayloadConflict         Code = "payload_conflict"
	CodeDuplicateKeyOK          Code = "duplicate_key_ok"
	CodeReasonRequired          Code = "reason_required"
	CodeUntrustedAuthority      Code = "untrusted_authority"
	CodeProtocolUnsupported     Code = "protocol_unsupported"
	CodeDaemonUnavailable       Code = "daemon_unavailable"
)

// Codes this protocol layer adds. They are NOT in the contract's vocabulary and
// are marked as such; every one of them describes a failure the contract has no
// word for, and none of them is used where a contract code exists.
const (
	// CodeMalformedRequest is a request that does not frame as a valid envelope
	// or whose payload does not decode.
	CodeMalformedRequest Code = "malformed_request"
	// CodeMissingIdempotencyKey is a mutating op without an idempotency key.
	// It is folded into malformed_request at the envelope level, and kept
	// distinct for callers that validate before sending.
	CodeMissingIdempotencyKey Code = "missing_idempotency_key"
	// CodePainNarrowed is a reserve refused because recorded pain caps the
	// single-attempt exposure (docs/PRESSURE_MECHANICS.md §6).
	CodePainNarrowed Code = "pain_narrowed"
	// CodeInvalidArgument is a payload that decodes but is not usable.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeInvalidAmount is a non-positive or non-integer amount. Booleans and
	// floats are rejected before they can reach a money path.
	CodeInvalidAmount Code = "invalid_amount"
	// CodeInvalidClass is an unknown settlement class or outcome.
	CodeInvalidClass Code = "invalid_class"
	// CodeInvalidRoleState is an unknown employment state.
	CodeInvalidRoleState Code = "invalid_role_state"
	// CodeEvidenceRequired is a success claim without an evidence digest.
	CodeEvidenceRequired Code = "evidence_required"
	// CodeRuleRequired is a lesson or loss without a rule.
	CodeRuleRequired Code = "rule_required"
	// CodeCorrectionRequired is a lesson or loss without a correction.
	CodeCorrectionRequired Code = "correction_required"
	// CodeDismissalTerminal is an attempt to leave `dismissed` without an
	// explicit operator reinstatement.
	CodeDismissalTerminal Code = "dismissal_terminal"
	// CodeUnknownPolicyVersion is a missing price schedule.
	CodeUnknownPolicyVersion Code = "unknown_policy_version"
	// CodeUnknownEvent is a reference to an event that does not exist.
	CodeUnknownEvent Code = "unknown_event"
	// CodeUnknownTask is a reference to a lease that does not exist or is not
	// visible to this identity.
	CodeUnknownTask Code = "unknown_task"
	// CodeNotCompensable is a reversal of an event that moved no credit.
	CodeNotCompensable Code = "not_compensable"
	// CodeAlreadyCompensated is a second reversal of the same event.
	CodeAlreadyCompensated Code = "already_compensated"
	// CodeExceedsOriginal is a reversal larger than the event it reverses.
	CodeExceedsOriginal Code = "exceeds_original"
	// CodeActorRequired is a ledger event with no recorded actor.
	CodeActorRequired Code = "actor_required"
	// CodeReservationSettled is a settle against an already-settled
	// reservation that does not replay the original payload.
	CodeReservationSettled Code = "reservation_settled"
	// CodeReservationCanceled is an operation on a canceled reservation.
	CodeReservationCanceled Code = "reservation_canceled"
	// CodeReservationState is an operation on a reservation in the wrong state.
	CodeReservationState Code = "reservation_state"
	// CodeNothingToAnchor is a seal or anchor with nothing to commit to.
	CodeNothingToAnchor Code = "nothing_to_anchor"
	// CodeSchemaTooNew is a database written by a newer binary.
	CodeSchemaTooNew Code = "schema_too_new"
	// CodeInternal is a failure the daemon could not classify.
	CodeInternal Code = "internal"
)

// ledgerCodes maps the ledger's stable codes onto wire codes. Every ledger code
// that the contract names keeps its name; the rest are carried through as
// themselves. This is the single mapping point between Go errors and the wire,
// and it is a pure string function so this package never imports the ledger.
var ledgerCodes = map[string]Code{
	"unknown_identity":          CodeUnknownIdentity,
	"insufficient_credit":       CodeInsufficientCredit,
	"insolvent":                 CodeInsolvent,
	"suspended":                 CodeSuspended,
	"dismissed":                 CodeDismissed,
	"guard_band_narrowed":       CodeGuardBandNarrowed,
	"pain_narrowed":             CodePainNarrowed,
	"task_revoked":              CodeTaskRevoked,
	"unknown_reservation":       CodeUnknownReservation,
	"reservation_revoked":       CodeReservationRevoked,
	"reservation_orphaned":      CodeReservationOrphaned,
	"reservation_canceled":      CodeReservationCanceled,
	"reservation_settled":       CodeReservationSettled,
	"reservation_state":         CodeReservationState,
	"usage_exceeds_reservation": CodeUsageExceedsReservation,
	"payload_conflict":          CodePayloadConflict,
	"reason_required":           CodeReasonRequired,
	"evidence_required":         CodeEvidenceRequired,
	"invalid_argument":          CodeInvalidArgument,
	"invalid_amount":            CodeInvalidAmount,
	"invalid_class":             CodeInvalidClass,
	"invalid_role_state":        CodeInvalidRoleState,
	"rule_required":             CodeRuleRequired,
	"correction_required":       CodeCorrectionRequired,
	"dismissal_terminal":        CodeDismissalTerminal,
	"unknown_policy_version":    CodeUnknownPolicyVersion,
	"unknown_event":             CodeUnknownEvent,
	"unknown_task":              CodeUnknownTask,
	"not_compensable":           CodeNotCompensable,
	"already_compensated":       CodeAlreadyCompensated,
	"exceeds_original":          CodeExceedsOriginal,
	"actor_required":            CodeActorRequired,
	"nothing_to_anchor":         CodeNothingToAnchor,
	"schema_too_new":            CodeSchemaTooNew,
	"invalid_policy_body":       CodeInternal,
}

// CodeFromLedger maps a ledger error code onto a wire code. An unknown ledger
// code becomes CodeInternal rather than being silently passed through as a
// string an adapter would not recognise.
func CodeFromLedger(code string) Code {
	if wire, ok := ledgerCodes[code]; ok {
		return wire
	}
	return CodeInternal
}

// CodeOf maps any error onto a wire code. A *ProtocolError keeps its own code;
// anything else is CodeInternal. A ledger error is mapped explicitly with
// CodeFromLedger(ledger.Code(err)), which keeps this package free of a ledger
// import.
func CodeOf(err error) Code {
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Code
	}
	return CodeInternal
}

// ReservedAuthorityFields are payload keys that would assert operator authority
// from caller-supplied data. Their presence is refused outright
// (docs/CONTRACTS.md §4, §7: `untrusted_authority`); authority is derived from
// the invoking OS user at the operator CLI, never from a field.
var ReservedAuthorityFields = []string{
	"authority",
	"actor",
	"operator",
	"authorized_by",
	"principal",
	"user",
	"approver",
}

// ValidateNoAuthority refuses a payload that supplies an authority field.
//
// This is the wire-level enforcement of "no side channels": a harness message,
// hook callback, adapter request, tool output, or agent-authored string cannot
// record an operator action, because it cannot even name one.
func ValidateNoAuthority(payload json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		// Not an object: it cannot carry a named authority field. The method
		// handler will reject a payload of the wrong shape.
		return nil
	}
	for _, reserved := range ReservedAuthorityFields {
		if _, present := fields[reserved]; present {
			return Errorf(CodeUntrustedAuthority,
				"payload field %q asserts authority; authority is the invoking OS user at the operator CLI, never a caller-supplied field",
				reserved)
		}
	}
	return nil
}

// RequireReason validates the mandatory evidence of a state-changing operator
// command (docs/CONTRACTS.md §4: who, when, and a non-empty reason).
//
// A command that changes state without a reason is refused here, before any
// transaction opens, so nothing partial can be written.
func RequireReason(reason string) error {
	if reason == "" {
		return Errorf(CodeReasonRequired, "a state-changing operator command requires a non-empty reason")
	}
	return nil
}

// RequireEvidence validates a success claim's evidence digest
// (docs/CONTRACTS.md §5.4: "a settle claiming success without evidence is
// rejected").
func RequireEvidence(evidence string) error {
	if evidence == "" {
		return Errorf(CodeEvidenceRequired, "a success claim requires an evidence digest")
	}
	return nil
}

// ValidateMicrocredits refuses anything that is not a positive integer count of
// microcredits. A decoded bool or float can never reach a money path.
func ValidateMicrocredits(amount int64) error {
	if amount <= 0 {
		return Errorf(CodeInvalidAmount, "amount must be a positive integer count of microcredits, got %d", amount)
	}
	return nil
}

// String renders a code for logs and CLI output.
func (c Code) String() string { return string(c) }
