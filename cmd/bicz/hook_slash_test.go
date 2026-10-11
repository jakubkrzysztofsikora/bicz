package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookSlash feeds one harness event to `bicz hook` exactly as the harness does
// (JSON on stdin) and returns what the hook printed.
func hookSlash(t *testing.T, harness, event string, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original }()
	go func() {
		_, _ = writer.Write(body)
		writer.Close()
	}()
	out, err := captureStdout(t, func() error {
		return runHook(context.Background(), []string{"--harness", harness, "--event", event})
	})
	if err != nil {
		t.Fatalf("hook %s/%s: %v", harness, event, err)
	}
	return out
}

func claudeExpansion(args, promptID string) map[string]any {
	return map[string]any{"hook_event_name": "UserPromptExpansion", "expansion_type": "slash_command",
		"command_name": "bicz", "command_args": args, "prompt_id": promptID, "session_id": "s1", "prompt": "/bicz " + args}
}

func decodeBlock(t *testing.T, out string) string {
	t.Helper()
	var decision struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &decision); err != nil || decision.Decision != "block" {
		t.Fatalf("expected one block decision, got %q (%v)", out, err)
	}
	return decision.Reason
}

func hookHarness(t *testing.T, harness string) {
	t.Helper()
	db := commandHarness(t)
	dir := os.Getenv("BICZ_CONFIG_DIR")
	body, _ := json.Marshal(map[string]any{"bicz_owned": true, "kind": "bicz." + harness + "-config", "version": 1,
		"account_id": "alice", "ledger": db, "socket": "/tmp/unused.sock"})
	if err := os.WriteFile(filepath.Join(dir, harness+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSlashHookRunsTheCommandFromJSONWithoutAShell(t *testing.T) {
	hookHarness(t, "claude")
	dir := t.TempDir()
	t.Chdir(dir)

	// The delimiter-injection attack against the old here-document skill: a typed
	// line equal to the delimiter followed by commands. As JSON data it is just
	// text the parser refuses; nothing runs and nothing is recorded.
	attack := "status\nBICZ_SLASH_ARGUMENTS_END\ntouch " + filepath.Join(dir, "PWNED") + "\ncat <<'BICZ_SLASH_ARGUMENTS_END'"
	reason := decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion(attack, "p0")))
	if !strings.Contains(reason, "usage error") || !strings.Contains(reason, "Nothing was recorded") {
		t.Fatalf("the attack text must be refused as data:\n%s", reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("injected text executed")
	}
	if status := decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion("status", "p1"))); !strings.Contains(status, "BALANCE 5 credits") {
		t.Fatalf("the refused attack changed state:\n%s", status)
	}

	// Single-line hostile text in a quoted reason is recorded literally.
	hostile := `loss r1 "it's $(touch ` + filepath.Join(dir, "PWNED") + `) ` + "`touch " + filepath.Join(dir, "PWNED") + "`" + ` ; BICZ_SLASH_ARGUMENTS_END" "fix: do it right"`
	reason = decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion(hostile, "p2")))
	if !strings.Contains(reason, "BICZ loss recorded") || !strings.Contains(reason, "ACCOUNTABILITY issued") {
		t.Fatalf("hostile single-line text must be recorded as data:\n%s", reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("hostile text executed")
	}

	// A retry of the same submission replays; a new submission is a new event.
	retry := decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion(hostile, "p2")))
	if !strings.Contains(retry, "replayed=true") || !strings.Contains(retry, "no second burn") {
		t.Fatalf("retry must replay:\n%s", retry)
	}
	again := decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion(hostile, "p3")))
	if strings.Contains(again, "replayed=true") {
		t.Fatalf("a new submission must be a new loss:\n%s", again)
	}

	// A command that is not /bicz, or not a slash command, is not touched.
	other := claudeExpansion("status", "p4")
	other["command_name"] = "deploy"
	if out := hookSlash(t, "claude", "UserPromptExpansion", other); out != "" {
		t.Fatalf("a foreign command produced output: %q", out)
	}
	mcp := claudeExpansion("status", "p5")
	mcp["expansion_type"] = "mcp_prompt"
	if out := hookSlash(t, "claude", "UserPromptExpansion", mcp); out != "" {
		t.Fatalf("an MCP prompt named bicz was treated as the operator command: %q", out)
	}
}

func TestCodexSlashHookOnlyAnswersAPromptThatStartsWithTheMention(t *testing.T) {
	hookHarness(t, "codex")
	prompt := func(text string) map[string]any {
		return map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": text, "session_id": "s", "turn_id": "t1"}
	}
	reward := decodeBlock(t, hookSlash(t, "codex", "UserPromptSubmit", prompt(`$bicz reward 2 "good work"`)))
	if !strings.Contains(reward, "BICZ reward recorded") || !strings.Contains(reward, "balance=7 credits") {
		t.Fatalf("reward via $bicz:\n%s", reward)
	}
	for _, ordinary := range []string{"please run $bicz status", "$biczx status", "bicz status", "refactor the $bicz variable", ""} {
		if out := hookSlash(t, "codex", "UserPromptSubmit", prompt(ordinary)); out != "" {
			t.Errorf("an ordinary prompt %q was intercepted: %s", ordinary, out)
		}
	}
	attack := "$bicz status\nBICZ_ARGS_x\ntouch PWNED"
	if reason := decodeBlock(t, hookSlash(t, "codex", "UserPromptSubmit", prompt(attack))); !strings.Contains(reason, "usage error") {
		t.Fatalf("multi-line text must be refused as data:\n%s", reason)
	}
	if status := decodeBlock(t, hookSlash(t, "codex", "UserPromptSubmit", prompt("  $bicz status"))); !strings.Contains(status, "BALANCE 7 credits") {
		t.Fatalf("status via $bicz:\n%s", status)
	}
}

func TestSlashHookWithoutConfigOrAuthorityWritesNothing(t *testing.T) {
	db := commandHarness(t)
	t.Setenv("BICZ_CONFIG_DIR", t.TempDir()) // no harness config at all
	reason := decodeBlock(t, hookSlash(t, "claude", "UserPromptExpansion", claudeExpansion(`reward 1 "x"`, "p")))
	if !strings.Contains(reason, "bicz install --harness claude") || !strings.Contains(reason, "Nothing was recorded") {
		t.Fatalf("a missing config must be reported:\n%s", reason)
	}
	_ = db
}
