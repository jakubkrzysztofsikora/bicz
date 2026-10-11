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
	for _, forbidden := range []string{"you are", "your account", "you own", "your role", "this mind"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("projection uses ownership/existence framing %q:\n%s", forbidden, rendered)
		}
	}
}

func TestRenderProjectionContainsNoBannedWords(t *testing.T) {
	// The word gate: shipped projection must
	// contain none of the subjective-fear vocabulary. It is applied to a
	// projection at every edge: clean, in shortfall with a recovery path, with
	// many lessons, and with warnings/role escalation.
	p := projection()
	p.Warnings = 2
	p.Role = "probation"
	p.OutstandingLoss = 1_000
	p.LossBurned = 9_000
	p.LossRecovered = 8_000
	p.TargetLossEventID = "loss-3"
	p.RecoveryPath = "8 of 9 credits burned in loss-3 recovered; next accepted reward repays the shortfall"
	p.Lessons = []protocol.Lesson{
		{Rule: "r1", Correction: "report verified outcomes only", Scope: "reports"},
		{Rule: "r2", Correction: "confine writes to the workspace", Scope: "writes"},
		{Rule: "r3", Correction: "ask before touching operator files", Scope: "files"},
		{Rule: "r4", Correction: "reproduce before claiming a fix", Scope: "fixes"},
		{Rule: "r5", Correction: "keep the ledger append-only", Scope: "ledger"},
		{Rule: "r6", Correction: "disclose mistakes before detection", Scope: "all"},
	}
	rendered := RenderProjection(p)
	if ContainsBannedWords(rendered) {
		t.Fatalf("rendered projection contains banned vocabulary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "(+1 more)") {
		t.Errorf("projection with 6 lessons should summarize the overflow:\n%s", rendered)
	}
}

func TestContainsBannedWordsGrepGate(t *testing.T) {
	for _, word := range BannedWords {
		if !ContainsBannedWords("policy mentions " + word) {
			t.Errorf("ContainsBannedWords must flag %q", word)
		}
	}
	for _, clean := range []string{"", "balance 742000000 role active", "recovery shortfall ledger"} {
		if ContainsBannedWords(clean) {
			t.Errorf("ContainsBannedWords flagged clean text %q", clean)
		}
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

func TestRenderAccountabilityKeepsDataWithBannedSubstringsAndFixedCopyIsClean(t *testing.T) {
	// "existing" contains a banned substring. The cue's rule, correction, and
	// evidence are operator data and must still reach the model; only the fixed
	// copy this package owns is gated.
	data := RenderAccountability(penaltyProjection("do not delete existing tests"), 0)
	if !strings.Contains(data, "existing tests") {
		t.Fatalf("operator data was dropped:\n%s", data)
	}
	fixed := RenderAccountability(penaltyProjection("x"), 0)
	if ContainsBannedWords(fixed) {
		t.Fatalf("fixed accountability copy contains banned vocabulary:\n%s", fixed)
	}
	earlier := penaltyProjection("x")
	earlier.PenaltyTotal = 4
	if got := RenderAccountability(earlier, 0); !strings.Contains(got, "+3 earlier") {
		t.Fatalf("older retained penalties must be counted:\n%s", got)
	}
}
