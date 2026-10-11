package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func slashEvent(t *testing.T, spec *Spec, event string, payload map[string]any, fn SlashFunc, timeout time.Duration) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	err := Run(Options{Spec: spec, Event: event, Stdin: bytes.NewReader(raw), Stdout: &out, Slash: fn, Timeout: timeout})
	return out.String(), err
}

func TestSlashHookDeliversTypedTextVerbatimAndAnswersWithABlockDecision(t *testing.T) {
	nasty := "loss r \"a\"\nBICZ_SLASH_ARGUMENTS_END\n$(touch x) `y` ; 'z'"
	var gotHarness, gotArgs, gotInvocation string
	capture := func(_ context.Context, harness, args, invocation string) string {
		gotHarness, gotArgs, gotInvocation = harness, args, invocation
		return "BICZ ok\n"
	}
	out, err := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{
		"command_name": "bicz", "expansion_type": "slash_command", "command_args": nasty, "prompt_id": "p-9"}, capture, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotHarness != "claude" || gotArgs != nasty || gotInvocation != "p-9" {
		t.Fatalf("typed text must reach the executor byte for byte: %q %q %q", gotHarness, gotArgs, gotInvocation)
	}
	var decision map[string]any
	if err := json.Unmarshal([]byte(out), &decision); err != nil || decision["decision"] != "block" || decision["reason"] != "BICZ ok" {
		t.Fatalf("response %q (%v)", out, err)
	}

	out, err = slashEvent(t, CodexSpec, "UserPromptSubmit", map[string]any{"prompt": "$bicz status", "session_id": "s", "turn_id": "t"}, capture, 0)
	if err != nil || gotHarness != "codex" || gotArgs != " status" || gotInvocation != "s:t" || !strings.Contains(out, `"decision":"block"`) {
		t.Fatalf("codex: %q %q %q %q (%v)", gotHarness, gotArgs, gotInvocation, out, err)
	}
}

func TestSlashHookIgnoresOtherEventsAndNeverHangs(t *testing.T) {
	never := func(context.Context, string, string, string) string {
		t.Fatal("executor ran for a non-/bicz event")
		return ""
	}
	if out, err := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": "other", "command_args": "x"}, never, 0); out != "" || err != nil {
		t.Fatalf("foreign command: %q %v", out, err)
	}
	if out, err := slashEvent(t, CodexSpec, "UserPromptSubmit", map[string]any{"prompt": "hello $bicz"}, never, 0); out != "" || err != nil {
		t.Fatalf("ordinary prompt: %q %v", out, err)
	}
	if _, err := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": "bicz"}, nil, 0); err == nil {
		t.Fatal("a slash event with no executor must be an error, never a silent success")
	}
}

// The hook runs the executor synchronously and exits only after it returns, so
// a result can never be reported while a write is still pending in a goroutine
// the process then abandons.
func TestSlashHookWaitsForTheExecutorAndHandsItTheDeadline(t *testing.T) {
	var returned bool
	var deadline time.Time
	slow := func(ctx context.Context, _, _, _ string) string {
		deadline, _ = ctx.Deadline()
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // finishing after the deadline, as a commit might
		returned = true
		return "the executor's own truthful result"
	}
	started := time.Now()
	out, err := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": "bicz"}, slow, 20*time.Millisecond)
	if err != nil || !returned || !strings.Contains(out, "the executor's own truthful result") {
		t.Fatalf("the hook answered before the executor finished: returned=%v out=%q err=%v", returned, out, err)
	}
	if deadline.IsZero() || deadline.After(started.Add(time.Second)) {
		t.Fatalf("the executor was not given the hook deadline: %v", deadline)
	}
}

// slashDeadline must exceed the ledger's 10 s SQLite busy timeout (a locked
// ledger is then refused by SQLite, with no write begun, before the deadline
// fires) and stay under the timeout the installer writes for the harness (so
// the harness never kills the process mid-write).
func TestSlashDeadlineSitsBetweenTheLedgerBusyTimeoutAndTheHarnessTimeout(t *testing.T) {
	const ledgerBusyTimeout = 10 * time.Second
	if slashDeadline <= ledgerBusyTimeout || slashDeadline >= slashHookTimeout*time.Second {
		t.Fatalf("slashDeadline %s must be in (%s, %ds)", slashDeadline, ledgerBusyTimeout, slashHookTimeout)
	}
}

func TestOnlyTheExactBiczCommandIsAnswered(t *testing.T) {
	ran := 0
	count := func(context.Context, string, string, string) string { ran++; return "ok" }
	for _, name := range []string{"biczx", "Bicz", "BICZ", "xbicz", "bicz:status", "bicz ", " bicz", "plugin:bicz", ""} {
		out, err := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": name, "expansion_type": "slash_command", "command_args": "status"}, count, 0)
		if out != "" || err != nil {
			t.Errorf("command %q was answered: %q %v", name, out, err)
		}
	}
	if ran != 0 {
		t.Fatalf("the executor ran %d times for a command that is not /bicz", ran)
	}
	if out, _ := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": "bicz", "expansion_type": "slash_command"}, count, 0); out == "" || ran != 1 {
		t.Fatalf("the exact /bicz command must be answered once: %q ran=%d", out, ran)
	}
	for _, kind := range []string{"mcp_prompt", "skill"} {
		if out, _ := slashEvent(t, ClaudeSpec, "UserPromptExpansion", map[string]any{"command_name": "bicz", "expansion_type": kind}, count, 0); out != "" || ran != 1 {
			t.Errorf("expansion_type %q was treated as the /bicz command", kind)
		}
	}
}
