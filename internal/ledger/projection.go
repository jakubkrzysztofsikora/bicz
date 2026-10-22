package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jakubkrzysztofsikora/bicz/internal/pressure"
)

// Projection is the compact, read-only context block an identity receives on
// every planning turn (docs/CONTRACTS.md §5.4 `projection`): identity, money,
// role, the outstanding burn, the recovery path out of it, and the lessons that
// apply. It is advisory text and counters only — it grants nothing.
type Projection struct {
	AccountID string `json:"account_id"`
	CreatedAt string `json:"created_at"`
	Balance   int64  `json:"balance"` // K
	Held      int64  `json:"held"`    // H
	Spendable int64  `json:"available"`
	Role      string `json:"role"`
	Warnings  int64  `json:"warnings"`

	LossBurned    int64 `json:"loss_burned"`
	LossRecovered int64 `json:"loss_recovered"`

	// OutstandingLoss is the unrecovered burn the next reward will be presented
	// as repaying; TargetLossEventID names the specific burn it belongs to.
	OutstandingLoss   int64  `json:"outstanding_loss"`
	TargetLossEventID string `json:"target_loss_event_id"`
	TargetLossBurned  int64  `json:"target_loss_burned"`

	// RecoveryPath is the one-line human-readable statement of where the
	// identity stands: how much of which burn is still outstanding, or that the
	// shortfall is closed.
	RecoveryPath string `json:"recovery_path"`

	Lessons []Lesson `json:"lessons"`

	// Penalties are the identity's newest operator-confirmed penalties (at most
	// MaxProjectedPenalties, oldest first). PenaltyTotal counts all of them and
	// PenaltySeq is the ledger position of the newest, which an adapter compares
	// to what it last delivered to notice a new penalty without another model
	// call. Administrative, unclassified, and self-disclosed losses are not here.
	Penalties    []Penalty `json:"penalties"`
	PenaltyTotal int64     `json:"penalty_total"`
	PenaltySeq   int64     `json:"penalty_seq"`

	// EligibleSponsor is the sponsorship G of the identity's newest open task,
	// and TaskCapacity is balance - held + that sponsorship: the spendable
	// capacity for that task. Sponsorship is per-task and never rolls into
	// capital (docs/CONTRACTS.md §1 I5).
	EligibleSponsor int64 `json:"task_sponsor"`
	TaskCapacity    int64 `json:"task_budget"`

	PolicyVersion string `json:"policy_version"`
	EventSeq      int64  `json:"event_seq"`
}

// ProjectionOption adjusts how Projection renders.
type ProjectionOption func(*projectionConfig)

type projectionConfig struct {
	pressure pressure.Level
}

// WithPressure sets the pressure level the penalty cues are worded at; the
// default is pressure.Default.
func WithPressure(level pressure.Level) ProjectionOption {
	return func(cfg *projectionConfig) { cfg.pressure = level }
}

// Projection reads the context block for accountID. It is read-only and appends
// no event.
func (l *Ledger) Projection(ctx context.Context, accountID string, opts ...ProjectionOption) (Projection, error) {
	cfg := projectionConfig{pressure: pressure.Default}
	for _, opt := range opts {
		opt(&cfg)
	}
	account, found, err := loadAccount(ctx, l.db, accountID)
	if err != nil {
		return Projection{}, err
	}
	if !found {
		return Projection{}, errf(ErrUnknownIdentity, "%q", accountID)
	}
	employment, err := loadEmployment(ctx, l.db, accountID)
	if err != nil {
		return Projection{}, err
	}
	state, err := unrecovered(ctx, l.db, accountID)
	if err != nil {
		return Projection{}, err
	}
	lessons, err := l.Lessons(ctx, accountID)
	if err != nil {
		return Projection{}, err
	}
	penalties, err := loadPenalties(ctx, l.db, accountID, state, cfg.pressure)
	if err != nil {
		return Projection{}, err
	}
	sponsor, err := openTaskSponsor(ctx, l.db, accountID)
	if err != nil {
		return Projection{}, err
	}
	var seq int64
	if err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return Projection{}, fmt.Errorf("ledger: read event tip: %w", err)
	}
	policyVersion, err := currentPolicyVersion(ctx, l.db)
	if err != nil {
		return Projection{}, err
	}

	projection := Projection{
		AccountID:         account.ID,
		CreatedAt:         account.CreatedAt,
		Balance:           account.Balance,
		Held:              account.Held,
		Spendable:         account.Spendable(),
		Role:              employment.State,
		Warnings:          employment.Warnings,
		LossBurned:        state.Burned,
		LossRecovered:     state.Recovered,
		OutstandingLoss:   state.Outstanding,
		TargetLossEventID: state.Target,
		TargetLossBurned:  state.TargetBurned,
		Lessons:           lessons,
		PenaltyTotal:      int64(len(penalties)),
		Penalties:         penalties,
		EligibleSponsor:   sponsor,
		TaskCapacity:      account.Spendable() + sponsor,
		PolicyVersion:     policyVersion,
		EventSeq:          seq,
	}
	if len(penalties) > 0 {
		projection.PenaltySeq = penalties[len(penalties)-1].Seq
	}
	if len(projection.Penalties) > MaxProjectedPenalties {
		projection.Penalties = projection.Penalties[len(projection.Penalties)-MaxProjectedPenalties:]
	}
	projection.RecoveryPath = recoveryPath(projection)
	return projection, nil
}

// currentPolicyVersion is the most recently published price schedule: prices
// apply prospectively, so the newest publication is the policy in force.
func currentPolicyVersion(ctx context.Context, q querier) (string, error) {
	var version string
	err := q.QueryRowContext(ctx, `SELECT version FROM price_schedules ORDER BY rowid DESC LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ledger: read current policy version: %w", err)
	}
	return version, nil
}

// recoveryPath renders the restoration framing of docs/CONTRACTS.md §6.2 in one
// line, so a burn has a concrete endpoint the identity can plan against.
func recoveryPath(projection Projection) string {
	switch {
	case projection.OutstandingLoss > 0:
		return fmt.Sprintf("recovered %d of %d microcredits burned in %s; %d outstanding",
			projection.LossRecovered, projection.LossBurned, projection.TargetLossEventID, projection.OutstandingLoss)
	case projection.LossBurned > 0:
		return fmt.Sprintf("loss fully recovered: %d microcredits, no outstanding shortfall", projection.LossBurned)
	default:
		return "no outstanding loss"
	}
}

func openTaskSponsor(ctx context.Context, q querier, accountID string) (int64, error) {
	var sponsor int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(sponsor), 0) FROM tasks WHERE account_id = ? AND state = ?`,
		accountID, "open").Scan(&sponsor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ledger: read task sponsorship for %s: %w", accountID, err)
	}
	return sponsor, nil
}
