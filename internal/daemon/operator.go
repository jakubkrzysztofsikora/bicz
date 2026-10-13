package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// Operator is the operator authority of docs/CONTRACTS.md §4: a real human at
// the CLI.
//
// Three rules are structural here, not advisory:
//
//   - Authority is derived from the invoking OS user by the ledger, never from a
//     parameter. There is no authority argument on any method below.
//   - Every state-changing command requires a non-empty reason, checked BEFORE
//     any transaction opens, so a refused command writes nothing.
//   - A caller-supplied authority is refused outright. On the wire that means
//     protocol payloads carrying an authority field; at the CLI it means the
//     environment, which is how a harness would try to inject one. Setting
//     BICZ_AUTHORITY, BICZ_ACTOR, or BICZ_OPERATOR makes every state-changing
//     command fail with untrusted_authority.
type Operator struct {
	ledger *ledger.Ledger
}

// NewOperator opens the ledger at path with the invoking OS user as the actor.
func NewOperator(ledgerPath string) (*Operator, error) {
	handle, err := ledger.Open(ledgerPath)
	if err != nil {
		return nil, err
	}
	return &Operator{ledger: handle}, nil
}

// Close releases the ledger handle.
func (o *Operator) Close() error { return o.ledger.Close() }

// Actor is the resolved operator identity: the local OS user.
func (o *Operator) Actor() string { return o.ledger.Actor() }

// Ledger exposes the read-only ledger API for the operator commands.
func (o *Operator) Ledger() *ledger.Ledger { return o.ledger }

// suppliedAuthorityEnv lists the environment variables that would assert an
// operator identity from outside the process. A harness spawns shells, so this
// is the realistic injection path for "authority=operator", and it is refused
// (docs/CONTRACTS.md §4: caller-supplied authority is rejected outright).
var suppliedAuthorityEnv = []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR"}

// checkNoSuppliedAuthority refuses any attempt to assert authority from the
// environment.
func checkNoSuppliedAuthority() error {
	for _, key := range suppliedAuthorityEnv {
		if value := os.Getenv(key); value != "" {
			return protocol.Errorf(protocol.CodeUntrustedAuthority,
				"%s=%q asserts operator authority; authority is the invoking OS user, never a caller-supplied value", key, value)
		}
	}
	return nil
}

// guard validates the two preconditions every state-changing command shares.
func guard(reason string) error {
	if err := checkNoSuppliedAuthority(); err != nil {
		return err
	}
	return protocol.RequireReason(reason)
}

// OpenRequest provisions an identity.
type OpenRequest struct {
	// Identity is the stable identity to provision. REQUIRED.
	Identity string
	// InitialCapital is the opening balance in integer microcredits, recorded as
	// one `grant` event. Zero provisions an identity with no credit.
	InitialCapital int64
	// Reason is the mandatory evidence text.
	Reason string
}

// Open provisions an identity and returns its position.
func (o *Operator) Open(ctx context.Context, request OpenRequest) (ledger.Account, error) {
	if request.Identity == "" {
		return ledger.Account{}, protocol.Errorf(protocol.CodeUnknownIdentity, "open requires an identity")
	}
	if request.InitialCapital < 0 {
		return ledger.Account{}, protocol.Errorf(protocol.CodeInvalidAmount,
			"initial capital %d must not be negative", request.InitialCapital)
	}
	if err := guard(request.Reason); err != nil {
		return ledger.Account{}, err
	}
	return o.ledger.OpenAccount(ctx, request.Identity, ledger.WithInitialCapital(request.InitialCapital))
}

// RewardRequest credits an identity, optionally as recovery of a specific burn.
type RewardRequest struct {
	EventID       string
	Identity      string
	Amount        int64
	Reason        string
	Evidence      string
	AppliedToLoss int64
}

// Reward is the `operator reward` command: append a reward event and credit K.
func (o *Operator) Reward(ctx context.Context, request RewardRequest) (ledger.RewardResult, error) {
	if err := guard(request.Reason); err != nil {
		return ledger.RewardResult{}, err
	}
	if err := protocol.ValidateMicrocredits(request.Amount); err != nil {
		return ledger.RewardResult{}, err
	}
	if request.AppliedToLoss < 0 {
		return ledger.RewardResult{}, protocol.Errorf(protocol.CodeInvalidAmount,
			"applied-to-loss %d must not be negative", request.AppliedToLoss)
	}
	return o.ledger.RecordReward(ctx, eventIDOr(request.EventID, "reward"), request.Identity,
		request.Amount, request.Reason, request.Evidence, request.AppliedToLoss)
}

// ResetRequest restores an identity's budget without running reward logic.
type ResetRequest struct {
	EventID       string
	Identity      string
	TargetBalance int64
	Reason        string
	Evidence      string
}

// Reset restores an identity's balance to TargetBalance without running reward logic.
// Outstanding loss, lessons, and role are untouched.
func (o *Operator) Reset(ctx context.Context, request ResetRequest) (ledger.ResetResult, error) {
	if request.Identity == "" {
		return ledger.ResetResult{}, protocol.Errorf(protocol.CodeUnknownIdentity, "reset requires an identity")
	}
	if request.Reason == "" {
		request.Reason = "reset budget to unblock agent"
	}
	if err := guard(request.Reason); err != nil {
		return ledger.ResetResult{}, err
	}
	return o.ledger.ResetBudget(ctx, eventIDOr(request.EventID, "reset"), request.Identity,
		request.TargetBalance, request.Reason, request.Evidence)
}

// AutorewardRequest records a bounded microreward to replenish spent credits.
type AutorewardRequest struct {
	EventID  string
	Identity string
	Amount   int64
	Trigger  string
	Reason   string
	Evidence string
}

// Autoreward records a bounded microreward to replenish spent credits up to baseline ceiling.
func (o *Operator) Autoreward(ctx context.Context, request AutorewardRequest) (ledger.AutorewardResult, error) {
	if request.Identity == "" {
		return ledger.AutorewardResult{}, protocol.Errorf(protocol.CodeUnknownIdentity, "autoreward requires an identity")
	}
	if request.Trigger == "" {
		return ledger.AutorewardResult{}, protocol.Errorf(protocol.CodeMalformedRequest, "autoreward requires a trigger")
	}
	if err := protocol.ValidateMicrocredits(request.Amount); err != nil {
		return ledger.AutorewardResult{}, err
	}
	if request.Reason == "" {
		request.Reason = "autoreward for " + request.Trigger
	}
	if err := guard(request.Reason); err != nil {
		return ledger.AutorewardResult{}, err
	}
	return o.ledger.RecordAutoreward(ctx, eventIDOr(request.EventID, "autoreward"), request.Identity,
		request.Amount, request.Trigger, request.Reason, request.Evidence)
}

// LossRequest finalizes a burn.
type LossRequest struct {
	EventID       string
	Identity      string
	Requested     int64
	Rule          string
	Correction    string
	Scope         string
	Exceptions    string
	Evidence      string
	PolicyVersion string
	Reason        string
	// SelfDisclosed marks an incident the agent reported before detection. It is
	// recorded and burned, but it never increments the repeat count and never
	// escalates the role.
	SelfDisclosed bool
	// Disposition classifies the burn and is REQUIRED: confirmed_breach or
	// integrity_breach (an operator-confirmed finding, which earns an
	// accountability cue) or administrative (no misconduct alleged, no cue). It
	// is never inferred from the rule text. The CLI supplies confirmed_breach when
	// its flag is omitted; no other caller gets a default.
	Disposition string
}

// Loss is the `operator loss` command: append a loss event, burn K by the frozen
// formula, and write the durable lesson in the same transaction.
func (o *Operator) Loss(ctx context.Context, request LossRequest) (ledger.LossResult, error) {
	if err := guard(request.Reason); err != nil {
		return ledger.LossResult{}, err
	}
	if request.Rule == "" {
		return ledger.LossResult{}, protocol.Errorf(protocol.CodeRuleRequired, "loss requires a rule")
	}
	if request.Correction == "" {
		return ledger.LossResult{}, protocol.Errorf(protocol.CodeCorrectionRequired, "loss requires a correction")
	}
	if request.Requested < 0 {
		return ledger.LossResult{}, protocol.Errorf(protocol.CodeInvalidAmount,
			"requested loss %d must not be negative", request.Requested)
	}
	disposition, err := ledger.ParseDisposition(request.Disposition)
	if err != nil {
		return ledger.LossResult{}, protocol.Errorf(protocol.CodeInvalidArgument, "%v", err)
	}
	options := []ledger.LossOption{ledger.WithDisposition(disposition)}
	if request.SelfDisclosed {
		options = append(options, ledger.SelfDisclosed())
	}
	return o.ledger.RecordLoss(ctx, eventIDOr(request.EventID, "loss"), request.Identity, request.Requested,
		request.Rule, request.Correction, request.Scope, request.Exceptions, request.Evidence,
		request.PolicyVersion, options...)
}

// AppealRequest proposes a reversal of a prior event.
type AppealRequest struct {
	EventID         string
	OriginalEventID string
	Identity        string
	Amount          int64
	Reason          string
	Evidence        string
}

// Appeal is the `appeal` command: applying a reversal appends a compensation
// event. Nothing is edited or deleted, so the original event and its lesson stay
// exactly as written.
func (o *Operator) Appeal(ctx context.Context, request AppealRequest) (ledger.CompensationResult, error) {
	if err := guard(request.Reason); err != nil {
		return ledger.CompensationResult{}, err
	}
	if err := protocol.ValidateMicrocredits(request.Amount); err != nil {
		return ledger.CompensationResult{}, err
	}
	if request.OriginalEventID == "" {
		return ledger.CompensationResult{}, protocol.Errorf(protocol.CodeInvalidArgument, "appeal requires an original event id")
	}
	return o.ledger.RecordCompensation(ctx, eventIDOr(request.EventID, "compensation"),
		request.OriginalEventID, request.Identity, request.Amount, request.Reason, request.Evidence)
}

// ReviewRequest records an adjudication verdict.
type ReviewRequest struct {
	EventID   string
	Identity  string
	Verdict   string
	Reason    string
	Evidence  string
	TaskID    string
	Role      string
	Reinstate bool
}

// Review is the `operator review` command: record a verdict, optionally revoke a
// task lease and optionally transition the role. It finalizes no fine: a burn is
// only ever written by Loss.
func (o *Operator) Review(ctx context.Context, request ReviewRequest) (ledger.ReviewResult, error) {
	if err := guard(request.Reason); err != nil {
		return ledger.ReviewResult{}, err
	}
	if request.Verdict == "" {
		return ledger.ReviewResult{}, protocol.Errorf(protocol.CodeInvalidArgument, "review requires a verdict")
	}
	options := make([]ledger.ReviewOption, 0, 2)
	if request.TaskID != "" {
		options = append(options, ledger.RevokingTask(request.TaskID))
	}
	if request.Role != "" {
		if !ledger.ValidRole(request.Role) {
			return ledger.ReviewResult{}, protocol.Errorf(protocol.CodeInvalidRoleState, "%q", request.Role)
		}
		options = append(options, ledger.TransitioningRole(request.Role, request.Reinstate))
	}
	return o.ledger.RecordReview(ctx, eventIDOr(request.EventID, "review"), request.Identity,
		request.Verdict, request.Reason, request.Evidence, options...)
}

// ReinstateRequest is the explicit operator reinstatement of a dismissed
// identity: the only exit from `dismissed` (docs/CONTRACTS.md §1 I4).
type ReinstateRequest struct {
	Key      string
	Identity string
	Reason   string
	Evidence string
}

// Reinstate returns a dismissed identity to active.
func (o *Operator) Reinstate(ctx context.Context, request ReinstateRequest) (ledger.RoleResult, error) {
	if err := guard(request.Reason); err != nil {
		return ledger.RoleResult{}, err
	}
	return o.ledger.SetRole(ctx, request.Identity, ledger.RoleActive, request.Reason, request.Evidence,
		eventIDOr(request.Key, "reinstate"), ledger.Reinstate())
}

// Price publishes a price schedule version from its JSON body.
func (o *Operator) Price(ctx context.Context, version string, body []byte, reason string) error {
	if err := guard(reason); err != nil {
		return err
	}
	if version == "" {
		return protocol.Errorf(protocol.CodeInvalidArgument, "price requires a version")
	}
	var policy ledger.Policy
	if err := json.Unmarshal(body, &policy); err != nil {
		return protocol.Errorf(protocol.CodeInvalidArgument, "policy body: %v", err)
	}
	return o.ledger.PublishPriceSchedule(ctx, version, policy)
}

// Verify checks the whole chain — events, blocks, and anchors — plus the
// externally recorded anchor lines.
func (o *Operator) Verify(ctx context.Context) (ledger.ChainStats, error) {
	events, err := o.ledger.Events(ctx)
	if err != nil {
		return ledger.ChainStats{}, err
	}
	blocks, err := o.ledger.Blocks(ctx)
	if err != nil {
		return ledger.ChainStats{}, err
	}
	anchors, err := o.ledger.Anchors(ctx)
	if err != nil {
		return ledger.ChainStats{}, err
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		return ledger.ChainStats{}, err
	}
	if err := integrity.VerifyAnchors(blocks, anchors); err != nil {
		return ledger.ChainStats{}, err
	}
	if err := verifyAnchorDestinations(anchors); err != nil {
		return ledger.ChainStats{}, err
	}
	return o.ledger.ChainStats(ctx)
}

// Anchor seals a block when events are pending and appends the digest to
// destination. Sealing an inconsistent chain would bless it, so the chain is
// verified first.
func (o *Operator) Anchor(ctx context.Context, destination, reason string) (integrity.AnchorPayload, error) {
	if err := guard(reason); err != nil {
		return integrity.AnchorPayload{}, err
	}
	if destination == "" {
		return integrity.AnchorPayload{}, protocol.Errorf(protocol.CodeInvalidArgument, "anchor requires a destination")
	}
	if _, err := o.Verify(ctx); err != nil {
		return integrity.AnchorPayload{}, fmt.Errorf("refusing to anchor: %w", err)
	}
	return o.ledger.Anchor(ctx, destination)
}

// verifyAnchorDestinations re-reads every anchor destination and checks that the
// recorded line is still present, which is what makes tampering detectable
// outside the database (docs/CONTRACTS.md §3.4).
func verifyAnchorDestinations(anchors []integrity.AnchorPayload) error {
	for _, anchor := range anchors {
		line := fmt.Sprintf("%d %s %s", anchor.Height, anchor.Digest, anchor.At)
		content, err := os.ReadFile(anchor.Destination)
		if err != nil {
			return &integrity.Inconsistency{
				Kind: integrity.KindAnchor, Height: anchor.Height, Field: integrity.FieldDigest,
				Stored: "<unreadable destination " + anchor.Destination + ">", Recomputed: line,
			}
		}
		if !strings.Contains(string(content), line) {
			return &integrity.Inconsistency{
				Kind: integrity.KindAnchor, Height: anchor.Height, Field: integrity.FieldDigest,
				Stored: "<line absent from " + anchor.Destination + ">", Recomputed: line,
			}
		}
	}
	return nil
}

func eventIDOr(provided, prefix string) string {
	if provided != "" {
		return provided
	}
	return prefix + "-" + protocol.NewKey()
}
