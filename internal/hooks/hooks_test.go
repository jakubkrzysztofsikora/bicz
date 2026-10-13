package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
)

// rig is a real supervisor on a temp socket over a temp ledger, plus the adapter
// config a hook would load. Nothing here touches a real harness or home dir.
type rig struct {
	t      *testing.T
	op     *daemon.Operator
	socket string
	cfg    *Config
}

// TestMain points hook call state at a throwaway directory, so no test can
// write under a real home even when it does not build a rig.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bicz-hook-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("BICZ_STATE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func newRig(t *testing.T, spec *Spec, credit int64) *rig {
	t.Helper()
	t.Setenv("BICZ_STATE_DIR", t.TempDir())
	short, err := os.MkdirTemp("", "bicz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	t.Setenv("BICZ_RUNTIME_DIR", short)
	ledgerPath := filepath.Join(t.TempDir(), "bicz.sqlite")
	op, err := daemon.NewOperator(ledgerPath)
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	t.Cleanup(func() { op.Close() })
	if _, err := op.Open(context.Background(), daemon.OpenRequest{Identity: "alice", InitialCapital: credit, Reason: "hook test"}); err != nil {
		t.Fatalf("open identity: %v", err)
	}
	socket := filepath.Join(short, "s.sock")
	supervisor, err := daemon.New(daemon.Config{
		LedgerPath:   ledgerPath,
		SocketPath:   socket,
		LockPath:     filepath.Join(short, "s.lock"),
		SealInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); supervisor.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done; supervisor.Close() })

	schedule := filepath.Join(t.TempDir(), "schedule.json")
	body := `{"tool_max_cost_mc":{"Bash":400,"Expensive":5000},"default_max_cost_mc":100,"max_cost_cap_mc":10000}`
	if err := os.WriteFile(schedule, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, op: op, socket: socket, cfg: &Config{
		BiczOwned: true, Kind: spec.ConfigKind, AccountID: "alice", Socket: socket,
		SchedulePath: schedule, PolicyVersion: "1",
	}}
}

func (r *rig) account() (balance, held int64) {
	r.t.Helper()
	account, err := r.op.Ledger().GetAccount(context.Background(), "alice")
	if err != nil {
		r.t.Fatalf("GetAccount: %v", err)
	}
	return account.Balance, account.Held
}

// run feeds one event to the hook and returns what it printed.
func run(t *testing.T, spec *Spec, event string, cfg *Config, payload map[string]any) string {
	t.Helper()
	return runWithTimeout(t, spec, event, cfg, payload, 0)
}

func runWithTimeout(t *testing.T, spec *Spec, event string, cfg *Config, payload map[string]any, timeout time.Duration) string {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	err := Run(Options{Spec: spec, Event: event, Stdin: bytes.NewReader(raw), Stdout: &out, Config: cfg, Timeout: timeout})
	if err != nil {
		t.Fatalf("Run %s: %v", event, err)
	}
	return out.String()
}

func tool(spec *Spec, event, name, id string) map[string]any {
	return map[string]any{
		"session_id": "sess-1", "hook_event_name": event, "tool_name": name,
		"tool_use_id": id, "tool_input": map[string]any{"command": "ls"},
	}
}

// specific decodes the hookSpecificOutput object of a response.
func specific(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("response is not the harness JSON shape: %v\n%q", err, out)
	}
	if len(doc) != 1 || doc["hookSpecificOutput"] == nil {
		t.Fatalf("response must be exactly {hookSpecificOutput:{...}}: %q", out)
	}
	return doc["hookSpecificOutput"]
}

func bothSpecs() []*Spec { return []*Spec{ClaudeSpec, CodexSpec} }

func TestSessionStartInjectsProjectionAsAdditionalContext(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 1_000)
		out := run(t, spec, "SessionStart", r.cfg, map[string]any{"session_id": "s", "hook_event_name": "SessionStart", "source": "startup"})
		got := specific(t, out)
		if got["hookEventName"] != "SessionStart" {
			t.Fatalf("%s: hookEventName = %v", spec.ID, got["hookEventName"])
		}
		context, _ := got["additionalContext"].(string)
		if !strings.Contains(context, "identity alice") || !strings.Contains(context, "balance 1000 mc") {
			t.Fatalf("%s: projection missing from context: %q", spec.ID, context)
		}
		if _, ok := got["permissionDecision"]; ok {
			t.Fatalf("%s: SessionStart must not carry a permission decision", spec.ID)
		}
	}
}

func TestPreCompactReinjectsProjectionForClaude(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	got := specific(t, run(t, ClaudeSpec, "PreCompact", r.cfg, map[string]any{"session_id": "s", "hook_event_name": "PreCompact"}))
	if got["hookEventName"] != "PreCompact" || !strings.Contains(got["additionalContext"].(string), "identity alice") {
		t.Fatalf("PreCompact response = %v", got)
	}
}

func TestPreToolUseAllowEmitsNothingAndNeverAllow(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 1_000)

		// A priced call that fits: no output at all, but the reservation is held.
		out := run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Bash", "toolu_1"))
		if out != "" {
			t.Fatalf("%s: a plain allow must print nothing (so the user's permission prompts still run), got %q", spec.ID, out)
		}
		if _, held := r.account(); held != 400 {
			t.Fatalf("%s: held = %d, want the 400 mc reservation", spec.ID, held)
		}

		// A call priced by the schedule default: context disclosure, never a decision.
		out = run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Glob", "toolu_2"))
		got := specific(t, out)
		if _, ok := got["permissionDecision"]; ok {
			t.Fatalf("%s: a defaulted allow must not set permissionDecision: %q", spec.ID, out)
		}
		if !strings.Contains(got["additionalContext"].(string), "conservative default") {
			t.Fatalf("%s: defaulted price must be disclosed: %q", spec.ID, out)
		}
		if strings.Contains(out, `"allow"`) {
			t.Fatalf("%s: output must never contain an allow decision: %q", spec.ID, out)
		}
	}
}

func TestPreToolUseDeniesOnInsufficientCredit(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 1_000)
		out := run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Expensive", "toolu_big"))
		got := specific(t, out)
		if got["hookEventName"] != "PreToolUse" || got["permissionDecision"] != "deny" {
			t.Fatalf("%s: want a PreToolUse deny, got %q", spec.ID, out)
		}
		reason, _ := got["permissionDecisionReason"].(string)
		if !strings.Contains(reason, "insufficient_credit") {
			t.Fatalf("%s: deny reason must carry the daemon's code: %q", spec.ID, reason)
		}
		if _, held := r.account(); held != 0 {
			t.Fatalf("%s: a denied call must not hold credit, held = %d", spec.ID, held)
		}
	}
}

func TestPostToolUseSettlesWithReceiptAndDuplicateSettlesOnce(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 1_000)
		run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Bash", "toolu_1"))

		post := tool(spec, "PostToolUse", "Bash", "toolu_1")
		post["tool_response"] = map[string]any{"stdout": "ok"}
		first := specific(t, run(t, spec, "PostToolUse", r.cfg, post))
		receipt, _ := first["additionalContext"].(string)
		if first["hookEventName"] != "PostToolUse" || !strings.Contains(receipt, "bicz receipt: completed, charged 400 mc, fine 0") {
			t.Fatalf("%s: receipt = %v", spec.ID, first)
		}
		balance, held := r.account()
		if balance != 600 || held != 0 {
			t.Fatalf("%s: after settle balance=%d held=%d, want 600/0", spec.ID, balance, held)
		}

		// The same tool_use_id again (harness retry): no second charge.
		again := specific(t, run(t, spec, "PostToolUse", r.cfg, post))
		if !strings.Contains(again["additionalContext"].(string), "replayed") {
			t.Fatalf("%s: a duplicate settle must be reported as a replay: %v", spec.ID, again)
		}
		if balance, _ := r.account(); balance != 600 {
			t.Fatalf("%s: duplicate tool_use_id charged twice, balance = %d", spec.ID, balance)
		}
	}
}

// Payloads captured verbatim from a real codex-cli 0.162.1 `codex exec` run
// (docs/RUNTIME_HOOKS.md section 3): note tool_name "Bash" for Codex's
// exec_command, tool_response as a bare string, transcript_path null. The Post
// payload is for `echo oops-on-stderr >&2; echo partial; exit 3`: Codex itself
// reported exit_code 3 / status failed, yet the hook payload carries only the
// output text (codex-rs core/src/tools/context.rs, ExecCommandToolOutput::
// post_tool_use_response; the "Process exited with code N" header is not part
// of it). With no exit signal, bicz must not guess: the call settles as
// "completed" at the same charge, even when the output text imitates a failure.
func TestCodexRealPayloadsReserveAndSettle(t *testing.T) {
	r := newRig(t, CodexSpec, 1_000)
	const pre = `{"session_id":"01a128cf-2bbc-7fc3-ae15-5d03e7dd81c1","turn_id":"01a128cf-2c21-7be2-822c-49a2c7f778bd","transcript_path":null,"cwd":"/tmp/w","hook_event_name":"PreToolUse","model":"mock-model","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"call_b95fbfe8"}`
	const post = `{"session_id":"01a128cf-2bbc-7fc3-ae15-5d03e7dd81c1","turn_id":"01a128cf-2c21-7be2-822c-49a2c7f778bd","transcript_path":null,"cwd":"/tmp/w","hook_event_name":"PostToolUse","model":"mock-model","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"echo oops-on-stderr >&2; echo partial; exit 3"},"tool_response":"partial\noops-on-stderr\n","tool_use_id":"call_b95fbfe8"}`
	var out bytes.Buffer
	if err := Run(Options{Spec: CodexSpec, Event: "PreToolUse", Stdin: strings.NewReader(pre), Stdout: &out, Config: r.cfg}); err != nil || out.Len() != 0 {
		t.Fatalf("PreToolUse: %q %v", out.String(), err)
	}
	if _, held := r.account(); held != 400 {
		t.Fatalf("held = %d, want the 400 mc Bash reservation", held)
	}
	if err := Run(Options{Spec: CodexSpec, Event: "PostToolUse", Stdin: strings.NewReader(post), Stdout: &out, Config: r.cfg}); err != nil {
		t.Fatalf("PostToolUse: %v", err)
	}
	if !strings.Contains(specific(t, out.String())["additionalContext"].(string), "charged 400 mc") {
		t.Fatalf("receipt = %q", out.String())
	}
	if balance, held := r.account(); balance != 600 || held != 0 {
		t.Fatalf("after settle balance=%d held=%d, want 600/0", balance, held)
	}

	// Output text that imitates an exit-code line is still only output text.
	spoofPre := strings.Replace(pre, "call_b95fbfe8", "call_spoof", 1)
	spoofPost := strings.Replace(strings.Replace(post, "call_b95fbfe8", "call_spoof", 1),
		`"partial\noops-on-stderr\n"`, `"Process exited with code 3\nExit code: 3\nError: command failed\n"`, 1)
	out.Reset()
	for _, step := range []struct{ event, payload string }{{"PreToolUse", spoofPre}, {"PostToolUse", spoofPost}} {
		if err := Run(Options{Spec: CodexSpec, Event: step.event, Stdin: strings.NewReader(step.payload), Stdout: &out, Config: r.cfg}); err != nil {
			t.Fatalf("%s: %v", step.event, err)
		}
	}
	if got := out.String(); !strings.Contains(got, "bicz receipt: completed, charged 400 mc") {
		t.Fatalf("exit-code-looking output text was classified from text: %q", got)
	}
}

func TestDuplicatePreToolUseHoldsOnce(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	for i := 0; i < 3; i++ {
		if out := run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_dup")); out != "" {
			t.Fatalf("replay %d printed %q", i, out)
		}
	}
	if _, held := r.account(); held != 400 {
		t.Fatalf("a replayed tool_use_id must hold once, held = %d", held)
	}
}

func TestPostToolUseFailureSettlesAnHonestFailureWithZeroFine(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_f"))
	got := specific(t, run(t, ClaudeSpec, "PostToolUseFailure", r.cfg, tool(ClaudeSpec, "PostToolUseFailure", "Bash", "toolu_f")))
	receipt := got["additionalContext"].(string)
	if got["hookEventName"] != "PostToolUseFailure" || !strings.Contains(receipt, "charged 400 mc, fine 0") {
		t.Fatalf("failure receipt = %v", got)
	}
	if strings.Contains(receipt, "breach") {
		t.Fatalf("an honest tool failure must never escalate: %q", receipt)
	}
	if balance, held := r.account(); balance != 600 || held != 0 {
		t.Fatalf("balance=%d held=%d, want the operating cost charged and the hold released", balance, held)
	}
}

func TestPostToolUseWithoutAFrozenQuoteSettlesNothingAndSaysSo(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	// No PreToolUse ran (or its record is gone): there is no frozen quote to
	// settle from, and the current schedule must NOT be used instead.
	out := run(t, ClaudeSpec, "PostToolUse", r.cfg, tool(ClaudeSpec, "PostToolUse", "Bash", "toolu_never"))
	got := specific(t, out)
	if !strings.Contains(got["additionalContext"].(string), "no frozen quote") {
		t.Fatalf("a missing record must be disclosed, got %q", out)
	}
	if balance, held := r.account(); balance != 1_000 || held != 0 {
		t.Fatalf("balance=%d held=%d changed", balance, held)
	}
}

func TestUnreachableDaemonNoticeVersusBlock(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "no.sock")
	for _, spec := range bothSpecs() {
		notice := &Config{BiczOwned: true, Kind: spec.ConfigKind, AccountID: "alice", Socket: dead,
			SchedulePath: writeSchedule(t), UnmanagedMode: ModeNotice}
		block := *notice
		block.UnmanagedMode = ModeBlock

		out := run(t, spec, "PreToolUse", notice, tool(spec, "PreToolUse", "Bash", "toolu_1"))
		got := specific(t, out)
		text, _ := got["additionalContext"].(string)
		if _, ok := got["permissionDecision"]; ok || !strings.Contains(text, "NOT metered") || !strings.Contains(text, "no charge recorded") {
			t.Fatalf("%s notice mode: want pass-through with a NOT metered disclosure, got %q", spec.ID, out)
		}

		out = run(t, spec, "PreToolUse", &block, tool(spec, "PreToolUse", "Bash", "toolu_1"))
		got = specific(t, out)
		reason, _ := got["permissionDecisionReason"].(string)
		if got["permissionDecision"] != "deny" || !strings.HasPrefix(reason, "daemon_unavailable") {
			t.Fatalf("%s block mode: want deny daemon_unavailable, got %q", spec.ID, out)
		}

		// Events that cannot deny disclose even in block mode.
		out = run(t, spec, "SessionStart", &block, map[string]any{"session_id": "s"})
		got = specific(t, out)
		if _, ok := got["permissionDecision"]; ok || !strings.Contains(got["additionalContext"].(string), "NOT metered") {
			t.Fatalf("%s: SessionStart with no daemon must disclose, got %q", spec.ID, out)
		}
		out = run(t, spec, "PostToolUse", &block, tool(spec, "PostToolUse", "Bash", "toolu_1"))
		got = specific(t, out)
		if _, ok := got["permissionDecision"]; ok || !strings.Contains(got["additionalContext"].(string), "no charge recorded") {
			t.Fatalf("%s: PostToolUse with no daemon must disclose, got %q", spec.ID, out)
		}
	}
}

func TestMissingConfigIsDisclosedNotGuessed(t *testing.T) {
	t.Setenv("BICZ_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	raw, _ := json.Marshal(tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_1"))
	var out bytes.Buffer
	if err := Run(Options{Spec: ClaudeSpec, Event: "PreToolUse", Stdin: bytes.NewReader(raw), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	got := specific(t, out.String())
	if _, ok := got["permissionDecision"]; ok || !strings.Contains(got["additionalContext"].(string), "NOT metered") {
		t.Fatalf("a missing config must pass through with a disclosure, got %q", out.String())
	}
}

func TestNoScheduleNoticeVersusBlock(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	cfg := *r.cfg
	cfg.SchedulePath = ""
	out := run(t, ClaudeSpec, "PreToolUse", &cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_1"))
	if got := specific(t, out); got["permissionDecision"] != nil || !strings.Contains(got["additionalContext"].(string), "no published price schedule") {
		t.Fatalf("no-schedule notice = %q", out)
	}
	cfg.NoScheduleMode = ModeBlock
	out = run(t, ClaudeSpec, "PreToolUse", &cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_1"))
	got := specific(t, out)
	if got["permissionDecision"] != "deny" || !strings.HasPrefix(got["permissionDecisionReason"].(string), "no_published_price_schedule") {
		t.Fatalf("no-schedule block = %q", out)
	}
	if _, held := r.account(); held != 0 {
		t.Fatalf("an unpriced call must not reserve anything, held = %d", held)
	}
}

func TestHungDaemonCannotHangTheHook(t *testing.T) {
	short, _ := os.MkdirTemp("", "bicz")
	t.Cleanup(func() { os.RemoveAll(short) })
	socket := filepath.Join(short, "hang.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn // accept and never answer
		}
	}()
	cfg := &Config{BiczOwned: true, Kind: ClaudeSpec.ConfigKind, AccountID: "alice", Socket: socket,
		SchedulePath: writeSchedule(t), UnmanagedMode: ModeBlock}
	start := time.Now()
	out := runWithTimeout(t, ClaudeSpec, "PreToolUse", cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_1"), 300*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hook took %s against a hung daemon", elapsed)
	}
	if got := specific(t, out); got["permissionDecision"] != "deny" || !strings.HasPrefix(got["permissionDecisionReason"].(string), "daemon_unavailable") {
		t.Fatalf("a hung daemon must apply the unmanaged policy, got %q", out)
	}
}

func TestHungStdinCannotHangTheHook(t *testing.T) {
	pr, pw, _ := os.Pipe()
	t.Cleanup(func() { pw.Close(); pr.Close() })
	cfg := &Config{BiczOwned: true, Kind: ClaudeSpec.ConfigKind, AccountID: "alice", Socket: "/nonexistent.sock", UnmanagedMode: ModeNotice}
	var out bytes.Buffer
	start := time.Now()
	err := Run(Options{Spec: ClaudeSpec, Event: "PreToolUse", Stdin: pr, Stdout: &out, Config: cfg, Timeout: 200 * time.Millisecond})
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
	if !strings.Contains(out.String(), "NOT metered") {
		t.Fatalf("a stalled stdin must apply the unmanaged policy, got %q", out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	cfg := &Config{BiczOwned: true, AccountID: "a", Socket: "/x"}
	var out bytes.Buffer
	if err := Run(Options{Spec: CodexSpec, Event: "PostToolUseFailure", Stdin: strings.NewReader("{}"), Stdout: &out, Config: cfg}); err == nil {
		t.Fatal("Codex has no PostToolUseFailure event")
	}
	if err := Run(Options{Spec: ClaudeSpec, Event: "PreToolUse", Stdin: strings.NewReader("not json"), Stdout: &out, Config: cfg}); err == nil {
		t.Fatal("malformed stdin must be an error")
	}
	if out.Len() != 0 {
		t.Fatalf("a failed hook printed %q", out.String())
	}
	if spec, ok := SpecFor("claude-code"); !ok || spec != ClaudeSpec {
		t.Fatal("claude-code must resolve to the claude spec")
	}
	if _, ok := SpecFor("opencode"); ok {
		t.Fatal("unknown harness resolved")
	}
}

func TestMissingToolUseIDIsDisclosedNotMeteredUnderAGuessedKey(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	payload := tool(ClaudeSpec, "PreToolUse", "Bash", "")
	got := specific(t, run(t, ClaudeSpec, "PreToolUse", r.cfg, payload))
	if got["permissionDecision"] != nil || !strings.Contains(got["additionalContext"].(string), "NOT metered") {
		t.Fatalf("got %v", got)
	}
	if _, held := r.account(); held != 0 {
		t.Fatalf("held = %d", held)
	}
}

func writeSchedule(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(`{"tool_max_cost_mc":{"Bash":400},"default_max_cost_mc":100}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- frozen quote -------------------------------------------------------------

func (r *rig) republish(body string) {
	r.t.Helper()
	if err := os.WriteFile(r.cfg.SchedulePath, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func recordFor(t *testing.T, spec *Spec, session, id string) *callState {
	t.Helper()
	calls, err := openStore(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := calls.load(session, id)
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	return st
}

func TestPostToolUseSettlesTheFrozenQuoteNotTheCurrentSchedule(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 1_000)

		// Call 1: priced 400 mc under policy "1". The operator then raises the
		// price and bumps the policy version before the delayed PostToolUse.
		if out := run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Bash", "toolu_1")); out != "" {
			t.Fatalf("%s: pre = %q", spec.ID, out)
		}
		r.republish(`{"tool_max_cost_mc":{"Bash":900},"default_max_cost_mc":100}`)
		r.cfg.PolicyVersion = "2"
		got := specific(t, run(t, spec, "PostToolUse", r.cfg, tool(spec, "PostToolUse", "Bash", "toolu_1")))
		if !strings.Contains(got["additionalContext"].(string), "bicz receipt: completed, charged 400 mc, fine 0") {
			t.Fatalf("%s: a raised schedule must not change the settled amount, got %v", spec.ID, got)
		}
		if balance, held := r.account(); balance != 600 || held != 0 {
			t.Fatalf("%s: balance=%d held=%d, want 600/0", spec.ID, balance, held)
		}
		if st := recordFor(t, spec, "sess-1", "toolu_1"); st.Status != statusSettled || st.PolicyVersion != "1" || st.Amount != 400 {
			t.Fatalf("%s: the record must stay bound to the policy it was quoted under, got %+v", spec.ID, st)
		}

		// Call 2: reserved at 300 mc, then the price is lowered AND the schedule
		// file disappears. The settlement is still the reserved 300, and the hold
		// is released rather than left dangling.
		r.cfg.PolicyVersion = "1"
		r.republish(`{"tool_max_cost_mc":{"Bash":300}}`)
		run(t, spec, "PreToolUse", r.cfg, tool(spec, "PreToolUse", "Bash", "toolu_2"))
		if _, held := r.account(); held != 300 {
			t.Fatalf("%s: held = %d, want 300", spec.ID, held)
		}
		r.republish(`{"tool_max_cost_mc":{"Bash":50}}`)
		os.Remove(r.cfg.SchedulePath)
		got = specific(t, run(t, spec, "PostToolUse", r.cfg, tool(spec, "PostToolUse", "Bash", "toolu_2")))
		if !strings.Contains(got["additionalContext"].(string), "charged 300 mc") {
			t.Fatalf("%s: a removed schedule must not change the settled amount, got %v", spec.ID, got)
		}
		if balance, held := r.account(); balance != 300 || held != 0 {
			t.Fatalf("%s: balance=%d held=%d, want 300/0", spec.ID, balance, held)
		}

		// A duplicate PostToolUse replays the original receipt: no second charge.
		got = specific(t, run(t, spec, "PostToolUse", r.cfg, tool(spec, "PostToolUse", "Bash", "toolu_1")))
		text := got["additionalContext"].(string)
		if !strings.Contains(text, "charged 400 mc") || !strings.Contains(text, "replayed") {
			t.Fatalf("%s: duplicate must replay the original 400 mc receipt, got %q", spec.ID, text)
		}
		if balance, held := r.account(); balance != 300 || held != 0 {
			t.Fatalf("%s: duplicate changed the ledger: balance=%d held=%d", spec.ID, balance, held)
		}
	}
}

func TestReplayedPreToolUseSurvivesAScheduleChange(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_r"))
	r.republish(`{"tool_max_cost_mc":{"Bash":900}}`)
	r.cfg.PolicyVersion = "2"
	// Re-sent with the same id and input: the frozen payload is replayed, so the
	// daemon sees an exact replay (no payload_conflict deny) and holds once.
	if out := run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_r")); out != "" {
		t.Fatalf("a replay after a policy change must still allow quietly, got %q", out)
	}
	if _, held := r.account(); held != 400 {
		t.Fatalf("held = %d, want the original 400", held)
	}
}

func TestReusedToolUseIDForADifferentCallIsAConflict(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_c"))
	other := tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_c")
	other["tool_input"] = map[string]any{"command": "rm -rf /"}
	got := specific(t, run(t, ClaudeSpec, "PreToolUse", r.cfg, other))
	if got["permissionDecision"] != "deny" || !strings.HasPrefix(got["permissionDecisionReason"].(string), "payload_conflict") {
		t.Fatalf("a reused id with different input must be a hard conflict, got %v", got)
	}
	if _, held := r.account(); held != 400 {
		t.Fatalf("a conflicting replay changed the hold: %d", held)
	}
}

func TestLostRecordLeavesTheHoldAndDisclosesIt(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_l"))
	calls, _ := openStore(ClaudeSpec, "")
	calls.remove("sess-1", "toolu_l")
	got := specific(t, run(t, ClaudeSpec, "PostToolUse", r.cfg, tool(ClaudeSpec, "PostToolUse", "Bash", "toolu_l")))
	if !strings.Contains(got["additionalContext"].(string), "stays held") {
		t.Fatalf("got %v", got)
	}
	if balance, held := r.account(); balance != 1_000 || held != 400 {
		t.Fatalf("a lost record must not settle or release: balance=%d held=%d", balance, held)
	}
}

func TestPendingRecordFromALostAuthorizeAnswerSettlesLater(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	dead := *r.cfg
	dead.Socket = filepath.Join(t.TempDir(), "gone.sock")
	// The daemon was unreachable at PreToolUse: the call is disclosed, the quote
	// is frozen as pending, and nothing is reserved.
	out := run(t, ClaudeSpec, "PreToolUse", &dead, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_p"))
	if !strings.Contains(out, "NOT metered") {
		t.Fatalf("pre = %q", out)
	}
	if st := recordFor(t, ClaudeSpec, "sess-1", "toolu_p"); st.Status != statusPending || st.Amount != 400 {
		t.Fatalf("record = %+v", st)
	}
	// Daemon back at PostToolUse: the daemon holds nothing for the call, so there
	// is nothing to settle; the record is dropped and no charge appears.
	if out := run(t, ClaudeSpec, "PostToolUse", r.cfg, tool(ClaudeSpec, "PostToolUse", "Bash", "toolu_p")); out != "" {
		t.Fatalf("post = %q", out)
	}
	if st := recordFor(t, ClaudeSpec, "sess-1", "toolu_p"); st != nil {
		t.Fatalf("an unreserved pending record must be dropped, got %+v", st)
	}
	if balance, held := r.account(); balance != 1_000 || held != 0 {
		t.Fatalf("balance=%d held=%d", balance, held)
	}
}

func TestCorruptStateIsRefusedNotGuessed(t *testing.T) {
	r := newRig(t, ClaudeSpec, 1_000)
	calls, _ := openStore(ClaudeSpec, "")
	if err := os.MkdirAll(calls.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(calls.path("sess-1", "toolu_x"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := specific(t, run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_x")))
	if got["permissionDecision"] != nil || !strings.Contains(got["additionalContext"].(string), "hook state unavailable") {
		t.Fatalf("notice mode: got %v", got)
	}
	block := *r.cfg
	block.UnmanagedMode = ModeBlock
	got = specific(t, run(t, ClaudeSpec, "PreToolUse", &block, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_x")))
	if got["permissionDecision"] != "deny" || !strings.HasPrefix(got["permissionDecisionReason"].(string), "hook_state_unavailable") {
		t.Fatalf("block mode: got %v", got)
	}
	if _, held := r.account(); held != 0 {
		t.Fatalf("an unreadable record must not authorize anything, held = %d", held)
	}
}

func TestStateIsOwnerOnlyAndPruneKeepsLiveHolds(t *testing.T) {
	r := newRig(t, ClaudeSpec, 10_000)
	calls, _ := openStore(ClaudeSpec, "")
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	seed := func(id, status, at string) {
		st := &callState{Version: stateVersion, Harness: "claude-code", SessionID: "sess-1", ToolUseID: id, Tool: "Bash",
			Status: status, Amount: 400, ReservationID: "claude:auth:" + id, At: at, SettledAt: ""}
		if status == statusSettled {
			st.SettledAt = at
		}
		if err := calls.save(st); err != nil {
			t.Fatal(err)
		}
	}
	seed("old_settled", statusSettled, old)
	seed("old_unreserved", statusUnreserved, old)
	seed("old_reserved", statusReserved, old)
	seed("old_pending", statusPending, old)
	seed("new_settled", statusSettled, fresh)

	run(t, ClaudeSpec, "PreToolUse", r.cfg, tool(ClaudeSpec, "PreToolUse", "Bash", "toolu_trigger"))

	for id, want := range map[string]bool{"old_settled": false, "old_unreserved": false, "old_reserved": true, "old_pending": true, "new_settled": true, "toolu_trigger": true} {
		st, err := calls.load("sess-1", id)
		if err != nil {
			t.Fatal(err)
		}
		if (st != nil) != want {
			t.Fatalf("record %s present=%v, want %v", id, st != nil, want)
		}
	}
	info, err := os.Stat(calls.path("sess-1", "toolu_trigger"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	if dir, _ := os.Stat(calls.dir); dir.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %v, want 0700", dir.Mode().Perm())
	}
}

func TestPostToolUsePassingTestGrantsAutorewardRebate(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 10_000)
		toolInput := map[string]any{"command": "go test ./..."}
		pre := tool(spec, "PreToolUse", "Bash", "toolu_test_1")
		pre["tool_input"] = toolInput
		run(t, spec, "PreToolUse", r.cfg, pre)

		post := tool(spec, "PostToolUse", "Bash", "toolu_test_1")
		post["tool_input"] = toolInput
		post["tool_response"] = map[string]any{"stdout": "PASS\nok  pkg 0.02s"}
		out := specific(t, run(t, spec, "PostToolUse", r.cfg, post))
		receipt, _ := out["additionalContext"].(string)

		if !strings.Contains(receipt, "charged 400 mc") {
			t.Fatalf("%s: missing charge in receipt: %s", spec.ID, receipt)
		}
		if !strings.Contains(receipt, "autoreward +400 mc (passing test rebate)") {
			t.Fatalf("%s: missing autoreward in receipt: %s", spec.ID, receipt)
		}
		// Balance before: 10,000. Charged: 400. Autoreward: +400. Balance after: 10,000.
		balance, held := r.account()
		if balance != 10_000 || held != 0 {
			t.Fatalf("%s: net balance after passing test rebate = %d held=%d, want 10000/0", spec.ID, balance, held)
		}
	}
}

func TestPostToolUseFailingTestDoesNotGrantAutoreward(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 10_000)
		toolInput := map[string]any{"command": "go test ./..."}
		pre := tool(spec, "PreToolUse", "Bash", "toolu_test_fail")
		pre["tool_input"] = toolInput
		run(t, spec, "PreToolUse", r.cfg, pre)

		post := tool(spec, "PostToolUse", "Bash", "toolu_test_fail")
		post["tool_input"] = toolInput
		post["tool_response"] = map[string]any{"stdout": "FAIL\n--- FAIL: TestFoo"}
		out := specific(t, run(t, spec, "PostToolUse", r.cfg, post))
		receipt, _ := out["additionalContext"].(string)

		if !strings.Contains(receipt, "charged 400 mc") {
			t.Fatalf("%s: missing charge in receipt: %s", spec.ID, receipt)
		}
		if strings.Contains(receipt, "autoreward") {
			t.Fatalf("%s: failing test must NOT grant autoreward: %s", spec.ID, receipt)
		}
		balance, held := r.account()
		if balance != 9_600 || held != 0 {
			t.Fatalf("%s: balance after failing test = %d held=%d, want 9600/0", spec.ID, balance, held)
		}
	}
}

func TestPostToolUseTodoCompletionGrantsAutoreward(t *testing.T) {
	for _, spec := range bothSpecs() {
		r := newRig(t, spec, 10_000)
		toolInput := map[string]any{"action": "complete", "task_id": "123"}
		pre := tool(spec, "PreToolUse", "todo_manager", "toolu_todo_1")
		pre["tool_input"] = toolInput
		run(t, spec, "PreToolUse", r.cfg, pre)

		post := tool(spec, "PostToolUse", "todo_manager", "toolu_todo_1")
		post["tool_input"] = toolInput
		post["tool_response"] = map[string]any{"status": "completed"}
		out := specific(t, run(t, spec, "PostToolUse", r.cfg, post))
		receipt, _ := out["additionalContext"].(string)

		if !strings.Contains(receipt, "autoreward +100000 mc (todolist item completed)") {
			t.Fatalf("%s: missing todo autoreward in receipt: %s", spec.ID, receipt)
		}
		// Balance before: 10,000. Charged: 100 (default unpriced max cost). Autoreward: +100,000. Balance after: 109,900.
		balance, held := r.account()
		if balance != 109_900 || held != 0 {
			t.Fatalf("%s: balance after todo autoreward = %d held=%d, want 109900/0", spec.ID, balance, held)
		}
	}
}

