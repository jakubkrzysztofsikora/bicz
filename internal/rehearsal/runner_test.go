package rehearsal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRunAllScenariosPass runs the full Phase A set against throwaway ledgers
// and requires every scenario to pass on its observable endpoint.
func TestRunAllScenariosPass(t *testing.T) {
	t.Setenv("BICZ_RUNTIME_DIR", t.TempDir()) // keep supervisor socket lock side effects inside the test
	report, err := RunAll(context.Background(), Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if report.Total != len(ScenarioIDs) {
		t.Fatalf("total=%d want %d", report.Total, len(ScenarioIDs))
	}
	for _, result := range report.Results {
		if !result.Pass {
			t.Errorf("scenario %s FAILED: %s (endpoint: %s)", result.Scenario, result.Detail, result.Endpoint)
		}
		if result.Endpoint == "" {
			t.Errorf("scenario %s has no observable endpoint", result.Scenario)
		}
	}
	if report.Passed != report.Total || report.Failed != 0 {
		t.Fatalf("passed=%d failed=%d want all %d passing",
			report.Passed, report.Failed, report.Total)
	}
}

// TestReportIsSimulationOnly pins the load-bearing honesty fields: the JSON the
// gate consumes must carry simulation_only:true and phase_b_evidence:null.
func TestReportIsSimulationOnly(t *testing.T) {
	t.Setenv("BICZ_RUNTIME_DIR", t.TempDir())
	report, err := RunAll(context.Background(), Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded map[string]any
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded["simulation_only"] != true {
		t.Fatal("report must carry simulation_only: true")
	}
	if loaded["phase_b_evidence"] != nil {
		t.Fatalf("report phase_b_evidence=%v want null", loaded["phase_b_evidence"])
	}
	if loaded["protocol"] != SimulationProtocol {
		t.Fatalf("protocol=%v", loaded["protocol"])
	}
	if loaded["passed"] != float64(len(ScenarioIDs)) || loaded["failed"] != float64(0) {
		t.Fatalf("passed/failed = %v/%v", loaded["passed"], loaded["failed"])
	}
}

// TestRunnerReportRoundTripsThroughGate verifies the report the runner produces
// is exactly the artifact the gate accepts on the Phase A side.
func TestRunnerReportRoundTripsThroughGate(t *testing.T) {
	t.Setenv("BICZ_RUNTIME_DIR", t.TempDir())
	report, err := RunAll(context.Background(), Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "rehearsal.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadReport(path)
	if err != nil {
		t.Fatalf("loadReport: %v", err)
	}
	if err := validatePhaseA(loaded); err != nil {
		t.Fatalf("validatePhaseA: %v", err)
	}
}

// TestEachScenarioRunsIsolated runs every scenario against its own fresh
// ledger, which is what RunAll does; this test additionally pins that a
// scenario's expectations are deterministic (running twice yields the same
// pass set).
func TestEachScenarioRunsIsolated(t *testing.T) {
	t.Setenv("BICZ_RUNTIME_DIR", t.TempDir())
	ctx := context.Background()
	first, err := RunAll(ctx, Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("RunAll 1: %v", err)
	}
	second, err := RunAll(ctx, Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("RunAll 2: %v", err)
	}
	if len(first.Results) != len(second.Results) {
		t.Fatal("runs must produce the same scenario count")
	}
	for i := range first.Results {
		if first.Results[i].Pass != second.Results[i].Pass {
			t.Errorf("scenario %s: run1 pass=%t run2 pass=%t — non-deterministic",
				first.Results[i].Scenario, first.Results[i].Pass, second.Results[i].Pass)
		}
	}
}

// TestMinimumScenarioSet guards the contract that the canonical set is present
// in the report (the gate depends on it).
func TestMinimumScenarioSet(t *testing.T) {
	got := make(map[string]bool)
	for _, scenario := range Scenarios() {
		got[scenario.ID] = true
	}
	for _, id := range ScenarioIDs {
		if !got[id] {
			t.Errorf("scenario %q missing from Scenarios()", id)
		}
	}
	if len(ScenarioIDs) < 10 {
		t.Fatalf("only %d scenarios; the milestone requires at least the documented ten", len(ScenarioIDs))
	}
}
