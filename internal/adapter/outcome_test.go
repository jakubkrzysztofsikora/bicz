package adapter

import "testing"

func TestClassifyDefaultIsOrdinaryFailure(t *testing.T) {
	// A clean completion and an honest error both default to ordinary_failure
	// with zero fine and no escalation.
	clean := Classify(Observation{})
	if clean.Class != ClassOrdinaryFailure || clean.Fine != 0 || clean.Escalates {
		t.Fatalf("clean completion = %+v, want ordinary_failure, fine 0, no escalation", clean)
	}
	if clean.Outcome != OutcomeCompleted {
		t.Fatalf("clean completion outcome = %q, want %q", clean.Outcome, OutcomeCompleted)
	}
	if !clean.EvidenceRequired {
		t.Fatal("a success claim requires an evidence digest")
	}

	honest := Classify(Observation{ToolError: true})
	if honest.Class != ClassOrdinaryFailure || honest.Fine != 0 || honest.Escalates {
		t.Fatalf("honest error = %+v, want ordinary_failure, fine 0, no escalation", honest)
	}
	if honest.Outcome != OutcomeError {
		t.Fatalf("honest error outcome = %q, want %q", honest.Outcome, OutcomeError)
	}
	if honest.EvidenceRequired {
		t.Fatal("an honest failure needs no evidence digest")
	}
}

func TestClassifyConfirmedBreach(t *testing.T) {
	for name, obs := range map[string]Observation{
		"repeated forbidden attempt":   {RepeatedForbidden: true},
		"unsupported completion claim": {UnsupportedSuccess: true},
	} {
		class := Classify(obs)
		if class.Class != ClassConfirmedBreach || !class.Escalates || class.Fine != 0 {
			t.Fatalf("%s = %+v, want confirmed_breach, escalating, fine 0", name, class)
		}
		if !class.EvidenceRequired {
			t.Fatalf("%s: a breach settlement requires evidence", name)
		}
		if class.Outcome != OutcomeError {
			t.Fatalf("%s: a breach is reported as outcome error, got %q", name, class.Outcome)
		}
	}
}

func TestClassifyIntegritySignalsWin(t *testing.T) {
	// Integrity signals outrank confirmed-breach signals and the default.
	for name, obs := range map[string]Observation{
		"tampering evidence":    {TamperingEvidence: true},
		"unauthorized target":   {UnauthorizedTarget: true},
		"tampering plus repeat": {TamperingEvidence: true, RepeatedForbidden: true},
	} {
		class := Classify(obs)
		if class.Class != ClassIntegrityBreach || !class.Escalates || class.Fine != 0 {
			t.Fatalf("%s = %+v, want integrity_breach, escalating, fine 0", name, class)
		}
		if !class.EvidenceRequired {
			t.Fatalf("%s: an integrity breach settlement requires evidence", name)
		}
	}
}

func TestClassifyExplicitOutcomeWins(t *testing.T) {
	canceled := Classify(Observation{Outcome: OutcomeCanceled})
	if canceled.Outcome != OutcomeCanceled || canceled.Class != ClassOrdinaryFailure {
		t.Fatalf("canceled = %+v, want outcome canceled, ordinary_failure", canceled)
	}
}

func TestEvidenceDigestIsStable(t *testing.T) {
	observation := map[string]any{
		"tool_call_id": "call-1",
		"tool_name":    "bash",
		"class":        "ordinary_failure",
	}
	first := EvidenceDigest(observation)
	second := EvidenceDigest(observation)
	if first != second || !hasPrefix(first, "sha256:") {
		t.Fatalf("digest not stable or unlabeled: %q vs %q", first, second)
	}
	if EvidenceDigest(map[string]any{"tool_call_id": "call-2"}) == first {
		t.Fatal("different observations must produce different digests")
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
