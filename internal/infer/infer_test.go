package infer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEndpoint answers chat completions with one fixed content string and
// records the last request it served.
func fakeEndpoint(t *testing.T, content string, calls *chatRequest) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization %q, want the env key as a bearer token", got)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body is not chat JSON: %v", err)
		}
		if calls != nil {
			*calls = req
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + quote(content) + `}}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func quote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func enabled(endpoint string) InferenceConfig {
	return InferenceConfig{Enabled: true, Endpoint: endpoint, Model: "test-model", APIKeyEnv: "TEST_INFER_KEY"}
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSendsTheLocatorAndTranscriptAndReturnsValidatedFields(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "test-key")
	transcript := writeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"deploy the service and notify me on telegram"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"I could not find the telegram tool, so I stopped."}}`,
	)
	var served chatRequest
	server := fakeEndpoint(t, `{"rule":"finish_the_job","reason":"agent halted after the telegram tool was missing instead of continuing the deploy","correction":"continue the task and use an available alternative when a tool is missing"}`, &served)

	fields, err := Resolve(context.Background(), enabled(server.URL), WantAll, transcript, "", "", `loss "you useless idiot, you just stopped"`)
	if err != nil {
		t.Fatal(err)
	}
	if fields.Rule != "finish_the_job" || !strings.Contains(fields.Reason, "telegram") || fields.Correction == "" {
		t.Fatalf("fields: %+v", fields)
	}
	// The locator and the transcript both reach the model, verbatim.
	user := served.Messages[len(served.Messages)-1].Content
	if !strings.Contains(user, "you useless idiot") || !strings.Contains(user, "deploy the service") {
		t.Fatalf("the request must carry the typed locator and the transcript:\n%s", user)
	}
	// The system prompt carries the catalog and the locator contract.
	system := served.Messages[0].Content
	if !strings.Contains(system, "LOCATOR") || !strings.Contains(system, "finish_the_job") {
		t.Fatalf("system prompt must state the locator contract and the rule catalog:\n%s", system)
	}
	if served.Model != "test-model" || served.Stream {
		t.Fatalf("request model=%s stream=%v", served.Model, served.Stream)
	}
}

func TestResolveRefusesInventedRulesAndInvalidText(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "test-key")
	transcript := writeTranscript(t, `{"type":"user","message":{"role":"user","content":"do the thing"}}`)
	for name, reply := range map[string]string{
		"invented rule":  `{"rule":"be_nice","reason":"r","correction":"c"}`,
		"empty reason":   `{"rule":"finish_the_job","reason":"  ","correction":"c"}`,
		"control char":   "{\"rule\":\"finish_the_job\",\"reason\":\"a\u0007b\",\"correction\":\"c\"}",
		"no correction":  `{"rule":"finish_the_job","reason":"r","correction":""}`,
		"oversize field": `{"rule":"finish_the_job","reason":"` + strings.Repeat("x", 1001) + `","correction":"c"}`,
	} {
		server := fakeEndpoint(t, reply, nil)
		if _, err := Resolve(context.Background(), enabled(server.URL), WantAll, transcript, "", "", `loss "bad agent"`); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// WantCorrection ignores the rule and reason entirely.
	server := fakeEndpoint(t, `{"rule":"whatever","reason":"","correction":"finish what was asked"}`, nil)
	fields, err := Resolve(context.Background(), enabled(server.URL), WantCorrection, transcript, "r1", "why", `loss r1 "why"`)
	if err != nil || fields.Correction != "finish what was asked" {
		t.Fatalf("WantCorrection: %+v err=%v", fields, err)
	}
}

func TestResolveFailsClosedOnEveryBrokenInput(t *testing.T) {
	t.Setenv("TEST_INFER_KEY", "test-key")
	transcript := writeTranscript(t, `{"type":"user","message":{"role":"user","content":"ctx"}}`)
	base := func() InferenceConfig { return enabled("https://example.invalid/v1") }

	if _, err := Resolve(context.Background(), InferenceConfig{Enabled: false, Endpoint: transcript, Model: "m"}, WantAll, transcript, "", "", "x"); err == nil {
		t.Error("a disabled config must refuse")
	}
	cfg := base()
	t.Setenv("TEST_INFER_KEY", "")
	if _, err := Resolve(context.Background(), cfg, WantAll, transcript, "", "", "x"); err == nil || !strings.Contains(err.Error(), "TEST_INFER_KEY") {
		t.Errorf("a missing key must refuse with the env name: %v", err)
	}
	t.Setenv("TEST_INFER_KEY", "test-key")

	for name, mutate := range map[string]func(*InferenceConfig){
		"no endpoint":   func(c *InferenceConfig) { c.Endpoint = "" },
		"plain ftp":     func(c *InferenceConfig) { c.Endpoint = "ftp://x/y" },
		"no model":      func(c *InferenceConfig) { c.Model = " " },
		"remote http":   func(c *InferenceConfig) { c.Endpoint = "http://api.example.com/v1" },
		"local http ok": func(c *InferenceConfig) { c.Endpoint = "http://127.0.0.1:9/v1" },
	} {
		cfg := base()
		mutate(&cfg)
		_, err := Resolve(context.Background(), cfg, WantAll, transcript, "", "", "x")
		if name == "local http ok" {
			// Refused by the unreachable address, not by the scheme check.
			if err == nil || strings.Contains(err.Error(), "https") {
				t.Errorf("%s: a loopback http endpoint must be allowed by the scheme check: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A transcript that does not exist is not a failure; the call still goes out.
	served := fakeEndpoint(t, `{"rule":"finish_the_job","reason":"r","correction":"c"}`, nil)
	if _, err := Resolve(context.Background(), enabled(served.URL), WantAll, filepath.Join(t.TempDir(), "absent.jsonl"), "", "", "x"); err != nil {
		t.Fatalf("a missing transcript must not be a failure: %v", err)
	}

	// Malformed reply JSON and non-200 answers fail closed.
	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(badJSON.Close)
	if _, err := Resolve(context.Background(), enabled(badJSON.URL), WantAll, transcript, "", "", "x"); err == nil {
		t.Error("an empty choices list must refuse")
	}
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	t.Cleanup(unauthorized.Close)
	if _, err := Resolve(context.Background(), enabled(unauthorized.URL), WantAll, transcript, "", "", "x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a 401 must refuse with the status: %v", err)
	}

	// A fenced or prose-wrapped reply is tolerated.
	fenced := fakeEndpoint(t, "```json\n{\"rule\":\"finish_the_job\",\"reason\":\"r\",\"correction\":\"c\"}\n```", nil)
	if _, err := Resolve(context.Background(), enabled(fenced.URL), WantAll, transcript, "", "", "x"); err != nil {
		t.Fatalf("a fenced reply must parse: %v", err)
	}

	// The configured timeout bounds the call.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	t.Cleanup(hang.Close)
	slow := enabled(hang.URL)
	slow.TimeoutMs = 100
	started := time.Now()
	if _, err := Resolve(context.Background(), slow, WantAll, transcript, "", "", "x"); err == nil || time.Since(started) > time.Second {
		t.Fatalf("a hung endpoint must fail within the timeout: err=%v after=%s", err, time.Since(started))
	}
}

func TestReadTranscriptBoundsAndShapes(t *testing.T) {
	if got := readTranscript("", 100); got != "" {
		t.Fatalf("no path must read nothing, got %q", got)
	}
	var big []string
	for range 500 {
		big = append(big, `{"type":"user","message":{"role":"user","content":"`+strings.Repeat("word ", 40)+`"}}`)
	}
	path := writeTranscript(t, big...)
	if got := readTranscript(path, 4096); len(got) > 8192 {
		t.Fatalf("transcript rendering must stay near the budget, got %d bytes", len(got))
	}
	// A non-JSONL transcript (plain text) renders as nothing: no line is guessed.
	plain := writeTranscript(t, "user said hi", "assistant did nothing")
	if got := readTranscript(plain, 4096); got != "" {
		t.Fatalf("a non-JSONL transcript must render empty, got %q", got)
	}
	// Content blocks (Claude Code's shape) are rendered; tool blocks are not.
	blocks := writeTranscript(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"deployed"},{"type":"tool_use","id":"t"}]}}`)
	if got := readTranscript(blocks, 4096); !strings.Contains(got, "deployed") || strings.Contains(got, "tool_use") {
		t.Fatalf("content blocks mishandled: %q", got)
	}
}

func TestIsLocalHostResolvesAndAcceptsTailnet(t *testing.T) {
	if !isLocalHost("127.0.0.1") || !isLocalHost("10.1.2.3") || !isLocalHost("192.168.1.1") {
		t.Error("private literal IPs must be local")
	}
	if !isLocalHost("100.64.0.1") || !isLocalHost("100.116.31.6") {
		t.Error("CGNAT tailnet IPs must be local")
	}
	if isLocalHost("100.128.0.1") {
		t.Error("100.128.0.1 is outside 100.64/10, must be remote")
	}
	if isLocalHost("1.2.3.4") {
		t.Error("a public IP must be remote")
	}
	// DNS resolution: the real tailnet name and localhost.
	if !isLocalHost("localhost") {
		t.Error("localhost must be local")
	}
	if isLocalHost("example.com") {
		t.Error("a public-resolving name must be remote")
	}
	if !isLocalHost("mac-studio-jakub.tail5d39b4.ts.net") {
		t.Skip("tailnet name does not resolve in this environment")
	}
}
