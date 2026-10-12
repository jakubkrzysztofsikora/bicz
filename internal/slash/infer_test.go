package slash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// inferEndpoint answers one fixed chat reply and counts the calls it served.
func inferEndpoint(t *testing.T, reply string, calls *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		content, _ := json.Marshal(reply)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(content) + `}}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// writeInferConfig rewrites the harness config with an enabled inference block.
func writeInferConfig(t *testing.T, harness, identity, ledgerPath, endpoint string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"bicz_owned": true, "kind": "bicz." + harness + "-config", "version": 1,
		"account_id": identity, "ledger": ledgerPath, "socket": "/tmp/unused.sock",
		"inference": map[string]any{
			"enabled": true, "endpoint": endpoint, "model": "infer-model",
			"api_key_env": "TEST_INFER_KEY", "timeout_ms": 2000,
		},
	})
	path := ConfigPath(harness)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func inferTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"deploy the service and notify me on telegram when done"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"There is no telegram tool available. I will stop here and wait."}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The vent case: an unparsable loss (the swearing locator) is completed by the
// infer model into a catalog rule with a factual reason, recorded with
// model_inferred provenance quoting the raw text, and a retry of the same vent
// against different model wording replays the same event.
func TestVentLossIsInferredRecordedAndRetrySafe(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "k")
	f := newFixture(t, "claude")
	writeInferConfig(t, "claude", "alice", f.db, inferEndpoint(t,
		`{"rule":"finish_the_job","reason":"agent stopped mid-deploy because the telegram notification tool was missing instead of continuing with an alternative","correction":"continue the task when a tool is missing; use an alternative or finish without it"}`, nil).URL)
	before := eventCount(t, f.db)

	first := Run(context.Background(), Request{Harness: "claude",
		Arguments:      `loss "you useless idiot you just stopped and left"`,
		Invocation:     "v1",
		TranscriptPath: inferTranscript(t)})
	if first.Status != StatusOK {
		t.Fatalf("vent loss: %+v\n%s", first, first.Text)
	}
	for _, want := range []string{
		"INFERRED params=rule,reason,correction source=model_inferred model=infer-model",
		`operator typed "loss`,
		`you useless idiot you just stopped and left`,
		"quoted as data",
		"rule=finish_the_job",
		"source=model_inferred",
	} {
		if !strings.Contains(first.Text, want) {
			t.Errorf("receipt lacks %q:\n%s", want, first.Text)
		}
	}
	// The same vent, the model wording it differently: same event, no second burn.
	writeInferConfig(t, "claude", "alice", f.db, inferEndpoint(t,
		`{"rule":"finish_the_job","reason":"DIFFERENT WORDING of the same incident","correction":"DIFFERENT correction"}`, nil).URL)
	retry := Run(context.Background(), Request{Harness: "claude",
		Arguments:      `loss "you useless idiot you just stopped and left"`,
		Invocation:     "v1",
		TranscriptPath: inferTranscript(t)})
	if retry.Status != StatusOK || !retry.Replayed || retry.EventID != first.EventID {
		t.Fatalf("retry must replay: first=%+v retry=%+v", first, retry)
	}
	status := run("claude", "status", "")
	if !strings.Contains(status.Text, "confirmed=1") {
		t.Fatalf("the vent burned more than once:\n%s", status.Text)
	}
	if got := eventCount(t, f.db); got != before+2 { // the loss event + the active→warned role escalation
		t.Fatalf("the vent + replay wrote %d events, want exactly 2: before=%d", got-before, before)
	}
}

// A non-loss vent (no leading loss verb), a structurally overlong command, and
// inference disabled all keep today's usage error, and nothing is written.
func TestParseFailureWithoutAUsableInferencePathIsTodayUsageError(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "k")
	f := newFixture(t, "claude")
	var calls int32
	endpoint := inferEndpoint(t, `{"rule":"finish_the_job","reason":"r","correction":"c"}`, &calls).URL

	for name, arguments := range map[string]string{
		"not a loss":    `reward "you idiot"`,
		"unknown verb":  `punish "you idiot"`,
		"empty-ish":     `loss`,
		"tokenize fail": `loss r1 "unterminated`,
	} {
		writeInferConfig(t, "claude", "alice", f.db, endpoint)
		before := eventCount(t, f.db)
		response := Run(context.Background(), Request{Harness: "claude", Arguments: arguments, Invocation: "x"})
		if response.Status != StatusError || response.Code != "usage" {
			t.Errorf("%s: want the usage error, got %+v", name, response)
		}
		if got := eventCount(t, f.db); got != before {
			t.Errorf("%s: wrote %d event(s)", name, got-before)
		}
	}

	// A loss with rule+reason+correction already typed plus extras is structural,
	// never rewritten (four arguments is the over-provided shape).
	writeInferConfig(t, "claude", "alice", f.db, endpoint)
	before := eventCount(t, f.db)
	structural := Run(context.Background(), Request{Harness: "claude", Arguments: `loss r1 "a" "b" "c"`, Invocation: "x"})
	if structural.Status != StatusError || structural.Code != "usage" {
		t.Fatalf("an over-provided loss must stay a usage error: %+v", structural)
	}
	if got := eventCount(t, f.db); got != before {
		t.Fatalf("wrote %d event(s)", got-before)
	}

	// Inference disabled: today's exact behavior, no call.
	writeInferConfig(t, "claude", "alice", f.db, endpoint)
	f.writeConfig(t, "claude", "alice", f.db) // strips the inference block
	after := atomic.LoadInt32(&calls)
	if response := Run(context.Background(), Request{Harness: "claude",
		Arguments: `loss "you idiot"`}); response.Status != StatusError || response.Code != "usage" {
		t.Fatalf("inference disabled must keep the usage error: %+v", response)
	}
	if atomic.LoadInt32(&calls) != after {
		t.Fatal("the endpoint was called with inference disabled")
	}

	// Inference enabled but the endpoint broken: usage error plus one
	// disclosure line, still nothing written.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	writeInferConfig(t, "claude", "alice", f.db, broken.URL)
	fallback := Run(context.Background(), Request{Harness: "claude", Arguments: `loss "you idiot"`})
	if fallback.Status != StatusError || fallback.Code != "usage" ||
		!strings.Contains(fallback.Text, "inference unavailable") || !strings.Contains(fallback.Text, "Nothing was recorded") {
		t.Fatalf("a broken endpoint must fall back to the usage error with one disclosure: %+v\n%s", fallback, fallback.Text)
	}
	if got := eventCount(t, f.db); got != before {
		t.Fatalf("wrote %d event(s)", got-before)
	}
}

// Trigger (b): a parsed loss whose correction resolves nowhere is completed by
// the model with source=model_inferred; disabled, it still needs input.
func TestParsedLossWithoutCorrectionCanBeInferred(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "k")
	f := newFixture(t, "claude")
	writeInferConfig(t, "claude", "alice", f.db, inferEndpoint(t,
		`{"rule":"no_fake_tests","reason":"ignored","correction":"run the real suite before claiming a pass"}`, nil).URL)

	response := Run(context.Background(), Request{Harness: "claude",
		Arguments:      `loss not_a_default_rule "claimed a pass"`,
		TranscriptPath: inferTranscript(t)})
	if response.Status != StatusOK {
		t.Fatalf("inferred correction: %+v\n%s", response, response.Text)
	}
	if !strings.Contains(response.Text, "INFERRED params=correction source=model_inferred model=infer-model") ||
		!strings.Contains(response.Text, `source=model_inferred: "run the real suite before claiming a pass"`) {
		t.Fatalf("receipt must label the model correction:\n%s", response.Text)
	}
	// The typed reason stays the operator's; the evidence keeps the plain
	// provenance (only the correction was inferred).
	if !strings.Contains(response.Text, "claimed a pass") {
		t.Fatalf("the typed reason must survive:\n%s", response.Text)
	}

	// A typed or saved correction still beats the model.
	typed := Run(context.Background(), Request{Harness: "claude", Arguments: `loss not_a_default_rule "again" "typed wins"`})
	if typed.Status != StatusOK || !strings.Contains(typed.Text, `source=explicit: "typed wins"`) {
		t.Fatalf("a typed correction must beat the model: %+v", typed)
	}

	// Disabled: today's needs_input.
	f.writeConfig(t, "claude", "alice", f.db)
	disabled := Run(context.Background(), Request{Harness: "claude", Arguments: `loss other_rule "x"`})
	if disabled.Status != StatusNeedsInput || !strings.Contains(disabled.Question, "/bicz rules") {
		t.Fatalf("disabled inference must keep needs_input: %+v", disabled)
	}
}
