package slash

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/policy"
)

func TestParseGrammar(t *testing.T) {
	cases := []struct {
		raw  string
		want Command
	}{
		{"", Command{Action: ActionHelp}},
		{"status", Command{Action: ActionStatus}},
		{"STATUS", Command{Action: ActionStatus}},
		{`loss no_fake_tests "claimed a pass"`, Command{Action: ActionLoss, Rule: "no_fake_tests", Reason: "claimed a pass"}},
		{`loss no_fake_tests 'claimed a pass' "run the suite"`, Command{Action: ActionLoss, Rule: "no_fake_tests", Reason: "claimed a pass", Correction: "run the suite"}},
		{`loss r1 "said \"done\" untested" fix`, Command{Action: ActionLoss, Rule: "r1", Reason: `said "done" untested`, Correction: "fix"}},
		{`loss r1 don't`, Command{Action: ActionLoss, Rule: "r1", Reason: "don't"}},
		{`reward 5 "shipped the fix"`, Command{Action: ActionReward, Credits: 5_000_000, Reason: "shipped the fix"}},
		{`reward 0.000001 ok`, Command{Action: ActionReward, Credits: 1, Reason: "ok"}},
		{`reward 12.5 ok`, Command{Action: ActionReward, Credits: 12_500_000, Reason: "ok"}},
		{"reset", Command{Action: ActionReset, Credits: 0, Reason: "reset budget to unblock agent"}},
		{`reset 10`, Command{Action: ActionReset, Credits: 10_000_000, Reason: "reset budget to unblock agent"}},
		{`reset "unblock"`, Command{Action: ActionReset, Credits: 0, Reason: "unblock"}},
		{`reset 5 "unblock"`, Command{Action: ActionReset, Credits: 5_000_000, Reason: "unblock"}},
	}
	for _, tc := range cases {
		got, err := Parse(tc.raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestParseRejectsAmbiguousAndUnsafeInput(t *testing.T) {
	bad := []string{
		`loss`,                                  // no rule
		`loss no_fake_tests`,                    // no reason
		`loss "claimed a pass" no_fake_tests`,   // reason where the rule goes
		`loss no_tests claimed pass without it`, // unquoted prose would silently become a correction
		`loss r1 ""`,                            // empty reason
		`loss r1 "   "`,                         // blank reason
		`loss r1 "reason" ""`,                   // empty explicit correction
		`loss r1 "a" "b" "c"`,
		`loss r1 "unterminated`,
		`loss r1 "x"y`,
		"loss r1 \"bell\x07\"",
		"loss r1 \"bidi\u202e\"",
		`loss ../../etc "x"`,
		`loss r1;rm "x"`,
		`reward 5`,
		`reward "" why`,
		`reward 0 why`,
		`reward -5 why`,
		`reward +5 why`,
		`reward 1e3 why`,
		`reward 0x10 why`,
		`reward 1,000 why`,
		`reward 1.1234567 why`,
		`reward .5 why`,
		`reward 5. why`,
		`reward 5 credits why`,
		`reward 9223372036854775807 why`,
		`reward 9223372036855 why`,
		`status now`,
		`rewards 5 why`,
		`appeal x`,
	}
	for _, raw := range bad {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) was accepted", raw)
		} else if _, ok := err.(*UsageError); !ok {
			t.Errorf("Parse(%q): want *UsageError, got %T (%v)", raw, err, err)
		}
	}
}

func TestMaliciousStringsAreDataNotSyntax(t *testing.T) {
	payloads := []string{"$(touch PWNED)", "`touch PWNED`", "; touch PWNED #", "${HOME}", "$ARGUMENTS $1", "'; DROP TABLE events; --", `\$(touch PWNED)`}
	dir := t.TempDir()
	t.Chdir(dir)
	for _, payload := range payloads {
		command, err := Parse(`loss r1 "` + strings.ReplaceAll(payload, `"`, `\"`) + `" "fix: ` + strings.ReplaceAll(payload, `"`, `\"`) + `"`)
		if err != nil {
			continue // a payload that is not valid text is refused, which is also safe
		}
		if !strings.Contains(command.Reason, payload) || !strings.Contains(command.Correction, payload) {
			t.Errorf("payload %q was altered or expanded: %+v", payload, command)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("a payload executed")
	}
}

// fixture is a real ledger plus a harness config naming it, in a temp config dir.
type fixture struct {
	db  string
	dir string
}

const testPolicy = `{"initial_capital_mc":100000000,"guard_band_bp":0,"repeat_multipliers":[1,2,4],"repeat_multiplier_cap":4,` +
	`"default_penalty":{"floor_mc":1000000,"fraction_bp":0,"lmax_mc":10000000},"penalties":{}}`

func newFixture(t *testing.T, harnesses ...string) fixture {
	t.Helper()
	for _, name := range []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR", "BICZ_CONFIG"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(dir, "config"))
	db := filepath.Join(dir, "ledger.sqlite")
	op, err := daemon.NewOperator(db)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	ctx := context.Background()
	if _, err := op.Open(ctx, daemon.OpenRequest{Identity: "alice", InitialCapital: 20_000_000, Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := op.Price(ctx, "1", []byte(testPolicy), "fixture policy"); err != nil {
		t.Fatal(err)
	}
	f := fixture{db: db, dir: dir}
	for _, harness := range harnesses {
		f.writeConfig(t, harness, "alice", db)
	}
	return f
}

func (f fixture) writeConfig(t *testing.T, harness, identity, ledgerPath string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"bicz_owned": true, "kind": "bicz." + harness + "-config", "version": 1,
		"account_id": identity, "ledger": ledgerPath, "socket": "/tmp/unused.sock",
	})
	path := ConfigPath(harness)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(harness, arguments, invocation string) Response {
	return Run(context.Background(), Request{Harness: harness, Arguments: arguments, Invocation: invocation})
}

func eventCount(t *testing.T, db string) int64 {
	t.Helper()
	op, err := daemon.NewOperator(db)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	stats, err := op.Ledger().ChainStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stats.Events
}

func TestLossBurnsIssuesCueAndStatusReflectsIt(t *testing.T) {
	newFixture(t, "claude")
	response := run("claude", `loss no_fake_tests "claimed a pass without running it" "run the real suite and report its result"`, "")
	if response.Status != StatusOK {
		t.Fatalf("loss: %+v", response)
	}
	for _, want := range []string{"applied=1 credits (1000000 mc)", "source=explicit", "ACCOUNTABILITY issued",
		"violated no_fake_tests", "run the real suite and report its result", "claimed a pass without running it", "unsigned same-user", "not cryptographic"} {
		if !strings.Contains(response.Text, want) {
			t.Errorf("loss output lacks %q:\n%s", want, response.Text)
		}
	}
	if !strings.HasPrefix(response.EventID, "slash-loss-claude-") {
		t.Errorf("event id %q does not carry slash provenance", response.EventID)
	}
	status := run("claude", "status", "")
	if status.Status != StatusOK || !strings.Contains(status.Text, "BALANCE 19 credits") || !strings.Contains(status.Text, "outstanding=1 credits") ||
		!strings.Contains(status.Text, "confirmed=1") {
		t.Fatalf("status does not show the burn:\n%s", status.Text)
	}
}

func TestLossWithoutCorrectionWritesNothingUntilOneExists(t *testing.T) {
	f := newFixture(t, "claude")
	before := eventCount(t, f.db)

	response := run("claude", `loss no_fake_tests "claimed a pass"`, "")
	if response.Status != StatusNeedsInput || response.ExitCode() != ExitNeedsInput || !strings.Contains(response.Question, "no_fake_tests") {
		t.Fatalf("a loss with no known correction must ask, got %+v", response)
	}
	if !strings.Contains(response.Question, `/bicz loss no_fake_tests "claimed a pass" "`) {
		t.Errorf("the question must show the exact re-run form: %q", response.Question)
	}
	if after := eventCount(t, f.db); after != before {
		t.Fatalf("needs_input wrote %d event(s)", after-before)
	}

	if first := run("claude", `loss no_fake_tests "first breach" "run the real suite"`, ""); first.Status != StatusOK {
		t.Fatalf("first loss: %+v", first)
	}
	// The correction is now saved for this rule; reuse is explicit in the output.
	second := run("claude", `loss no_fake_tests "second breach"`, "")
	if second.Status != StatusOK || !strings.Contains(second.Text, `source=saved: "run the real suite"`) {
		t.Fatalf("saved correction not reused: %+v", second)
	}
	// A different rule has no saved correction.
	if other := run("claude", `loss other_rule "x"`, ""); other.Status != StatusNeedsInput {
		t.Fatalf("a different rule must not borrow another rule's correction: %+v", other)
	}
}

type fixedPrompter string

func (p fixedPrompter) Correction(string) (string, error) { return string(p), nil }

func TestInteractivePromptFillsAMissingCorrection(t *testing.T) {
	newFixture(t, "codex")
	response := Run(context.Background(), Request{Harness: "codex", Arguments: `loss r1 "why"`, Prompter: fixedPrompter("  do it right  ")})
	if response.Status != StatusOK || !strings.Contains(response.Text, `source=prompted: "do it right"`) {
		t.Fatalf("prompted loss: %+v", response)
	}
	blank := Run(context.Background(), Request{Harness: "codex", Arguments: `loss r2 "why"`, Prompter: fixedPrompter("   ")})
	if blank.Status == StatusOK {
		t.Fatalf("a blank prompted correction was accepted: %+v", blank)
	}
}

func TestDuplicateSubmissionDoesNotDoublePenalize(t *testing.T) {
	f := newFixture(t, "claude")
	const args = `loss r1 "claimed a pass" "run the suite"`
	first := run("claude", args, "submit-1")
	afterFirst := eventCount(t, f.db)
	retry := run("claude", args, "submit-1")
	if eventCount(t, f.db) != afterFirst {
		t.Fatal("a retry appended ledger events")
	}
	if first.Status != StatusOK || retry.Status != StatusOK || !retry.Replayed || retry.EventID != first.EventID {
		t.Fatalf("retry must replay the same event: first=%+v retry=%+v", first, retry)
	}
	if strings.Contains(retry.Text, "violated") || !strings.Contains(retry.Text, "no second burn") {
		t.Errorf("a retry must not issue a second cue:\n%s", retry.Text)
	}
	status := run("claude", "status", "")
	if !strings.Contains(status.Text, "BALANCE 19 credits") {
		t.Fatalf("a retry burned twice:\n%s", status.Text)
	}
	// With no invocation id an identical command is the same event too.
	if again := run("claude", args, ""); again.Status != StatusOK || again.EventID == first.EventID {
		t.Fatalf("the no-invocation key must differ from the invocation key: %+v", again)
	}
	if replay := run("claude", args, ""); !replay.Replayed {
		t.Fatalf("an identical command without an invocation id must replay: %+v", replay)
	}
	// A new submission (new invocation id) is a new event.
	if next := run("claude", args, "submit-2"); next.Replayed || next.Status != StatusOK {
		t.Fatalf("a new submission must be a new loss: %+v", next)
	}
	rewardA := run("claude", `reward 2 "good work"`, "r-1")
	rewardB := run("claude", `reward 2 "good work"`, "r-1")
	if !rewardB.Replayed || rewardA.EventID != rewardB.EventID {
		t.Fatalf("a reward retry must replay: %+v %+v", rewardA, rewardB)
	}
}

func TestRewardCreditsDecimalAmountExactly(t *testing.T) {
	newFixture(t, "claude")
	response := run("claude", `reward 2.5 "shipped the fix"`, "")
	if response.Status != StatusOK || !strings.Contains(response.Text, "amount=2.5 credits (2500000 mc)") ||
		!strings.Contains(response.Text, "balance=22.5 credits (22500000 mc)") {
		t.Fatalf("reward: %+v", response)
	}
	if status := run("claude", "status", ""); !strings.Contains(status.Text, "BALANCE 22.5 credits (22500000 mc)") {
		t.Fatalf("status after reward:\n%s", status.Text)
	}
	for _, bad := range []string{`reward 1.0000001 x`, `reward -1 x`, `reward 5`, `reward 5 ""`} {
		before := run("claude", "status", "").Text
		if r := run("claude", bad, ""); r.Status != StatusError || r.ExitCode() != ExitUsage {
			t.Errorf("%q must be a usage error: %+v", bad, r)
		}
		if run("claude", "status", "").Text != before {
			t.Errorf("%q changed state", bad)
		}
	}
}

func TestEachHarnessUsesItsOwnConfigAndRefusesBrokenOnes(t *testing.T) {
	f := newFixture(t)
	// Two harnesses, two distinct ledgers.
	other := filepath.Join(f.dir, "other.sqlite")
	op, err := daemon.NewOperator(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Open(context.Background(), daemon.OpenRequest{Identity: "bob", InitialCapital: 7_000_000, Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	op.Close()
	f.writeConfig(t, "claude", "alice", f.db)
	f.writeConfig(t, "codex", "bob", other)

	if s := run("claude", "status", ""); !strings.Contains(s.Text, "identity=alice") || !strings.Contains(s.Text, "BALANCE 20 credits") {
		t.Fatalf("claude must see alice:\n%s", s.Text)
	}
	if s := run("codex", "status", ""); !strings.Contains(s.Text, "identity=bob") || !strings.Contains(s.Text, "BALANCE 7 credits") {
		t.Fatalf("codex must see bob:\n%s", s.Text)
	}
	// A harness with no config is refused; it never borrows another's.
	if s := run("omp", "status", ""); s.Status != StatusError || !strings.Contains(s.Error, "bicz install --harness omp") {
		t.Fatalf("omp without config: %+v", s)
	}
	// A config of the wrong kind in this harness's slot is refused.
	wrong, _ := os.ReadFile(ConfigPath("claude"))
	if err := os.WriteFile(ConfigPath("omp"), wrong, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := run("omp", "status", ""); s.Status != StatusError || !strings.Contains(s.Error, "not a bicz-owned omp config") {
		t.Fatalf("a claude config must not serve omp: %+v", s)
	}
	for name, mutate := range map[string]func(map[string]any){
		"no identity":         func(c map[string]any) { c["account_id"] = " " },
		"relative ledger":     func(c map[string]any) { c["ledger"] = "ledger.sqlite" },
		"no ledger":           func(c map[string]any) { delete(c, "ledger") },
		"not bicz owned":      func(c map[string]any) { c["bicz_owned"] = false },
		"missing ledger file": func(c map[string]any) { c["ledger"] = filepath.Join(f.dir, "absent.sqlite") },
	} {
		cfg := map[string]any{}
		_ = json.Unmarshal(wrong, &cfg)
		cfg["kind"] = "bicz.opencode-config"
		mutate(cfg)
		body, _ := json.Marshal(cfg)
		if err := os.WriteFile(ConfigPath("opencode"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if s := run("opencode", `reward 1 "x"`, ""); s.Status != StatusError {
			t.Errorf("%s: a broken config was accepted: %+v", name, s)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "absent.sqlite")); err == nil {
		t.Fatal("a slash command created a ledger")
	}
	if _, err := NormalizeHarness("claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeHarness("vim"); err == nil {
		t.Fatal("unknown harness accepted")
	}
}

func TestUnknownIdentityAndSuppliedAuthorityWriteNothing(t *testing.T) {
	f := newFixture(t)
	f.writeConfig(t, "claude", "ghost", f.db)
	before := eventCount(t, f.db)
	if r := run("claude", `reward 1 "x"`, ""); r.Status != StatusError {
		t.Fatalf("an identity that is not in the ledger was credited: %+v", r)
	}
	f.writeConfig(t, "claude", "alice", f.db)
	t.Setenv("BICZ_AUTHORITY", "operator")
	r := run("claude", `loss r1 "x" "fix"`, "")
	if r.Status != StatusError || r.Code != "untrusted_authority" {
		t.Fatalf("supplied authority must be refused: %+v", r)
	}
	t.Setenv("BICZ_AUTHORITY", "")
	if after := eventCount(t, f.db); after != before {
		t.Fatalf("refusals wrote %d event(s)", after-before)
	}
}

func TestHelpWritesNothingAndNeedsNoConfig(t *testing.T) {
	t.Setenv("BICZ_CONFIG_DIR", t.TempDir())
	t.Setenv("BICZ_CONFIG", "")
	r := run("claude", "", "")
	if r.Status != StatusOK || r.Action != ActionHelp || !strings.Contains(r.Text, "/bicz loss RULE") {
		t.Fatalf("help: %+v", r)
	}
}

// lockLedger holds the SQLite write lock from a second connection, as a busy
// ledger would, until the returned func releases it.
func lockLedger(t *testing.T, db string) func() {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+db+"?_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS lock_probe(x)`); err != nil {
		t.Fatal(err)
	}
	return func() { _ = tx.Rollback(); _ = conn.Close() }
}

// slashDeadlineForTest mirrors hooks.slashDeadline (not importable here).
const slashDeadlineForTest = 12 * time.Second

func TestExpiredContextOrLockedLedgerRecordsNothingAndRetryRecordsOnce(t *testing.T) {
	f := newFixture(t, "claude")
	const args = `loss r1 "claimed a pass" "run the suite"`
	before := eventCount(t, f.db)

	// Context already expired: refused before any write, never "recorded".
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	response := Run(expired, Request{Harness: "claude", Arguments: args, Invocation: "submit-1"})
	if response.Status != StatusError || !strings.Contains(response.Text, "Nothing was recorded") {
		t.Fatalf("a cancelled command must report that nothing was recorded: %+v", response)
	}

	// A write blocked behind another writer. SQLite's busy wait is not
	// interruptible by the context, so it ends at the ledger's 10 s busy timeout
	// with "database is locked" (BEGIN IMMEDIATE never took the lock, so nothing
	// was written). That must land before the hook's own deadline, or the hook
	// would report a timeout for a write that could still be pending.
	if testing.Short() {
		t.Skip("waits out the 10 s SQLite busy timeout")
	}
	release := lockLedger(t, f.db)
	started := time.Now()
	ctx, stop := context.WithTimeout(context.Background(), slashDeadlineForTest)
	defer stop()
	blocked := Run(ctx, Request{Harness: "claude", Arguments: args, Invocation: "submit-1"})
	elapsed := time.Since(started)
	release()
	if blocked.Status != StatusError || !strings.Contains(blocked.Text, "Nothing was recorded") {
		t.Fatalf("a blocked command must report that nothing was recorded: %+v", blocked)
	}
	if elapsed >= slashDeadlineForTest {
		t.Fatalf("the busy timeout must end the wait before the hook deadline: returned after %s", elapsed)
	}
	if got := eventCount(t, f.db); got != before {
		t.Fatalf("a refused command appended events: %d -> %d", before, got)
	}

	// The retry of the same submission records exactly once; a further retry replays.
	first := run("claude", args, "submit-1")
	retry := run("claude", args, "submit-1")
	if first.Status != StatusOK || first.Replayed || !retry.Replayed || retry.EventID != first.EventID {
		t.Fatalf("retry after a refused attempt: first=%+v retry=%+v", first, retry)
	}
	if status := run("claude", "status", ""); !strings.Contains(status.Text, "BALANCE 19 credits") {
		t.Fatalf("burned other than once:\n%s", status.Text)
	}
}

func TestFailedCommitIsReportedAsUnknownNotAsNothingRecorded(t *testing.T) {
	err := fmt.Errorf("ledger: commit: %w: %w", ledger.ErrCommitOutcomeUnknown, errors.New("disk I/O error"))
	response := failure(ActionLoss, err)
	if response.Code != "outcome_unknown" || strings.Contains(response.Text, "Nothing was recorded") ||
		!strings.Contains(response.Text, "OUTCOME UNKNOWN") || !strings.Contains(response.Text, "/bicz status") {
		t.Fatalf("a failed commit must be reported as ambiguous: %+v", response)
	}
}

// A shipped default rule needs no typed correction, but it never overrides one
// the operator wrote: typed beats saved beats shipped default.
func TestCorrectionPrecedenceExplicitThenSavedThenDefault(t *testing.T) {
	f := newFixture(t, "claude")
	catalog, err := policy.Load()
	if err != nil {
		t.Fatal(err)
	}
	const rule = "no_unverified_done_claim"
	shipped, _ := catalog.Rule(rule)

	first := run("claude", `loss `+rule+` "claimed deploy, not live"`, "")
	if first.Status != StatusOK || !strings.Contains(first.Text, "source=default: "+strconvQuote(shipped.Correction)) {
		t.Fatalf("a default rule with no saved correction must use the shipped one: %+v", first)
	}
	op, err := daemon.NewOperator(f.db)
	if err != nil {
		t.Fatal(err)
	}
	lessons, err := op.Ledger().Lessons(context.Background(), "alice")
	op.Close()
	if err != nil || len(lessons) != 1 || lessons[0].Scope != shipped.Scope || lessons[0].Exceptions != shipped.Exceptions {
		t.Fatalf("the lesson must carry the catalog scope and exceptions: %+v err=%v", lessons, err)
	}

	typed := run("claude", `loss `+rule+` "second breach" "typed by the operator"`, "")
	if typed.Status != StatusOK || !strings.Contains(typed.Text, `source=explicit: "typed by the operator"`) {
		t.Fatalf("a typed correction must beat the shipped default: %+v", typed)
	}
	saved := run("claude", `loss `+rule+` "third breach"`, "")
	if saved.Status != StatusOK || !strings.Contains(saved.Text, `source=saved: "typed by the operator"`) {
		t.Fatalf("a saved correction must beat the shipped default: %+v", saved)
	}

	before := eventCount(t, f.db)
	unknown := run("claude", `loss not_a_default_rule "x"`, "")
	if unknown.Status != StatusNeedsInput || !strings.Contains(unknown.Question, "/bicz rules") || eventCount(t, f.db) != before {
		t.Fatalf("a rule that is neither saved nor shipped must ask and write nothing: %+v", unknown)
	}
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// Every shipped rule must be usable through the real grammar: a rule id or
// correction the parser refused would make its default unreachable.
func TestEveryShippedRuleSurvivesTheSlashParser(t *testing.T) {
	catalog, err := policy.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Rules) == 0 {
		t.Fatal("empty catalog")
	}
	for _, rule := range catalog.Rules {
		raw := `loss ` + rule.ID + ` "reason" ` + strconvQuote(rule.Correction)
		command, err := Parse(raw)
		if err != nil || command.Rule != rule.ID || command.Correction != rule.Correction {
			t.Errorf("rule %s is not usable through /bicz loss: %+v err=%v", rule.ID, command, err)
		}
		if strings.ContainsAny(rule.Correction, "\r\n") || len([]rune(rule.Correction)) > 500 {
			t.Errorf("rule %s correction must be one line of at most 500 characters", rule.ID)
		}
	}
}

func TestRulesListsDefaultsWithoutAConfigOrAWrite(t *testing.T) {
	t.Setenv("BICZ_CONFIG_DIR", t.TempDir())
	t.Setenv("BICZ_CONFIG", "")
	r := run("claude", "rules", "")
	if r.Status != StatusOK || r.Action != ActionRules {
		t.Fatalf("rules: %+v", r)
	}
	columns := map[string][]string{}
	for _, line := range strings.Split(r.Text, "\n") {
		if fields := strings.Fields(line); len(fields) >= 3 {
			columns[fields[0]] = fields[1:3]
		}
	}
	if got := columns["no_unverified_done_claim"]; len(got) != 2 || got[0] != "high" || got[1] != "on" {
		t.Errorf("no_unverified_done_claim columns = %v, want [high on]", got)
	}
	if got := columns["no_idle_waiting"]; len(got) != 2 || got[0] != "low" || got[1] != "off" {
		t.Errorf("no_idle_waiting columns = %v, want [low off]", got)
	}
	if bad := run("claude", "rules extra", ""); bad.Code != "usage" {
		t.Fatalf("rules takes no arguments: %+v", bad)
	}
}

func TestRunResetUnblocksAgentWithoutTouchingLoss(t *testing.T) {
	fx := newFixture(t, "claude")

	// Apply a loss first
	lossResp := run("claude", `loss no_fake_pass "bad test claim" "quote real output"`, "")
	if lossResp.Status != StatusOK {
		t.Fatalf("loss failed: %+v", lossResp)
	}

	// Drain the remaining balance
	op, err := daemon.NewOperator(fx.db)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	projBefore, err := op.Ledger().Projection(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if projBefore.OutstandingLoss == 0 {
		t.Fatal("expected outstanding loss after loss command")
	}

	// Drain balance to 0 in capped attempts: recorded pain limits what one
	// attempt may reserve, so drain in chunks like real metered work.
	for round := 0; ; round++ {
		proj, err := op.Ledger().Projection(context.Background(), "alice")
		if err != nil {
			t.Fatal(err)
		}
		if proj.Spendable <= 0 {
			break
		}
		bp := int64(10-proj.Pain)*900 + 1000
		if bp < 1000 {
			bp = 1000
		}
		if bp > 10000 {
			bp = 10000
		}
		amount := proj.Spendable * bp / 10000
		if amount > proj.Spendable {
			amount = proj.Spendable
		}
		if amount < 1 {
			amount = 1
		}
		r, err := op.Ledger().Reserve(context.Background(), "alice",
			fmt.Sprintf("res-drain-%d", round), "", amount)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := op.Ledger().Settle(context.Background(), r.Reservation.ID, amount, ledger.ClassCompleted, "sha256:evidence"); err != nil {
			t.Fatal(err)
		}
	}

	// Verify balance is 0
	projEmpty, _ := op.Ledger().Projection(context.Background(), "alice")
	if projEmpty.Spendable != 0 {
		t.Fatalf("spendable = %d, want 0", projEmpty.Spendable)
	}

	// Run /bicz reset
	resetResp := run("claude", `reset`, "")
	if resetResp.Status != StatusOK || resetResp.Action != ActionReset {
		t.Fatalf("reset failed: %+v", resetResp)
	}
	if !strings.Contains(resetResp.Text, "BUDGET balance reset") {
		t.Errorf("expected text to mention BUDGET balance reset, got: %s", resetResp.Text)
	}
	if !strings.Contains(resetResp.Text, "(unchanged)") {
		t.Errorf("expected text to explicitly mention outstanding loss (unchanged), got: %s", resetResp.Text)
	}

	// Check projection after reset: balance replenished, loss untouched
	projAfter, err := op.Ledger().Projection(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if projAfter.Spendable <= 0 {
		t.Fatalf("spendable = %d, want > 0 after reset", projAfter.Spendable)
	}
	if projAfter.OutstandingLoss != projBefore.OutstandingLoss {
		t.Fatalf("outstanding loss changed: got %d, want %d (must be untouched!)", projAfter.OutstandingLoss, projBefore.OutstandingLoss)
	}
}
