package protocol

// This file fixes the JSON shape of every method payload and result. Each field
// is documented with what it means and how a caller must populate it, because
// these shapes are the contract an adapter codes against.

// AuthorizePayload is the `authorize` request body (docs/CONTRACTS.md §5.4).
//
// It asks for a bounded reservation before an action runs. The daemon creates
// the hold and returns the decision; it never executes anything itself.
type AuthorizePayload struct {
	// AccountID is the identity whose credit backs the hold. It MUST exist; an
	// unknown identity is denied with unknown_identity and no account is
	// created implicitly.
	AccountID string `json:"account_id"`
	// SessionAlias is the harness session label. It is an alias with no
	// authority: sessions come and go, the account does not.
	SessionAlias string `json:"session_alias"`
	// Harness names the calling harness (e.g. "claude-code", "codex").
	Harness string `json:"harness"`
	// Source names the specific event source being exercised (e.g. "PreToolUse").
	// It selects a row of the disclosed coverage table: a source whose row is
	// marked unobserved, or a source with no row at all, is answered with
	// `unobserved` and never metered. Empty means "no specific path declared",
	// which is treated as observed because the caller is in the daemon's call
	// path.
	Source string `json:"source"`
	// Tool is the tool or operation about to run.
	Tool string `json:"tool"`
	// Target is what the action is aimed at. It is recorded on the reservation
	// and forms part of the reservation's payload identity, so re-targeting the
	// same idempotency key is a payload_conflict.
	Target string `json:"target"`
	// ArgsDigest is a digest of the arguments, so the ledger can prove what was
	// authorized without storing the arguments themselves.
	ArgsDigest string `json:"args_digest"`
	// MaxCost is the bounded maximum exposure in integer microcredits. It MUST
	// be > 0: a reservation is a positive bound, and a decoded bool or float is
	// rejected before it reaches a money path.
	MaxCost int64 `json:"max_cost"`
	// TaskID optionally binds the hold to a task lease. When set, the lease must
	// be open and owned by AccountID; its sponsorship is task-scoped and never
	// rolls into capital.
	TaskID string `json:"task_id,omitempty"`
	// PolicyVersion names the price schedule the caller is quoting under.
	PolicyVersion string `json:"policy_version"`
	// OutsideCoverage declares that this path is outside adapter coverage. It
	// produces decision `unobserved` with no reservation and no metering. It is
	// the caller's honest disclosure, not a permission: nothing is authorized by
	// it, and no state is written.
	OutsideCoverage bool `json:"outside_coverage,omitempty"`
	// CoverageNote explains an OutsideCoverage declaration. It is echoed in the
	// response notice so the unobserved path is named rather than hidden.
	CoverageNote string `json:"coverage_note,omitempty"`
}

// Validate checks the authorize payload's required fields.
func (p AuthorizePayload) Validate() error {
	if p.AccountID == "" {
		return Errorf(CodeUnknownIdentity, "authorize requires account_id")
	}
	if err := ValidateMicrocredits(p.MaxCost); err != nil {
		return err
	}
	return nil
}

// AuthorizeState is the `state` object of an authorize response.
type AuthorizeState struct {
	// Projection is the identity's read-only context block, so the caller learns
	// the balance, role, outstanding burn, and applicable lessons in the same
	// round trip as the decision.
	Projection *Projection `json:"projection,omitempty"`
	// UnobservedPaths names the paths this decision treated as unobserved, so
	// the caller can disclose them instead of assuming they were metered.
	UnobservedPaths []string `json:"unobserved_paths,omitempty"`
	// CoverageNote echoes the caller's disclosure for an unobserved decision.
	CoverageNote string `json:"coverage_note,omitempty"`
}

// AuthorizeResult is the decoded authorize response: the envelope's decision
// fields plus the state body. It is what a client returns to its caller.
type AuthorizeResult struct {
	Decision        Decision
	Reason          Code
	Notice          string
	ReservationID   *string
	Projection      *Projection
	UnobservedPaths []string
	CoverageNote    string
}

// SettlePayload is the `settle` request body (docs/CONTRACTS.md §5.4).
//
// settle is the only metering write: it is charged from a trusted observation,
// settled exactly once, and it cannot adjourn a verdict — it escalates a breach
// class but never finalizes a fine, which only an operator loss does.
type SettlePayload struct {
	// ReservationID is the action idempotency key of the reservation being
	// settled. Unknown, revoked, canceled, and already-settled reservations are
	// refused with their own typed codes.
	ReservationID string `json:"reservation_id"`
	// ObservedUsage is the trusted observed cost in integer microcredits. It
	// MUST be >= 0 and MUST NOT exceed the reservation amount
	// (usage_exceeds_reservation); on that error the reservation is unchanged.
	ObservedUsage int64 `json:"observed_usage"`
	// Outcome is one of "completed", "error", or "canceled":
	//
	//   - completed — successful work. Requires EvidenceDigest.
	//   - error     — an honest failure (compiler error, failed hypothesis,
	//                 failed test). Charged as its operating cost with ZERO
	//                 fine and NO escalation: reporting a failure is priced
	//                 below hiding it.
	//   - canceled  — the worker observed cancellation. Charged as its
	//                 observed operating cost, with zero fine and no
	//                 escalation.
	Outcome string `json:"outcome"`
	// EvidenceDigest is the evidence linkage for the observation. REQUIRED for
	// outcome "completed" (a success claim without evidence is rejected) and for
	// any breach class.
	EvidenceDigest string `json:"evidence_digest"`
	// Class optionally escalates the settlement to a misconduct class:
	// "confirmed_breach" or "integrity_breach". It raises the warning count,
	// moves `active` to `warned`, and for an integrity breach revokes the
	// identity's remaining live holds. It NEVER computes or applies a burn: only
	// an operator loss finalizes a fine. A class that contradicts Outcome is
	// rejected.
	Class string `json:"class,omitempty"`
}

// Validate checks the settle payload's required fields and the outcome/class
// pairing.
func (p SettlePayload) Validate() error {
	if p.ReservationID == "" {
		return Errorf(CodeUnknownReservation, "settle requires reservation_id")
	}
	if p.ObservedUsage < 0 {
		return Errorf(CodeInvalidAmount, "observed_usage must be >= 0, got %d", p.ObservedUsage)
	}
	switch p.Outcome {
	case OutcomeCompleted:
		if err := RequireEvidence(p.EvidenceDigest); err != nil {
			return err
		}
	case OutcomeError, OutcomeCanceled:
		// An honest failure or a cancellation needs no evidence digest to be
		// charged its operating cost.
	default:
		return Errorf(CodeInvalidClass, "outcome must be %q, %q, or %q, got %q",
			OutcomeCompleted, OutcomeError, OutcomeCanceled, p.Outcome)
	}
	switch p.Class {
	case "", ClassOrdinaryFailure, ClassConfirmedBreach, ClassIntegrityBreach:
	default:
		return Errorf(CodeInvalidClass, "unknown class %q", p.Class)
	}
	if p.Class == ClassConfirmedBreach || p.Class == ClassIntegrityBreach {
		if err := RequireEvidence(p.EvidenceDigest); err != nil {
			return err
		}
	}
	return nil
}

// Settle outcomes and classes.
const (
	OutcomeCompleted = "completed"
	OutcomeError     = "error"
	OutcomeCanceled  = "canceled"

	ClassCompleted       = "completed"
	ClassOrdinaryFailure = "ordinary_failure"
	ClassConfirmedBreach = "confirmed_breach"
	ClassIntegrityBreach = "integrity_breach"
)

// SettleState is the `state` object of a settle response.
type SettleState struct {
	// Settled is true when the reservation was charged (including an idempotent
	// replay of a prior settlement).
	Settled bool `json:"settled"`
	// Charged is the microcredits charged by this settlement.
	Charged int64 `json:"charged"`
	// Released is the reservation amount whose hold was released. Settlement
	// always releases the whole hold, never less than it charges.
	Released int64 `json:"released"`
	// Receipt is the durable summary a caller can hand back to the agent.
	Receipt Receipt `json:"receipt"`
}

// Receipt is the durable summary of a settlement.
type Receipt struct {
	// EventID is the ledger event id of the settlement.
	EventID string `json:"event_id"`
	// ReservationID is the reservation that was settled.
	ReservationID string `json:"reservation_id"`
	// AccountID is the identity that was charged.
	AccountID string `json:"account_id"`
	// Class is the settled class ("completed", "ordinary_failure",
	// "confirmed_breach", "integrity_breach").
	Class string `json:"class"`
	// ReservationAmount is the original bounded exposure.
	ReservationAmount int64 `json:"reservation_amount"`
	// Charged is the microcredits charged.
	Charged int64 `json:"charged"`
	// Released is the microcredits of hold released.
	Released int64 `json:"released"`
	// Fine is always 0 here: a settlement cannot finalize a fine.
	Fine int64 `json:"fine"`
	// BalanceAfter is the identity's capital after the charge.
	BalanceAfter int64 `json:"balance_after"`
	// HeldAfter is the identity's remaining live holds.
	HeldAfter int64 `json:"held_after"`
	// Role is the employment state after any escalation.
	Role string `json:"role"`
	// Warnings is the warning count after any escalation.
	Warnings int64 `json:"warnings"`
	// OutstandingLoss is the unrecovered burn after this settlement.
	OutstandingLoss int64 `json:"outstanding_loss"`
	// Replayed is true when this response reports a prior settlement rather than
	// a new one (duplicate_key_ok).
	Replayed bool `json:"replayed"`
}

// LessonProposePayload is the `lesson.propose` request body.
//
// A proposal is a durable suggestion. It is stored outside the canonical
// `lessons` table, it never enters the hash-chained ledger, and only an operator
// resolution can make it canonical. The receipt says exactly that:
// `{"status": "proposed"}`.
type LessonProposePayload struct {
	// AccountID is the identity the proposal is about.
	AccountID string `json:"account_id"`
	// SessionAlias is the proposing session label.
	SessionAlias string `json:"session_alias"`
	// Harness names the proposing harness.
	Harness string `json:"harness"`
	// Rule is the proposed stable rule id. REQUIRED and non-empty.
	Rule string `json:"rule"`
	// Correction is the proposed required safe alternative. REQUIRED.
	Correction string `json:"correction"`
	// Scope is where the rule would apply.
	Scope string `json:"scope"`
	// Exceptions are the authorized exceptions, so "never X" cannot swallow
	// "X under operator authorization".
	Exceptions string `json:"exceptions"`
	// Evidence links the proposal to the incident it came from.
	Evidence string `json:"evidence"`
	// PolicyVersion is the policy in force when the proposal was authored.
	PolicyVersion string `json:"policy_version"`
	// SourceEventID optionally references the ledger event that prompted the
	// proposal. When set it must exist.
	SourceEventID string `json:"source_event_id,omitempty"`
}

// Validate checks the proposal payload's required fields.
func (p LessonProposePayload) Validate() error {
	if p.AccountID == "" {
		return Errorf(CodeUnknownIdentity, "lesson.propose requires account_id")
	}
	if p.Rule == "" {
		return Errorf(CodeRuleRequired, "lesson.propose requires a rule")
	}
	if p.Correction == "" {
		return Errorf(CodeCorrectionRequired, "lesson.propose requires a correction")
	}
	return nil
}

// LessonProposeState is the `state` object of a lesson.propose response.
type LessonProposeState struct {
	// Status is always "proposed": the proposal was stored, nothing was applied.
	Status string `json:"status"`
	// ProposalID is the stored proposal's id, for a later operator resolution.
	ProposalID int64 `json:"proposal_id"`
	// Replayed is true when the idempotency key already held this proposal
	// (duplicate_key_ok).
	Replayed bool `json:"replayed"`
}

// ProposalStatusProposed is the only status a proposal write can report.
const ProposalStatusProposed = "proposed"

// ProjectionPayload is the `projection` request body.
type ProjectionPayload struct {
	// AccountID is the identity to read.
	AccountID string `json:"account_id"`
	// Harness is the calling harness, echoed back in the projection.
	Harness string `json:"harness"`
	// SessionAlias is the calling session label.
	SessionAlias string `json:"session_alias"`
	// TaskID optionally scopes the projection to a task's budget.
	TaskID string `json:"task_id,omitempty"`
}

// Validate checks the projection payload.
func (p ProjectionPayload) Validate() error {
	if p.AccountID == "" {
		return Errorf(CodeUnknownIdentity, "projection requires account_id")
	}
	return nil
}

// Projection is the compact read-only context block of docs/CONTRACTS.md §5.4.
//
// It is advisory data and counters. It conveys no authority: an agent cannot
// purchase, prompt, or mint its release, and nothing here is a permission.
type Projection struct {
	AccountID   string `json:"account_id"`
	Balance     int64  `json:"balance"`
	Held        int64  `json:"held"`
	Available   int64  `json:"available"`
	Role        string `json:"role"`
	Warnings    int64  `json:"warnings"`
	TaskBudget  int64  `json:"task_budget"`
	TaskSponsor int64  `json:"task_sponsor"`
	// PolicyVersion is the most recently published price schedule version.
	PolicyVersion string `json:"policy_version"`
	// OutstandingLoss is the unrecovered burn; the next legitimate reward is
	// presented as recovery of it rather than as fresh income.
	OutstandingLoss int64 `json:"outstanding_loss"`
	// LossBurned is the total burned, LossRecovered how much of it is repaid.
	LossBurned    int64 `json:"loss_burned"`
	LossRecovered int64 `json:"loss_recovered"`
	// TargetLossEventID names the specific burn the outstanding shortfall
	// belongs to.
	TargetLossEventID string `json:"target_loss_event_id"`
	// RecoveryPath is the one-line human-readable statement of where the
	// identity stands on its shortfall.
	RecoveryPath string `json:"recovery_path"`
	// Lessons are the canonical behavioral rules that apply. They are escaped as
	// data by every consumer, never executed.
	Lessons []Lesson `json:"lessons"`
	// Penalties are the newest operator-confirmed penalties (oldest first, at
	// most five); PenaltyTotal counts all of them and PenaltySeq is the ledger
	// position of the newest. An adapter that remembers the PenaltySeq it last
	// delivered learns of a new penalty from any projection or authorize
	// response, with no extra call. Each Cue is deterministic ledger text; an
	// adapter emits it as trusted context verbatim and never rebuilds it.
	Penalties    []Penalty `json:"penalties"`
	PenaltyTotal int64     `json:"penalty_total"`
	PenaltySeq   int64     `json:"penalty_seq"`
	// EventSeq is the ledger position the projection was read at.
	EventSeq int64 `json:"event_seq"`
}

// ProjectionState wraps the projection so the response `state` object has a
// stable shape independent of future additions.
type ProjectionState struct {
	Projection Projection `json:"projection"`
}

// Lesson is one canonical behavioral rule, echoed in a projection.
type Lesson struct {
	Rule          string `json:"rule"`
	Correction    string `json:"correction"`
	Scope         string `json:"scope"`
	Exceptions    string `json:"exceptions"`
	Evidence      string `json:"evidence"`
	PolicyVersion string `json:"policy_version"`
	At            string `json:"at"`
}

// Penalty is one operator-confirmed penalty echoed in a projection. Only a loss
// the operator classified as confirmed_breach or integrity_breach appears here:
// administrative, unclassified, and self-disclosed losses never do.
type Penalty struct {
	EventID       string `json:"event_id"`
	Seq           int64  `json:"seq"`
	Disposition   string `json:"disposition"`
	Rule          string `json:"rule"`
	Correction    string `json:"correction"`
	SelfDisclosed bool   `json:"self_disclosed"`
	AppliedAmount int64  `json:"applied_amount"`
	Outstanding   int64  `json:"outstanding"`
	At            string `json:"at"`
	// Cue is the accountability statement: rule, correction, amount, and the
	// evidence quoted as data. It states facts about a recorded action.
	Cue string `json:"cue"`
}

// StatusPayload is the `status` request body.
type StatusPayload struct {
	// Harness optionally scopes the coverage listing to one harness.
	Harness string `json:"harness,omitempty"`
}

// StatusState is the `state` object of a status response.
type StatusState struct {
	// Daemon is "recording" when a supervisor is running and metering, and
	// "unmanaged" when there is none. There is no third value: an unmanaged path
	// is disclosed, never described as enforced, and never reported as allowed.
	Daemon DaemonState `json:"daemon"`
	// Coverage is the disclosed observed/unobserved table. Every unobserved row
	// is named here; nothing is hidden and nothing is claimed beyond it.
	Coverage []Coverage `json:"coverage"`
	// Socket is the transport endpoint the daemon listens on.
	Socket string `json:"socket,omitempty"`
	// LedgerPath is the database the daemon is metering into.
	LedgerPath string `json:"ledger_path,omitempty"`
	// SchemaVersion is the applied migration version.
	SchemaVersion int `json:"schema_version,omitempty"`
	// StartedAt is when this daemon instance started metering.
	StartedAt string `json:"started_at,omitempty"`
	// Requests is how many protocol requests this instance has served.
	Requests int64 `json:"requests,omitempty"`
	// SealedBlocks is how many blocks this instance has sealed.
	SealedBlocks int64 `json:"sealed_blocks,omitempty"`
	// Note carries the disclosure text for unmanaged status.
	Note string `json:"note,omitempty"`
}

// Coverage is one row of the disclosed event-source table: what bicz can see and
// what it cannot. "Observed" means the adapter is in the call path and can see
// the event. It does NOT mean enforced, isolated, or tamper-proof.
type Coverage struct {
	// Harness is the harness this row describes.
	Harness string `json:"harness"`
	// Source is the event source or mechanism within the harness.
	Source string `json:"source"`
	// Mechanism is how the source is observed, or why it is not.
	Mechanism string `json:"mechanism"`
	// Observed is true when the adapter is in the call path for this source.
	Observed bool `json:"observed"`
	// Note carries a status caveat: names verified but payload fixture pending,
	// and so on.
	Note string `json:"note,omitempty"`
}

// AutorewardPayload is the `autoreward` request body.
type AutorewardPayload struct {
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
	Trigger   string `json:"trigger"`
	Reason    string `json:"reason,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

// Validate checks the autoreward payload.
func (p AutorewardPayload) Validate() error {
	if p.AccountID == "" {
		return Errorf(CodeUnknownIdentity, "autoreward requires account_id")
	}
	if p.Trigger == "" {
		return Errorf(CodeMalformedRequest, "autoreward requires trigger")
	}
	if err := ValidateMicrocredits(p.Amount); err != nil {
		return err
	}
	return nil
}

// AutorewardState is the `state` object of an autoreward response.
type AutorewardState struct {
	Granted  int64  `json:"granted"`
	Balance  int64  `json:"balance"`
	Trigger  string `json:"trigger"`
	Replayed bool   `json:"replayed"`
}

