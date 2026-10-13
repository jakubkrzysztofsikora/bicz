package ledger

import (
	"context"
	"database/sql"
)

// The payload structs below are the canonical, hashed form of each event's
// `payload` column. They are written through canonical.Marshal (so they are
// bicz-canonical-v1 JSON: sorted keys, integers only, no floats) and read back
// through encoding/json for idempotency comparison and projection.
//
// Payload keys are the stable receipt fields of docs/CONTRACTS.md §5.4 and
// docs/PRESSURE_MECHANICS.md §4/§5, so a receipt can be reconstructed from the ledger
// alone.

// querier is satisfied by both *sql.DB and *sql.Tx, which lets the read helpers
// run either standalone or inside a mutation transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type grantPayload struct {
	Phase          string `json:"phase"`
	AccountID      string `json:"account_id"`
	InitialCapital int64  `json:"initial_capital"`
}

type reservePayload struct {
	Phase         string                 `json:"phase"`
	ReservationID string                 `json:"reservation_id"`
	AccountID     string                 `json:"account_id"`
	TaskID        *string                `json:"task_id"`
	Amount        int64                  `json:"amount"`
	State         string                 `json:"state"`
	Balance       int64                  `json:"balance"`
	Held          int64                  `json:"held"`
	HeldAfter     int64                  `json:"held_after"`
	Context       *reserveContextPayload `json:"context"`
}

// reserveContextPayload is the recorded authorization context: which harness,
// session, tool, and target the hold was created for.
type reserveContextPayload struct {
	Harness       string `json:"harness"`
	SessionAlias  string `json:"session_alias"`
	Tool          string `json:"tool"`
	Target        string `json:"target"`
	ArgsDigest    string `json:"args_digest"`
	PolicyVersion string `json:"policy_version"`
}

type settlePayload struct {
	Phase               string   `json:"phase"`
	ReservationID       string   `json:"reservation_id"`
	AccountID           string   `json:"account_id"`
	Class               string   `json:"class"`
	ReservationAmount   int64    `json:"reservation_amount"`
	Charged             int64    `json:"charged"`
	Released            int64    `json:"released"`
	Fine                int64    `json:"fine"`
	Evidence            string   `json:"evidence"`
	BalanceBefore       int64    `json:"balance_before"`
	BalanceAfter        int64    `json:"balance_after"`
	Role                string   `json:"role"`
	Warnings            int64    `json:"warnings"`
	Revoked             int64    `json:"revoked"`
	RevokedReservations []string `json:"revoked_reservations"`
	OutstandingAfter    int64    `json:"outstanding_loss_after"`
}

type cancelPayload struct {
	Phase         string `json:"phase"`
	ReservationID string `json:"reservation_id"`
	AccountID     string `json:"account_id"`
	Amount        int64  `json:"amount"`
	Evidence      string `json:"evidence"`
}

type revokePayload struct {
	Phase         string   `json:"phase"`
	AccountID     string   `json:"account_id"`
	Released      int64    `json:"released"`
	Reservations  []string `json:"reservations"`
	SourceEventID string   `json:"source_event_id"`
}

type rewardPayload struct {
	AccountID              string `json:"account_id"`
	Amount                 int64  `json:"amount"`
	Reason                 string `json:"reason"`
	Evidence               string `json:"evidence"`
	RequestedAppliedToLoss int64  `json:"requested_applied_to_loss"`
	AppliedToLoss          int64  `json:"applied_to_loss"`
	LossEventID            string `json:"loss_event_id"`
	BalanceBefore          int64  `json:"balance_before"`
	BalanceAfter           int64  `json:"balance_after"`
	OutstandingAfter       int64  `json:"outstanding_loss_after"`
}

type lossPayload struct {
	AccountID        string `json:"account_id"`
	Rule             string `json:"rule"`
	Correction       string `json:"correction"`
	Scope            string `json:"scope"`
	Exceptions       string `json:"exceptions"`
	PolicyVersion    string `json:"policy_version"`
	RequestedAmount  int64  `json:"requested_amount"`
	FormulaAmount    int64  `json:"formula_amount"`
	AppliedAmount    int64  `json:"applied_amount"`
	RepeatCount      int64  `json:"repeat_count"`
	RepeatMultiplier int64  `json:"repeat_multiplier"`
	GuardBand        bool   `json:"guard_band"`
	SelfDisclosed    bool   `json:"self_disclosed"`
	// Disposition and ClassificationVersion are the explicit classification of
	// the burn (accountability.go). A payload written before they existed has
	// version 0 and reads as unclassified; nothing is inferred or rewritten.
	Disposition           string   `json:"disposition"`
	ClassificationVersion int64    `json:"classification_version"`
	BalanceBefore         int64    `json:"balance_before"`
	BalanceAfter          int64    `json:"balance_after"`
	HeldBefore            int64    `json:"held_before"`
	HeldAfter             int64    `json:"held_after"`
	Revoked               int64    `json:"held_revoked"`
	RevokedReservations   []string `json:"revoked_reservations"`
	LessonID              int64    `json:"lesson_id"`
	OutstandingAfter      int64    `json:"outstanding_loss_after"`
}

type compensationPayload struct {
	AccountID        string `json:"account_id"`
	OriginalEventID  string `json:"original_event_id"`
	OriginalKind     string `json:"original_kind"`
	OriginalAmount   int64  `json:"original_amount"`
	Amount           int64  `json:"amount"`
	BalanceBefore    int64  `json:"balance_before"`
	BalanceAfter     int64  `json:"balance_after"`
	OutstandingAfter int64  `json:"outstanding_loss_after"`
}

type rolePayload struct {
	AccountID     string `json:"account_id"`
	Previous      string `json:"previous_state"`
	State         string `json:"state"`
	Warnings      int64  `json:"warnings"`
	Reinstatement bool   `json:"reinstatement"`
}

type pricePayload struct {
	Version string `json:"version"`
}

type anchorPayload struct {
	Height      int64  `json:"height"`
	SeqLow      int64  `json:"seq_low"`
	SeqHigh     int64  `json:"seq_high"`
	Destination string `json:"destination"`
	BlockDigest string `json:"block_digest"`
}

type taskPayload struct {
	Phase     string `json:"phase"`
	TaskID    string `json:"task_id"`
	AccountID string `json:"account_id"`
	State     string `json:"state"`
	Sponsor   int64  `json:"sponsor"`
}

type lessonResolutionPayload struct {
	ProposalID    int64  `json:"proposal_id"`
	Decision      string `json:"decision"`
	Rule          string `json:"rule"`
	Correction    string `json:"correction"`
	PolicyVersion string `json:"policy_version"`
	LessonID      int64  `json:"lesson_id"`
}

type reviewPayload struct {
	AccountID    string `json:"account_id"`
	Verdict      string `json:"verdict"`
	TaskID       string `json:"task_id"`
	TaskRevoked  bool   `json:"task_revoked"`
	Role         string `json:"role"`
	PreviousRole string `json:"previous_role"`
	Warnings     int64  `json:"warnings"`
}

type resetPayload struct {
	Phase         string `json:"phase"`
	AccountID     string `json:"account_id"`
	TargetBalance int64  `json:"target_balance"`
	BalanceBefore int64  `json:"balance_before"`
	BalanceAfter  int64  `json:"balance_after"`
	Delta         int64  `json:"delta"`
}

type autorewardPayload struct {
	AccountID       string `json:"account_id"`
	Trigger         string `json:"trigger"`
	RequestedAmount int64  `json:"requested_amount"`
	GrantedAmount   int64  `json:"granted_amount"`
	BaselineCeiling int64  `json:"baseline_ceiling"`
	BalanceBefore   int64  `json:"balance_before"`
	BalanceAfter    int64  `json:"balance_after"`
	Reason          string `json:"reason"`
	Evidence        string `json:"evidence"`
}

