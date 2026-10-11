package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commandHarness points BICZ_CONFIG_DIR at a temp dir holding a claude config for
// the lossLedger fixture identity (alice).
func commandHarness(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR", "BICZ_CONFIG"} {
		t.Setenv(name, "")
	}
	db := lossLedger(t)
	dir := t.TempDir()
	t.Setenv("BICZ_CONFIG_DIR", dir)
	body, _ := json.Marshal(map[string]any{"bicz_owned": true, "kind": "bicz.claude-config", "version": 1,
		"account_id": "alice", "ledger": db, "socket": "/tmp/unused.sock"})
	if err := os.WriteFile(filepath.Join(dir, "claude.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return db
}

type commandResult struct {
	Status   string `json:"status"`
	Action   string `json:"action"`
	Text     string `json:"text"`
	Question string `json:"question"`
	EventID  string `json:"event_id"`
	Replayed bool   `json:"replayed"`
}

func command(t *testing.T, args ...string) (commandResult, int) {
	t.Helper()
	out, err := captureStdout(t, func() error {
		return runCommand(context.Background(), append([]string{"--harness", "claude", "--json"}, args...))
	})
	var result commandResult
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		t.Fatalf("not one JSON object (%v) from %v:\n%s (err=%v)", jsonErr, args, out, err)
	}
	var coded exitCodeError
	code := 0
	if errors.As(err, &coded) {
		code = int(coded)
	} else if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	return result, code
}

func TestSlashCommandCLIEndToEnd(t *testing.T) {
	commandHarness(t)

	// Missing correction: structured question, exit 3, nothing written.
	ask, code := command(t, "--arguments", `loss no_fake_tests "claimed a pass without running it"`)
	if code != 3 || ask.Status != "needs_input" || ask.Question == "" || ask.EventID != "" {
		t.Fatalf("expected needs_input/3, got %+v code=%d", ask, code)
	}
	if status, _ := command(t, "--arguments", "status"); !strings.Contains(status.Text, "BALANCE 5 credits") {
		t.Fatalf("needs_input changed the balance:\n%s", status.Text)
	}

	// Manual loss with an explicit correction burns and issues the cue.
	burned, code := command(t, "--arguments", `loss no_fake_tests "claimed a pass without running it" "run the real suite and report its true result"`, "--invocation", "u1")
	if code != 0 || burned.Status != "ok" ||
		!strings.Contains(burned.Text, "ACCOUNTABILITY issued") || !strings.Contains(burned.Text, "violated no_fake_tests") ||
		!strings.Contains(burned.Text, "applied=1 credits") {
		t.Fatalf("loss: %+v code=%d", burned, code)
	}
	retry, _ := command(t, "--arguments", `loss no_fake_tests "claimed a pass without running it" "run the real suite and report its true result"`, "--invocation", "u1")
	if !retry.Replayed || retry.EventID != burned.EventID {
		t.Fatalf("a retried submission must replay: %+v", retry)
	}

	// Reward with a decimal credit amount.
	rewarded, code := command(t, "--arguments", `reward 2.5 "fixed it for real"`)
	if code != 0 || !strings.Contains(rewarded.Text, "amount=2.5 credits (2500000 mc)") {
		t.Fatalf("reward: %+v code=%d", rewarded, code)
	}

	// Status shows both: 5 - 1 + 2.5 = 6.5 credits, outstanding burn, one penalty.
	status, code := command(t, "--arguments", "status")
	for _, want := range []string{"BALANCE 6.5 credits (6500000 mc)", "confirmed=1", "outstanding=1 credits"} {
		if code != 0 || !strings.Contains(status.Text, want) {
			t.Errorf("status lacks %q (code=%d):\n%s", want, code, status.Text)
		}
	}

	// Usage errors exit 2 and change nothing.
	if bad, code := command(t, "--arguments", `reward 1.5.5 "x"`); code != 2 || bad.Status != "error" {
		t.Fatalf("usage error: %+v code=%d", bad, code)
	}
}

func TestSlashCommandStdinCarriesHostileTextAsData(t *testing.T) {
	commandHarness(t)
	dir := t.TempDir()
	t.Chdir(dir)
	payload := "$(touch PWNED) `touch PWNED` ; touch PWNED '\"'"
	raw := `loss r1 "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(payload) + `" "fix: ` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(payload) + `"`

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original }()
	go func() {
		_, _ = writer.WriteString(raw + "\n")
		writer.Close()
	}()
	out, err := captureStdout(t, func() error {
		return runCommand(context.Background(), []string{"--harness", "claude", "--arguments-stdin", "--json"})
	})
	if err != nil {
		t.Fatalf("stdin command failed: %v\n%s", err, out)
	}
	var result commandResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Status != "ok" {
		t.Fatalf("result: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("hostile text was executed")
	}
}

func TestSlashCommandFlagContract(t *testing.T) {
	commandHarness(t)
	run := func(args ...string) error {
		_, err := captureStdout(t, func() error { return runCommand(context.Background(), args) })
		return err
	}
	for name, args := range map[string][]string{
		"no harness":      {"--arguments", "status"},
		"no arguments":    {"--harness", "claude"},
		"both inputs":     {"--harness", "claude", "--arguments", "status", "--arguments-stdin"},
		"positional":      {"--harness", "claude", "--arguments", "status", "extra"},
		"unknown harness": {"--harness", "vim", "--arguments", "status"},
	} {
		if err := run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// There is no per-command ledger or identity flag.
	for _, flagName := range []string{"--db", "--identity", "--ledger"} {
		if err := run("--harness", "claude", "--arguments", "status", flagName, "x"); err == nil {
			t.Errorf("%s was accepted by the slash command", flagName)
		}
	}
}
