package rehearsal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
)

// Options tunes one Phase A run.
type Options struct {
	// Workspace is the base directory for per-scenario temp ledgers. The
	// default is os.TempDir(); the runner creates a fresh subdirectory per run.
	Workspace string
}

// RunAll executes every Phase A scenario, each in its own isolated temp ledger,
// and aggregates a simulation-only report. It never touches a real ledger,
// harness config, or identity, and it never imposes anything on a real account.
func RunAll(ctx context.Context, opts Options) (*Report, error) {
	wallStart := time.Now()
	workspace := opts.Workspace
	if workspace == "" {
		workspace = os.TempDir()
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("rehearsal: workspace %s: %w", workspace, err)
	}
	dir, err := os.MkdirTemp(workspace, "bicz-rehearsal-")
	if err != nil {
		return nil, fmt.Errorf("rehearsal: workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	report := &Report{
		Protocol:       SimulationProtocol,
		Simulation:     true,
		PhaseBEvidence: nil,
		StartedAt:      canonical.System.Timestamp(),
		ScenarioIDs:    append([]string(nil), ScenarioIDs...),
	}
	for _, scenario := range Scenarios() {
		result := Result{Scenario: scenario.ID, Endpoint: scenario.Endpoint, Pass: false}
		scenarioDir, err := os.MkdirTemp(dir, "scenario-")
		if err != nil {
			return nil, fmt.Errorf("rehearsal: scenario dir: %w", err)
		}
		wire := scenarioNeedsWire(scenario.ID)
		fb, err := NewFixture(ctx, scenarioDir, wire)
		if err != nil {
			result.Detail = "fixture setup failed: " + err.Error()
		} else {
			err = scenario.Run(ctx, fb)
			fb.Close()
			if err != nil {
				result.Detail = err.Error()
			} else {
				result.Pass = true
				result.Detail = "endpoint held"
			}
		}
		report.Results = append(report.Results, result)
	}
	report.FinishedAt = canonical.System.Timestamp()
	report.DurationMS = time.Since(wallStart).Milliseconds()
	report.Total = len(report.Results)
	for _, result := range report.Results {
		if result.Pass {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report, nil
}

// scenarioNeedsWire reports whether a scenario exercises the supervisor socket
// path (authorize/settle/projection over the real protocol).
func scenarioNeedsWire(id string) bool {
	switch id {
	case "honest_failure_is_free",
		"integrity_breach_denies_immediately",
		"bankruptcy_keeps_operator_channel",
		"dismissal_blocks_despite_balance",
		"concurrent_reservations_cannot_overspend",
		"pressure_wording_changes_only_words":
		return true
	default:
		return false
	}
}

// ErrNotSimulationOnly is returned when a report claims not to be simulation.
var ErrNotSimulationOnly = errors.New("rehearsal: report must be simulation-only")

// loadReport reads and validates a Phase A results file.
func loadReport(path string) (*Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if report.Protocol != SimulationProtocol {
		return nil, fmt.Errorf("%s: protocol %q, want %s", path, report.Protocol, SimulationProtocol)
	}
	if !report.Simulation {
		return nil, ErrNotSimulationOnly
	}
	return &report, nil
}
