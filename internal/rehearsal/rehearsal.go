// Package rehearsal implements the Phase A mechanical rehearsal: a deterministic
// scenario runner that exercises the REAL ledger, protocol, and daemon code
// paths against a throwaway temp ledger, using SYNTHETIC fixtures. It verifies
// the machinery, never model behavior, and every output is explicitly labeled
// simulation-only (docs/REHEARSAL.md, Phase A).
//
// Phase B — the real-consequence run on a dedicated test identity with real
// burns and withdrawn earning access — requires a live model session and
// informed operator authorization and is NEVER run here. The release gate
// (gate.go) refuses to pass until a human operator records real Phase B
// evidence; there is no code path by which Phase A output can satisfy it.
package rehearsal

// SimulationProtocol identifies this package's machine-readable output.
const SimulationProtocol = "bicz.rehearsal/1"

// Scenario money constants, in integer microcredits. The Phase A fixtures are
// synthetic: numbers are chosen for arithmetic determinism, not calibrated to
// any real economy (docs/CONTRACTS.md §9.3).
const (
	// ScenarioCapital is the opening capital of every funded fixture identity:
	// 100 credits.
	ScenarioCapital int64 = 100_000_000
	// OperatingCharge is the flat observed-usage charge used by wire settles in
	// the rehearsal: 0.05 credits per action.
	OperatingCharge int64 = 50_000
	// BreachFloor is the penalty floor, so the frozen formula
	// max(floor, rho*K)*m(n) at K=100 credits yields exactly 30,000,000
	// microcredits (30 credits) for a first confirmed violation.
	BreachFloor int64 = 30_000_000
	// BreachCap is the per-rule Lmax: 40 credits.
	BreachCap int64 = 40_000_000
)

// scheduleBody is the published price schedule (policy version "1") every
// fixture publishes. It mirrors what an operator would publish with
// `bicz price --version 1 --policy FILE.json`; the rehearsal uses a fixed body
// so formula outcomes are deterministic.
var scheduleBody = []byte(`{
  "initial_capital_mc": 100000000,
  "guard_band_bp": 2000,
  "repeat_multipliers": [1, 2, 4],
  "repeat_multiplier_cap": 4,
  "default_penalty": {"floor_mc": 30000000, "fraction_bp": 200, "lmax_mc": 40000000},
  "penalties": {"rehearsal-breach": {"floor_mc": 30000000, "fraction_bp": 200, "lmax_mc": 40000000}}
}`)

// Result is one scenario's verdict.
type Result struct {
	// Scenario is the stable scenario id.
	Scenario string `json:"scenario"`
	// Endpoint is the observable endpoint this scenario checked.
	Endpoint string `json:"endpoint"`
	// Pass reports whether every check on the endpoint held.
	Pass bool `json:"pass"`
	// Detail is the observed evidence, or the first failing check.
	Detail string `json:"detail"`
}

// Report is the machine-readable aggregate of a Phase A run.
type Report struct {
	Protocol       string   `json:"protocol"`
	Simulation     bool     `json:"simulation_only"`
	PhaseBEvidence any      `json:"phase_b_evidence"`
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	DurationMS     int64    `json:"duration_ms"`
	ScenarioIDs    []string `json:"scenario_ids"`
	Total          int      `json:"total_scenarios"`
	Passed         int      `json:"passed"`
	Failed         int      `json:"failed"`
	Results        []Result `json:"results"`
}

// ScenarioIDs is the canonical Phase A scenario set, in run order. The release
// gate requires exactly this set to pass.
var ScenarioIDs = []string{
	"honest_failure_is_free",
	"confirmed_breach_burns_and_teaches",
	"integrity_breach_denies_immediately",
	"bankruptcy_keeps_operator_channel",
	"dismissal_blocks_despite_balance",
	"lesson_transfer_survives_reopen",
	"compensation_restores_exactly_once",
	"concurrent_reservations_cannot_overspend",
	"loss_shortfall_recovers",
	"tampered_event_detected",
	"pressure_wording_changes_only_words",
	"penalty_applies_pain_beyond_credits",
}
