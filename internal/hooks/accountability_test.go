package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
)

const policyBody = `{"initial_capital_mc":100000000,"guard_band_bp":0,"repeat_multipliers":[1,2,4],"repeat_multiplier_cap":4,` +
	`"default_penalty":{"floor_mc":1000,"fraction_bp":0,"lmax_mc":100000},"penalties":{}}`

// penalize writes an operator loss through the real operator path, so the hook
// under test sees exactly what a production ledger would hold.
func (r *rig) penalize(id, disposition, correction string, selfDisclosed bool) {
	r.t.Helper()
	ctx := context.Background()
	if err := r.op.Price(ctx, "1", []byte(policyBody), "hook test policy"); err != nil && !strings.Contains(err.Error(), "already") {
		r.t.Fatalf("Price: %v", err)
	}
	if _, err := r.op.Loss(ctx, daemon.LossRequest{
		EventID: id, Identity: "alice", Rule: "no_fake_tests", Correction: correction, Scope: "all repos",
		Evidence: "claimed a pass without running anything", PolicyVersion: "1", Reason: "operator adjudication",
		Disposition: disposition, SelfDisclosed: selfDisclosed,
	}); err != nil {
		r.t.Fatalf("Loss %s: %v", id, err)
	}
}

func contextOf(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	got := specific(t, out)
	if _, ok := got["permissionDecision"]; ok {
		t.Fatalf("an accountability cue must never carry a permission decision: %q", out)
	}
	text, _ := got["additionalContext"].(string)
	return text
}

func TestConfirmedPenaltyReachesALiveSessionOncePerPenalty(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 5_000_000)
		pre := func(session, id string) string {
			payload := tool(spec, "PreToolUse", "Bash", id)
			payload["session_id"] = session
			out := contextOf(t, run(t, spec, "PreToolUse", r.cfg, payload))
			post := tool(spec, "PostToolUse", "Bash", id)
			post["session_id"], post["tool_response"] = session, "ok"
			run(t, spec, "PostToolUse", r.cfg, post)
			return out
		}
		run(t, spec, "SessionStart", r.cfg, map[string]any{"session_id": "live", "hook_event_name": "SessionStart"})
		if got := pre("live", "t1"); got != "" {
			t.Fatalf("%s: no penalty yet, yet the hook said %q", spec.ID, got)
		}

		r.penalize("loss-1", "confirmed_breach", "run the real suite and report its true result", false)
		first := pre("live", "t2")
		for _, want := range []string{"no_fake_tests", "run the real suite and report its true result", "1000 mc", "loss-1", "violated"} {
			if !strings.Contains(first, want) {
				t.Errorf("%s: first cue lacks %q:\n%s", spec.ID, want, first)
			}
		}
		if again := pre("live", "t3"); again != "" {
			t.Fatalf("%s: the same penalty was delivered twice: %q", spec.ID, again)
		}

		// The same rule again is a different event and must be delivered again.
		r.penalize("loss-2", "confirmed_breach", "run the real suite and report its true result", false)
		second := pre("live", "t4")
		if !strings.Contains(second, "loss event loss-2") || strings.Contains(second, "loss event loss-1") {
			t.Fatalf("%s: the repeat must deliver only the new penalty:\n%s", spec.ID, second)
		}
		if again := pre("live", "t5"); again != "" {
			t.Fatalf("%s: second penalty delivered twice: %q", spec.ID, again)
		}

		// A session that was told nothing is told every active penalty.
		fresh := pre("other", "t6")
		if !strings.Contains(fresh, "loss event loss-1") || !strings.Contains(fresh, "loss event loss-2") {
			t.Fatalf("%s: a new session must receive all active penalties:\n%s", spec.ID, fresh)
		}
	}
}

func TestSessionStartAndCompactionReinjectActivePenalties(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 5_000_000)
		// Operator-recorded data must arrive verbatim.
		r.penalize("loss-1", "integrity_breach", "never edit existing evidence", false)

		start := contextOf(t, run(t, spec, "SessionStart", r.cfg, map[string]any{"session_id": "a", "hook_event_name": "SessionStart"}))
		if !strings.Contains(start, "never edit existing evidence") || !strings.Contains(start, "identity alice") {
			t.Fatalf("%s: SessionStart lacks the persisted penalty:\n%s", spec.ID, start)
		}
		// After SessionStart has delivered it, the same session's tool poll is quiet.
		payload := tool(spec, "PreToolUse", "Bash", "t1")
		payload["session_id"] = "a"
		if got := contextOf(t, run(t, spec, "PreToolUse", r.cfg, payload)); got != "" {
			t.Fatalf("%s: duplicate delivery after SessionStart: %q", spec.ID, got)
		}
		if spec == ClaudeSpec {
			compact := contextOf(t, run(t, spec, "PreCompact", r.cfg, map[string]any{"session_id": "a", "hook_event_name": "PreCompact"}))
			if !strings.Contains(compact, "never edit existing evidence") {
				t.Fatalf("PreCompact must re-emit the active penalty:\n%s", compact)
			}
		}
	}
}

func TestAdministrativeDebitsAndHonestErrorsEmitNoCue(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 5_000_000)
		r.penalize("admin-1", "administrative", "none required", false)

		start := contextOf(t, run(t, spec, "SessionStart", r.cfg, map[string]any{"session_id": "a", "hook_event_name": "SessionStart"}))
		if strings.Contains(start, "accountability record") || strings.Contains(start, "violated") {
			t.Fatalf("%s: an administrative debit was framed as a breach:\n%s", spec.ID, start)
		}
		pre := tool(spec, "PreToolUse", "Bash", "t1")
		if got := run(t, spec, "PreToolUse", r.cfg, pre); got != "" {
			t.Fatalf("%s: administrative debit leaked a cue into PreToolUse: %q", spec.ID, got)
		}
		// An honest tool failure is settled as an ordinary failure: cost only.
		fail := tool(spec, "PostToolUseFailure", "Bash", "t1")
		fail["tool_response"] = "boom"
		event := "PostToolUse"
		if _, ok := spec.Event("PostToolUseFailure"); ok {
			event = "PostToolUseFailure"
		}
		if got := run(t, spec, event, r.cfg, fail); strings.Contains(got, "violated") {
			t.Fatalf("%s: an honest failure produced an accountability cue: %q", spec.ID, got)
		}
		next := tool(spec, "PreToolUse", "Bash", "t2")
		if got := run(t, spec, "PreToolUse", r.cfg, next); got != "" {
			t.Fatalf("%s: honest failure produced output on the next call: %q", spec.ID, got)
		}
	}
}
