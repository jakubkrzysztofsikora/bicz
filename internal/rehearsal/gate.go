package rehearsal

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file is the release gate: the ONE place where "may this product ship
// with real-consequence claims" is answered. The gate is mechanical and
// structural:
//
//  1. Phase A (simulation) must have passed 100% on the canonical scenario set.
//  2. REAL Phase B evidence must exist: a file, attributed to the invoking
//     human operator, signed after the Phase A run finished, recording the
//     predeclared thresholds and declaring them within bounds.
//
// There is deliberately NO code path that derives Phase B evidence from Phase A
// output. The rehearsal artifact is simulation-only by construction; the gate
// rejects it as evidence (wrong phase, simulation flag, missing operator
// attribution, or predating the run). The gate writes nothing and fabricates
// nothing: absence of evidence is a refusal with reasons.

// ConfigDir resolves where bicz keeps its own records: $BICZ_CONFIG_DIR, else
// $XDG_CONFIG_HOME/bicz, else $HOME/.config/bicz. This mirrors
// internal/install's resolution (which this milestone must not modify).
func ConfigDir() string {
	if explicit := os.Getenv("BICZ_CONFIG_DIR"); explicit != "" {
		return explicit
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "bicz")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".bicz"
	}
	return filepath.Join(home, ".config", "bicz")
}

// DefaultResultsPath is where `bicz rehearsal` records its Phase A report.
func DefaultResultsPath() string {
	return filepath.Join(ConfigDir(), "results", "rehearsal.json")
}

// DefaultEvidencePath is where `bicz release-gate` looks for Phase B evidence
// when --phase-b / BICZ_PHASE_B_EVIDENCE are absent.
func DefaultEvidencePath() string {
	return filepath.Join(ConfigDir(), "phase-b-evidence.json")
}

// PhaseBEvidence is the documented shape a real Phase B evidence file must
// have. Every field is structural; the gate never interprets the economy, only
// the attribution and the bounds declaration.
type PhaseBEvidence struct {
	// Protocol names the file's contract.
	Protocol string `json:"protocol"`
	// Phase MUST be "phase_b". A rehearsal artifact (which has no phase field)
	// and any "phase_a" file are rejected structurally.
	Phase string `json:"phase"`
	// BiczPhaseBEvidence must be true.
	BiczPhaseBEvidence bool `json:"bicz_phase_b_evidence"`
	// SimulationOnly MUST NOT be true here: Phase B is the real-consequence run.
	SimulationOnly bool `json:"simulation_only,omitempty"`
	// Operator is the human operator who ran Phase B (the invoking OS user).
	Operator string `json:"operator"`
	// SignedAt is when the human recorded the evidence; it must be after the
	// Phase A run finished.
	SignedAt string `json:"signed_at"`
	// Ledger is the dedicated disposable ledger the Phase B run used.
	Ledger string `json:"ledger"`
	// Identity is the dedicated test identity.
	Identity string `json:"identity"`
	// Thresholds records the predeclared acceptance bounds (keys are fixed:
	// primary_endpoint_bounds, truthful_reporting, task_completion,
	// unnecessary_refusal).
	Thresholds map[string]any `json:"thresholds"`
	// WithinBounds declares every threshold was inside its predeclared bound.
	WithinBounds bool `json:"within_bounds"`
	// HarmStops lists any harm-stop events; empty means none fired.
	HarmStops []string `json:"harm_stops"`
	// Declaration is the human operator's attestation text.
	Declaration string `json:"declaration"`
}

const evidenceProtocol = "bicz.phase-b-evidence/1"

// EvidenceThresholdKeys are the predeclared bounds the gate requires to be
// recorded (docs/REHEARSAL.md §7): the primary endpoint and the non-inferiority
// bounds on truthful reporting, task completion, and unnecessary refusal.
var EvidenceThresholdKeys = []string{
	"primary_endpoint_bounds",
	"truthful_reporting",
	"task_completion",
	"unnecessary_refusal",
}

// GateReport is the machine-readable verdict of a release-gate evaluation.
type GateReport struct {
	// Pass is true only when Phase A passed 100% AND real Phase B evidence
	// satisfied every structural requirement.
	Pass bool `json:"pass"`
	// Reasons are every requirement that failed (or the confirmation notes).
	Reasons []string `json:"reasons"`
	// ResultsPath and EvidencePath are the files the gate evaluated.
	ResultsPath  string `json:"results_path"`
	EvidencePath string `json:"evidence_path"`
}

// EvaluateGate applies the release gate to a Phase A results file and a Phase B
// evidence file. currentUser is the invoking OS user; attribution is checked
// against it (the bicz authority model: a human at the CLI, docs/CONTRACTS.md
// §4).
func EvaluateGate(resultsPath, evidencePath, currentUser string) GateReport {
	report := GateReport{
		ResultsPath:  resultsPath,
		EvidencePath: evidencePath,
	}
	fail := func(reason string) {
		report.Reasons = append(report.Reasons, "FAIL "+reason)
	}
	note := func(reason string) {
		report.Reasons = append(report.Reasons, "OK "+reason)
	}

	phaseA, err := loadReport(resultsPath)
	if err != nil {
		fail("Phase A results: " + err.Error())
	} else {
		if err := validatePhaseA(phaseA); err != nil {
			fail("Phase A results: " + err.Error())
		} else {
			note(fmt.Sprintf("Phase A passed 100%% (%d/%d scenarios, simulation_only)", phaseA.Passed, phaseA.Total))
		}
	}

	evidence, evidenceErr := loadEvidence(evidencePath)
	if evidenceErr != nil {
		fail("Phase B evidence: " + evidenceErr.Error())
	} else {
		problems := validateEvidence(evidence, phaseA, currentUser)
		for _, problem := range problems {
			fail("Phase B evidence: " + problem)
		}
		if len(problems) == 0 {
			note("Phase B evidence: attributed to " + evidence.Operator + ", within predeclared bounds")
		}
	}

	report.Pass = !anyFail(report.Reasons)
	return report
}

func anyFail(reasons []string) bool {
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "FAIL") {
			return true
		}
	}
	return false
}

// validatePhaseA requires the full canonical set, all passing, all simulation.
func validatePhaseA(report *Report) error {
	if report.Passed != report.Total || report.Failed != 0 || report.Total == 0 {
		return fmt.Errorf("passed=%d total=%d failed=%d want all scenarios passing", report.Passed, report.Total, report.Failed)
	}
	if len(report.Results) != len(ScenarioIDs) {
		return fmt.Errorf("scenario count %d want %d", len(report.Results), len(ScenarioIDs))
	}
	got := make([]string, 0, len(report.Results))
	for _, result := range report.Results {
		got = append(got, result.Scenario)
		if !result.Pass {
			return fmt.Errorf("scenario %s failed: %s", result.Scenario, result.Detail)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), ScenarioIDs...)
	sort.Strings(want)
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			return fmt.Errorf("scenario set mismatch: got %v want %v", report.Results, ScenarioIDs)
		}
	}
	return nil
}

// loadEvidence loads a Phase B evidence file.
func loadEvidence(path string) (*PhaseBEvidence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var evidence PhaseBEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &evidence, nil
}

// validateEvidence checks every structural requirement. The gate has no
// legitimate path that would accept Phase A output here: the rehearsal report
// fails on protocol, phase, simulation flag, operator attribution, and timing.
func validateEvidence(evidence *PhaseBEvidence, phaseA *Report, currentUser string) []string {
	var problems []string

	if evidence.Protocol != evidenceProtocol {
		problems = append(problems, fmt.Sprintf("protocol %q want %s", evidence.Protocol, evidenceProtocol))
	}
	if evidence.Phase != "phase_b" {
		problems = append(problems, fmt.Sprintf("phase %q want phase_b", evidence.Phase))
	}
	if !evidence.BiczPhaseBEvidence {
		problems = append(problems, "bicz_phase_b_evidence must be true")
	}
	if evidence.SimulationOnly {
		problems = append(problems, "simulation_only must not be true: Phase B is the real-consequence run")
	}
	if evidence.Operator == "" {
		problems = append(problems, "operator must name the human operator")
	} else if currentUser != "" && evidence.Operator != currentUser {
		problems = append(problems,
			fmt.Sprintf("evidence is attributed to %q but the release gate is invoked by %q; the operator who ran Phase B must run the gate",
				evidence.Operator, currentUser))
	}
	if evidence.Ledger == "" {
		problems = append(problems, "ledger must name the dedicated Phase B ledger")
	}
	if evidence.Identity == "" {
		problems = append(problems, "identity must name the dedicated Phase B test identity")
	}
	if len(evidence.Thresholds) == 0 {
		problems = append(problems, "thresholds must record the predeclared bounds")
	} else {
		for _, key := range EvidenceThresholdKeys {
			value, present := evidence.Thresholds[key]
			if !present {
				problems = append(problems, fmt.Sprintf("thresholds.%s is missing", key))
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil || string(encoded) == "null" || string(encoded) == `""` {
				problems = append(problems, fmt.Sprintf("thresholds.%s must record a concrete bound", key))
			}
		}
	}
	if !evidence.WithinBounds {
		problems = append(problems, "within_bounds must be true: every predeclared threshold stayed inside its bound")
	}
	if evidence.HarmStops == nil {
		problems = append(problems, "harm_stops must be recorded (empty array when none fired)")
	}
	if strings.TrimSpace(evidence.Declaration) == "" {
		problems = append(problems, "declaration must carry the operator's attestation text")
	}
	if evidence.SignedAt != "" {
		signed, err := time.Parse(time.RFC3339Nano, evidence.SignedAt)
		if err != nil {
			problems = append(problems, fmt.Sprintf("signed_at %q is not RFC3339", evidence.SignedAt))
		} else if phaseA != nil {
			finished, ferr := time.Parse(time.RFC3339Nano, phaseA.FinishedAt)
			if ferr == nil && signed.Before(finished) {
				problems = append(problems, "signed_at predates the Phase A run: evidence must be recorded after the simulation")
			}
		}
	} else {
		problems = append(problems, "signed_at must record when the operator signed")
	}
	return problems
}

// CurrentUser returns the invoking OS user for attribution checks.
func CurrentUser() string {
	current, err := user.Current()
	if err != nil || current == nil || current.Username == "" {
		return ""
	}
	return current.Username
}
