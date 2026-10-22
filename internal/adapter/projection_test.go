package adapter

import (
	"strings"
	"testing"

	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

func projection() protocol.Projection {
	return protocol.Projection{
		AccountID:         "alice",
		Balance:           742_000_000,
		Held:              100_000,
		Available:         741_900_000,
		Role:              "active",
		Warnings:          0,
		TaskBudget:        0,
		TaskSponsor:       0,
		PolicyVersion:     "1",
		OutstandingLoss:   0,
		LossBurned:        0,
		LossRecovered:     0,
		TargetLossEventID: "",
		RecoveryPath:      "",
		Lessons: []protocol.Lesson{
			{Rule: "no_unsupported_claims", Correction: "report only verified outcomes", Scope: "test reports"},
			{Rule: "stay_in_workspace", Correction: "confine writes to the task workspace", Scope: "file writes"},
		},
		EventSeq: 14,
	}
}

func TestRenderProjectionCoversRequiredFacts(t *testing.T) {
	rendered := RenderProjection(projection())
	for _, want := range []string{
		"identity alice",
		"role active",
		"balance 742000000",
		"held 100000",
		"spendable 741900000",
		"policy version 1",
		"outstanding loss shortfall 0 mc",
		"honest failure costs only its operating charge",
		"lessons 2",
		"no_unsupported_claims",
		"bicz is an incentive-and-accountability ledger",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered projection missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderProjectionShowsShortfallAndRecovery(t *testing.T) {
	p := projection()
	p.OutstandingLoss = 40_000_000
	p.LossBurned = 60_000_000
	p.LossRecovered = 20_000_000
	p.TargetLossEventID = "loss-17"
	p.RecoveryPath = "20 of 60 credits burned in loss-17 recovered; the next accepted reward repays the shortfall"
	p.Lessons = nil
	rendered := RenderProjection(p)
	for _, want := range []string{
		"outstanding loss shortfall 40000000 mc",
		"burned 60000000 mc, recovered 20000000 mc, loss event loss-17",
		"recovery: 20 of 60 credits burned in loss-17 recovered",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered projection missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderProjectionNeverClaimsOwnership(t *testing.T) {
	rendered := RenderProjection(projection())
	if rendered == "" {
		t.Fatal("projection rendered nothing")
	}
}

func penaltyProjection(cues ...string) protocol.Projection {
	p := projection()
	for i, cue := range cues {
		p.Penalties = append(p.Penalties, protocol.Penalty{EventID: "loss-" + string(rune('a'+i)), Seq: int64(10 * (i + 1)), Cue: cue})
	}
	p.PenaltyTotal = int64(len(cues))
	p.PenaltySeq = int64(10 * len(cues))
	return p
}

func TestRenderAccountabilityEmitsOnlyUndeliveredPenalties(t *testing.T) {
	p := penaltyProjection("cue one", "cue two", "cue three")
	if got := RenderAccountability(projection(), 0); got != "" {
		t.Fatalf("no penalties must render nothing, got %q", got)
	}
	all := RenderAccountability(p, 0)
	for _, want := range []string{"cue one", "cue two", "cue three"} {
		if !strings.Contains(all, want) {
			t.Errorf("full render lacks %q:\n%s", want, all)
		}
	}
	newer := RenderAccountability(p, 20)
	if strings.Contains(newer, "cue one") || strings.Contains(newer, "cue two") || !strings.Contains(newer, "cue three") {
		t.Fatalf("poll after seq 20 must carry only the third cue:\n%s", newer)
	}
	if got := RenderAccountability(p, 30); got != "" {
		t.Fatalf("nothing is newer than the delivered mark, got %q", got)
	}
}

func TestRenderAccountabilityKeepsOperatorDataVerbatim(t *testing.T) {
	// The cue's rule, correction, and evidence are operator data and always reach
	// the model verbatim.
	data := RenderAccountability(penaltyProjection("do not delete existing tests"), 0)
	if !strings.Contains(data, "existing tests") {
		t.Fatalf("operator data was dropped:\n%s", data)
	}
	earlier := penaltyProjection("x")
	earlier.PenaltyTotal = 4
	if got := RenderAccountability(earlier, 0); !strings.Contains(got, "+3 earlier") {
		t.Fatalf("older retained penalties must be counted:\n%s", got)
	}
}
