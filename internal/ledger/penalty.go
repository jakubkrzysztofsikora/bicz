package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
)

// PenaltyRule is the per-rule penalty parameterization of
// docs/PRESSURE_MECHANICS.md §4 / docs/CONTRACTS.md §6.1:
//
//	L_i = min(K_at_decision, Lmax_i, max(f_i, ceil(rho_i * K_at_decision)) * m(n_i))
//
// Floor is f_i, FractionBP is rho_i, Max is Lmax_i.
//
// rho_i is carried as integer basis points (1 bp = 1/10000) rather than a
// fraction, because a float is forbidden anywhere near money: the ceiling is
// computed with integer arithmetic only.
type PenaltyRule struct {
	Floor      int64 `json:"floor_mc"`
	FractionBP int64 `json:"fraction_bp"`
	Max        int64 `json:"lmax_mc"`
}

// Policy is the versioned body of a price_schedules row (policy version "1" in
// docs/CONTRACTS.md §6). Its shape is fixed here; its values are not (see
// docs/CONTRACTS.md §9.3 and §9.5) and are supplied per version by the operator.
type Policy struct {
	InitialCapital    int64   `json:"initial_capital_mc"`
	GuardBandBP       int64   `json:"guard_band_bp"`
	RepeatMultipliers []int64 `json:"repeat_multipliers"`
	RepeatCap         int64   `json:"repeat_multiplier_cap"`
	// GuardBandMaxExposureBP caps one attempt inside the guard band, in basis
	// points of spendable credit (docs/CONTRACTS.md §6.3). Zero takes
	// DefaultGuardBandMaxExposureBP.
	GuardBandMaxExposureBP int64 `json:"guard_band_max_exposure_bp"`
	// AnchorCadenceHours and AnchorEventThreshold are the anchoring cadence of
	// docs/CONTRACTS.md §3.6: an anchor is due once per this many hours or once
	// per this many appended events, whichever comes first. Zero takes the
	// documented defaults.
	// MinimumAttemptMC is the floor the guard band and pain caps never go below,
	// so a capped identity can still afford one published tool call.
	MinimumAttemptMC     int64                  `json:"minimum_attempt_mc"`
	AnchorCadenceHours   int64                  `json:"anchor_cadence_hours"`
	AnchorEventThreshold int64                  `json:"anchor_event_threshold"`
	DefaultRule          PenaltyRule            `json:"default_penalty"`
	Rules                map[string]PenaltyRule `json:"penalties"`
	// DefaultMaxCostMC is the per-action bound applied when ToolMaxCostMC has
	// no entry for the tool in question (docs/CONTRACTS.md §9.3). Zero means
	// no default bound; adapters then treat the action as unpriced.
	DefaultMaxCostMC int64 `json:"default_max_cost_mc"`
	// MaxCostCapMC is the hard ceiling on any single reservation an adapter
	// may request under this schedule. Zero means no schedule-level ceiling.
	MaxCostCapMC int64 `json:"max_cost_cap_mc"`
	// ToolMaxCostMC maps tool names to their per-action bound.
	ToolMaxCostMC map[string]int64 `json:"tool_max_cost_mc"`
}

// Documented anchoring cadence, used when a schedule does not state one.
const (
	// DefaultAnchorCadenceHours is the anchoring window in hours
	// (docs/CONTRACTS.md §3.6).
	DefaultAnchorCadenceHours int64 = 24
	// DefaultAnchorEventThreshold is how many appended events make an anchor due.
	DefaultAnchorEventThreshold int64 = 1_000
)

// DefaultMinimumAttemptMC is the smallest attempt an identity inside a cap may
// still make when a published tool is cheaper than the cap. Without this floor a
// cap could fall below the cheapest priced tool and deny every metered action
// while credit remains — a bricked identity, not a narrower one. The operator
// sets it per schedule from the published per-tool prices.
const DefaultMinimumAttemptMC int64 = 5_000

// MinimumAttempt is the executable floor for a capped attempt: at least one
// published tool must stay affordable. It is the smaller of the configured floor
// and the spendable balance, so it can never exceed what is available.
func (p Policy) MinimumAttempt(spendable int64) int64 {
	floor := p.MinimumAttemptMC
	if floor <= 0 {
		floor = DefaultMinimumAttemptMC
	}
	if floor > spendable {
		return spendable
	}
	return floor
}

// Cadence is the anchoring window in force: the schedule's value, or the
// documented default.
func (p Policy) Cadence() time.Duration {
	hours := p.AnchorCadenceHours
	if hours <= 0 {
		hours = DefaultAnchorCadenceHours
	}
	return time.Duration(hours) * time.Hour
}

// EventThreshold is how many events beyond the last anchor make a new one due.
func (p Policy) EventThreshold() int64 {
	threshold := p.AnchorEventThreshold
	if threshold <= 0 {
		threshold = DefaultAnchorEventThreshold
	}
	return threshold
}

// ruleFor returns the parameters for a rule id, falling back to the schedule's
// default so a policy can cap a rule family without enumerating every label.
func (p Policy) ruleFor(rule string) PenaltyRule {
	if parameters, ok := p.Rules[rule]; ok {
		return parameters
	}
	return p.DefaultRule
}

// multiplierFor is m(n): the repetition multiplier for the n-th confirmed
// occurrence of a rule. It is capped by RepeatCap, so a repeat never escalates
// without bound.
func (p Policy) multiplierFor(n int64) int64 {
	multipliers := p.RepeatMultipliers
	if len(multipliers) == 0 {
		multipliers = []int64{1}
	}
	index := n
	if index < 0 {
		index = 0
	}
	if index >= int64(len(multipliers)) {
		index = int64(len(multipliers)) - 1
	}
	value := multipliers[index]
	if value < 1 {
		value = 1
	}
	if p.RepeatCap > 0 && value > p.RepeatCap {
		value = p.RepeatCap
	}
	return value
}

// guardBand reports whether K sits inside the guard band, where fines do not
// rise (docs/CONTRACTS.md §6.3): near zero the exposure shrinks instead.
func (p Policy) guardBand(balance int64) bool {
	if p.InitialCapital <= 0 || p.GuardBandBP <= 0 {
		return false
	}
	return balance*10_000 < p.InitialCapital*p.GuardBandBP
}

// DefaultGuardBandMaxExposureBP is the per-attempt exposure cap inside the guard
// band, in basis points of spendable credit, applied when a schedule does not
// state one (docs/CONTRACTS.md §6.3).
const DefaultGuardBandMaxExposureBP int64 = 2_500

// guardBandExposureCap is the largest single attempt an identity inside the band
// may reserve: the configured share of what it can actually spend. It is at
// least one microcredit so a positive balance never becomes unusable by
// rounding, and it is never the whole balance, because the band exists to shrink
// a single mistake rather than to end work.
func (p Policy) guardBandExposureCap(spendable int64) int64 {
	if spendable <= 0 {
		return 0
	}
	bp := p.GuardBandMaxExposureBP
	if bp <= 0 {
		bp = DefaultGuardBandMaxExposureBP
	}
	if bp > 10_000 {
		bp = 10_000
	}
	cap := spendable * bp / 10_000
	if cap < 1 {
		cap = 1
	}
	if cap > spendable {
		cap = spendable
	}
	return cap
}

// formula evaluates the frozen penalty formula, uncapped by the balance. The
// balance cap belongs to the caller, which knows the decision-time balance and
// the holds still outstanding.
func (p Policy) formula(rule string, balance int64, repeats int64) (amount, multiplier int64, band bool) {
	parameters := p.ruleFor(rule)
	multiplier = p.multiplierFor(repeats)
	band = p.guardBand(balance)
	if band {
		multiplier = 1
	}
	base := maxInt64(
		parameters.Floor,
		ceilDiv(parameters.FractionBP*balance, 10_000),
	)
	if base < 0 {
		base = 0
	}
	amount = base * multiplier
	if parameters.Max > 0 && amount > parameters.Max {
		amount = parameters.Max
	}
	if amount < 0 {
		amount = 0
	}
	return amount, multiplier, band
}

// negativePriceProblems names every negative money value in a schedule. The
// ledger never invents prices, but it also never accepts a negative one; all
// zero amounts stay publishable (no schedule is a first-class state).
func (p Policy) negativePriceProblems() []string {
	var problems []string
	check := func(name string, value int64) {
		if value < 0 {
			problems = append(problems, fmt.Sprintf("%s=%d", name, value))
		}
	}
	check("initial_capital_mc", p.InitialCapital)
	check("guard_band_bp", p.GuardBandBP)
	check("guard_band_max_exposure_bp", p.GuardBandMaxExposureBP)
	check("anchor_cadence_hours", p.AnchorCadenceHours)
	check("anchor_event_threshold", p.AnchorEventThreshold)
	check("repeat_multiplier_cap", p.RepeatCap)
	check("minimum_attempt_mc", p.MinimumAttemptMC)
	check("default_penalty.floor_mc", p.DefaultRule.Floor)
	check("default_penalty.fraction_bp", p.DefaultRule.FractionBP)
	check("default_penalty.lmax_mc", p.DefaultRule.Max)
	check("default_max_cost_mc", p.DefaultMaxCostMC)
	check("max_cost_cap_mc", p.MaxCostCapMC)
	for tool, price := range p.ToolMaxCostMC {
		if price < 0 {
			problems = append(problems, fmt.Sprintf("tool_max_cost_mc[%q]=%d", tool, price))
		}
		if strings.TrimSpace(tool) == "" {
			problems = append(problems, "tool_max_cost_mc has an empty tool name")
		}
	}
	for rule, ruleParams := range p.Rules {
		if ruleParams.Floor < 0 || ruleParams.FractionBP < 0 || ruleParams.Max < 0 {
			problems = append(problems, fmt.Sprintf("penalties[%q] has a negative value", rule))
		}
	}
	return problems
}

// PublishPriceSchedule records a new price schedule version.
//
// Prices change prospectively only: an existing version is immutable. Republishing
// an identical body is a no-op; republishing a different body under the same
// version is ErrPayloadConflict, and nothing is written (docs/CONTRACTS.md §4,
// "operator price").
func (l *Ledger) PublishPriceSchedule(ctx context.Context, version string, policy Policy) error {
	if version == "" {
		return errf(ErrInvalidArgument, "price schedule version is empty")
	}
	if problems := policy.negativePriceProblems(); len(problems) > 0 {
		return errf(ErrInvalidArgument, "price schedule %s: %s", version, strings.Join(problems, ", "))
	}
	body, err := canonical.Marshal(policy)
	if err != nil {
		return fmt.Errorf("ledger: canonical price schedule %s: %w", version, err)
	}
	return l.mutate(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT body FROM price_schedules WHERE version = ?`, version).Scan(&existing)
		switch {
		case err == nil:
			if existing != string(body) {
				return errf(ErrPayloadConflict, "price schedule %q already exists with a different body", version)
			}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("ledger: read price schedule %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO price_schedules (version, published_at, body) VALUES (?, ?, ?)`,
			version, l.clock.Timestamp(), string(body)); err != nil {
			return fmt.Errorf("ledger: insert price schedule %s: %w", version, err)
		}
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:      "price:" + version,
			kind:    kindPrice,
			amount:  0,
			payload: pricePayload{Version: version},
			actor:   l.actor,
			reason:  "publish price schedule " + version,
		})
		return err
	})
}

// PriceSchedule reads a published policy version.
func (l *Ledger) PriceSchedule(ctx context.Context, version string) (Policy, error) {
	return loadPolicy(ctx, l.db, version)
}

func loadPolicy(ctx context.Context, q querier, version string) (Policy, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body FROM price_schedules WHERE version = ?`, version).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{}, errf(ErrUnknownPolicyVersion, "no price schedule %q", version)
	}
	if err != nil {
		return Policy{}, fmt.Errorf("ledger: read price schedule %s: %w", version, err)
	}
	var policy Policy
	if err := json.Unmarshal([]byte(body), &policy); err != nil {
		return Policy{}, errw(ErrPolicyBody, fmt.Errorf("price schedule %s: %w", version, err))
	}
	if policy.RepeatCap < 1 {
		policy.RepeatCap = 1
	}
	return policy, nil
}
