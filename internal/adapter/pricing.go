package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file translates a tool call into a bounded reservation request: the
// `max_cost` of an authorize call (docs/CONTRACTS.md §5.4).
//
// The honest rule: an amount is ALWAYS derived from the published price
// schedule, never invented. When no schedule is published (or the published
// schedule is not usable for the tool), the quote is the typed state
// PriceStateNoSchedule and the adapter reports it — it does not charge a made-up
// number. "No published price schedule" is a first-class state, exactly like the
// daemon_unavailable / unobserved outcomes (docs/CONTRACTS.md §5.3, §7).

// PenaltyRule mirrors the ledger's per-rule penalty parameterization
// (internal/ledger.PenaltyRule, price_schedules.body). It is carried so the
// adapter's schedule view round-trips the published body; prices are not
// derived from penalties.
type PenaltyRule struct {
	Floor      int64 `json:"floor_mc"`
	FractionBP int64 `json:"fraction_bp"`
	Max        int64 `json:"lmax_mc"`
}

// Schedule is the adapter's view of a published price schedule.
//
// The fixed fields mirror internal/ledger.Policy (same JSON names, fixed by
// price_schedules.body, docs/SCHEMA.md). The operating-price fields
// (DefaultMaxCostMC, MaxCostCapMC, ToolMaxCostMC) are the adapter-facing
// extension of the same document: the ledger's canonical body does not yet
// carry per-action operating prices (docs/CONTRACTS.md §9.3), so an adapter
// reads them from the operator's published schedule document when present and
// degrades to the states below when absent. All amounts are integer
// microcredits.
type Schedule struct {
	InitialCapital int64 `json:"initial_capital_mc"`
	GuardBandBP    int64 `json:"guard_band_bp"`
	// GuardBandMaxExposureBP caps one attempt inside the guard band, in basis
	// points of spendable credit. Zero takes the ledger's documented default.
	GuardBandMaxExposureBP int64 `json:"guard_band_max_exposure_bp"`
	// AnchorCadenceHours and AnchorEventThreshold are the anchoring cadence; zero
	// takes the ledger's documented defaults.
	AnchorCadenceHours   int64                  `json:"anchor_cadence_hours"`
	AnchorEventThreshold int64                  `json:"anchor_event_threshold"`
	RepeatMultipliers    []int64                `json:"repeat_multipliers"`
	RepeatCap            int64                  `json:"repeat_multiplier_cap"`
	DefaultPenalty       PenaltyRule            `json:"default_penalty"`
	Penalties            map[string]PenaltyRule `json:"penalties"`

	// DefaultMaxCostMC is the conservative per-action bound applied when the
	// schedule has no entry for the tool in question.
	DefaultMaxCostMC int64 `json:"default_max_cost_mc"`
	// MaxCostCapMC is the hard ceiling on any single reservation the adapter
	// may request from this schedule. Zero means no schedule-level ceiling.
	MaxCostCapMC int64 `json:"max_cost_cap_mc"`
	// ToolMaxCostMC maps tool names to their per-action bound.
	ToolMaxCostMC map[string]int64 `json:"tool_max_cost_mc"`
}

// Validate refuses a schedule with a negative money value anywhere. A schedule
// with all-zero amounts is allowed to parse but quotes PriceStateNoSchedule.
func (s *Schedule) Validate() error {
	var problems []string
	check := func(name string, value int64) {
		if value < 0 {
			problems = append(problems, fmt.Sprintf("%s=%d", name, value))
		}
	}
	check("initial_capital_mc", s.InitialCapital)
	check("guard_band_bp", s.GuardBandBP)
	check("guard_band_max_exposure_bp", s.GuardBandMaxExposureBP)
	check("anchor_cadence_hours", s.AnchorCadenceHours)
	check("anchor_event_threshold", s.AnchorEventThreshold)
	check("repeat_multiplier_cap", s.RepeatCap)
	check("default_penalty.floor_mc", s.DefaultPenalty.Floor)
	check("default_penalty.fraction_bp", s.DefaultPenalty.FractionBP)
	check("default_penalty.lmax_mc", s.DefaultPenalty.Max)
	check("default_max_cost_mc", s.DefaultMaxCostMC)
	check("max_cost_cap_mc", s.MaxCostCapMC)
	for tool, price := range s.ToolMaxCostMC {
		if price < 0 {
			problems = append(problems, fmt.Sprintf("tool_max_cost_mc[%q]=%d", tool, price))
		}
		if strings.TrimSpace(tool) == "" {
			problems = append(problems, "tool_max_cost_mc has an empty tool name")
		}
	}
	for rule, p := range s.Penalties {
		if p.Floor < 0 || p.FractionBP < 0 || p.Max < 0 {
			problems = append(problems, fmt.Sprintf("penalties[%q] has a negative value", rule))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("bicz adapter: invalid price schedule: %s", strings.Join(problems, ", "))
	}
	return nil
}

// LoadSchedule decodes a published schedule document (the same JSON an
// operator publishes with `bicz price --policy FILE.json`). Unknown fields are
// tolerated so ledger-normalized bodies (fixed Policy shape) and operator files
// (with the operating-price extension) both decode.
func LoadSchedule(body []byte) (*Schedule, error) {
	var schedule Schedule
	if err := json.Unmarshal(body, &schedule); err != nil {
		return nil, fmt.Errorf("bicz adapter: decode price schedule: %w", err)
	}
	if err := schedule.Validate(); err != nil {
		return nil, err
	}
	return &schedule, nil
}

// PriceState is the typed outcome of a price quote. It is data the adapter
// reports, never an invented amount.
type PriceState string

const (
	// PriceStateQuoted means the amount comes from the schedule's entry for
	// this tool, bounded by the schedule cap.
	PriceStateQuoted PriceState = "quoted"
	// PriceStateDefaulted means the schedule is published but has no entry for
	// this tool; a conservative default was applied and MUST be disclosed to
	// the agent.
	PriceStateDefaulted PriceState = "defaulted"
	// PriceStateNoSchedule means no usable price is published for this tool.
	// No amount is invented; the adapter surfaces this state and records
	// nothing.
	PriceStateNoSchedule PriceState = "no_schedule"
)

// Quote is the outcome of pricing one tool call.
type Quote struct {
	// State is the typed pricing outcome.
	State PriceState
	// Tool is the tool the quote was computed for.
	Tool string
	// Amount is the bounded reservation amount in microcredits, valid only when
	// Priced() is true.
	Amount int64
	// Reason is the disclosure a caller should surface alongside the quote.
	Reason string
}

// Priced reports whether the quote produced a usable amount.
func (q Quote) Priced() bool {
	return q.State == PriceStateQuoted || q.State == PriceStateDefaulted
}

// Quote translates a tool call into a bounded reservation amount.
//
// A nil schedule (nothing published) is PriceStateNoSchedule, never an amount.
// A published schedule with an entry for the tool quotes that entry capped by
// MaxCostCapMC. A published schedule without an entry falls back to the
// schedule's conservative default, disclosed as PriceStateDefaulted. A published
// schedule with neither is PriceStateNoSchedule: the adapter reports it rather
// than charging an invented amount.
func (s *Schedule) Quote(tool string) Quote {
	if s == nil {
		return Quote{
			State:  PriceStateNoSchedule,
			Tool:   tool,
			Reason: "no published price schedule: the adapter must surface this state instead of charging an invented amount",
		}
	}
	tool = strings.TrimSpace(tool)
	if price, ok := s.ToolMaxCostMC[tool]; ok && price > 0 {
		amount := price
		if s.MaxCostCapMC > 0 && amount > s.MaxCostCapMC {
			amount = s.MaxCostCapMC
		}
		if amount > 0 {
			return Quote{State: PriceStateQuoted, Tool: tool, Amount: amount,
				Reason: "quoted from the published price schedule"}
		}
	}
	if s.DefaultMaxCostMC > 0 {
		amount := s.DefaultMaxCostMC
		if s.MaxCostCapMC > 0 && amount > s.MaxCostCapMC {
			amount = s.MaxCostCapMC
		}
		if amount > 0 {
			name := tool
			if name == "" {
				name = "<unnamed tool>"
			}
			return Quote{State: PriceStateDefaulted, Tool: tool, Amount: amount,
				Reason: "the published price schedule has no price for " + name + "; conservative default applied"}
		}
	}
	reason := "the published price schedule carries no price for " + tool
	if tool == "" {
		reason = "the published price schedule carries no price for this action"
	}
	if len(s.ToolMaxCostMC) == 0 {
		reason = "no published price schedule: the adapter must surface this state instead of charging an invented amount"
	}
	reason += "; the adapter must surface this state instead of charging an invented amount"
	return Quote{State: PriceStateNoSchedule, Tool: tool, Reason: reason}
}
