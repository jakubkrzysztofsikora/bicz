package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/pressure"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// childEnv marks a re-exec of this test binary as a supervisor process. It is
// the standard Go pattern for exercising the real spawn path without building a
// separate binary.
const childEnv = "BICZ_TEST_SUPERVISOR"

// TestMain lets this test binary act as a supervisor when re-executed.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		runChildSupervisor()
		return
	}
	os.Exit(m.Run())
}

// runChildSupervisor runs a supervisor in this process until it is killed. It is
// reached only through the re-exec path above.
func runChildSupervisor() {
	cfg := Config{
		LedgerPath: os.Getenv("BICZ_TEST_DB"),
		SocketPath: os.Getenv("BICZ_TEST_SOCKET"),
		LockPath:   os.Getenv("BICZ_TEST_LOCK"),
	}
	supervisor, err := New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "child supervisor: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signalContext()
	defer stop()
	go func() {
		<-ctx.Done()
		supervisor.Close() //nolint:errcheck // shutdown is best effort
	}()
	if err := supervisor.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "child supervisor: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// signalContext is the child supervisor's shutdown signal source.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

type testClock struct {
	step int64
	base time.Time
}

func (c *testClock) now() time.Time {
	c.step++
	return c.base.Add(time.Duration(c.step) * time.Second)
}

type harness struct {
	t   *testing.T
	cfg Config
	op  *Operator
}

// newHarness builds an isolated ledger and endpoint inside a temporary
// directory. Nothing is written under the user's home or runtime directory: the
// endpoint and lock paths are explicit.
// shortDir is a temporary directory short enough for a Unix socket path.
//
// t.TempDir() embeds the test name, which on macOS pushes the endpoint past the
// kernel's ~103-byte sun_path limit. The ledger lives in t.TempDir(); only the
// endpoint needs the short path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bicz")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	endpoint := shortDir(t)
	clock := &testClock{base: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	cfg := Config{
		LedgerPath:   filepath.Join(dir, "bicz.sqlite"),
		SocketPath:   filepath.Join(endpoint, "s.sock"),
		LockPath:     filepath.Join(endpoint, "s.lock"),
		SealInterval: time.Hour,
		Clock:        canonical.Clock(clock.now),
		ReadyTimeout: 5 * time.Second,
	}
	op, err := NewOperator(cfg.LedgerPath)
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	t.Cleanup(func() {
		if err := op.Close(); err != nil {
			t.Errorf("close operator: %v", err)
		}
	})
	return &harness{t: t, cfg: cfg, op: op}
}

// spawner runs the supervisor in this process. It exercises the real locking and
// readiness logic; only the process boundary differs.
func (h *harness) spawner(spawned *atomic.Int64, fail bool) Spawner {
	h.t.Helper()
	return func(cfg Config) (func() error, error) {
		if fail {
			return nil, errors.New("spawner refused")
		}
		spawned.Add(1)
		supervisor, err := New(cfg)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			supervisor.Serve(ctx) //nolint:errcheck // shutdown closes the listener
		}()
		stop := func() error {
			cancel()
			<-done
			return supervisor.Close()
		}
		h.t.Cleanup(func() { stop() }) //nolint:errcheck // idempotent
		return stop, nil
	}
}

// processSpawner re-executes this test binary as a real supervisor process.
func (h *harness) processSpawner(spawned *atomic.Int64) Spawner {
	h.t.Helper()
	executable, err := os.Executable()
	if err != nil {
		h.t.Fatalf("os.Executable: %v", err)
	}
	return func(cfg Config) (func() error, error) {
		spawned.Add(1)
		command := exec.Command(executable) //nolint:gosec // the test binary re-execs itself
		command.Env = append(os.Environ(),
			childEnv+"=1",
			"BICZ_TEST_DB="+cfg.LedgerPath,
			"BICZ_TEST_SOCKET="+cfg.SocketPath,
			"BICZ_TEST_LOCK="+cfg.LockPath,
		)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			return nil, fmt.Errorf("start child: %w", err)
		}
		stop := func() error {
			if command.Process != nil {
				command.Process.Kill() //nolint:errcheck // best effort
			}
			command.Wait() //nolint:errcheck // reaping the killed child
			return nil
		}
		h.t.Cleanup(func() { stop() })
		return stop, nil
	}
}

func ensureCode(t *testing.T, err error, want protocol.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", want)
	}
	if got := protocol.CodeOf(err); got != want {
		t.Fatalf("code = %s, want %s (err=%v)", got, want, err)
	}
}

// fund provisions and credits an identity straight through the operator CLI
// path, which is how credit enters the ledger.
func (h *harness) fund(identity string, microcredits int64) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.op.Open(ctx, OpenRequest{Identity: identity, Reason: "provision identity"}); err != nil {
		h.t.Fatalf("Open: %v", err)
	}
	if _, err := h.op.Reward(ctx, RewardRequest{
		EventID: "fund:" + identity, Identity: identity, Amount: microcredits,
		Reason: "operator grant", Evidence: "test",
	}); err != nil {
		h.t.Fatalf("Reward: %v", err)
	}
}

func (h *harness) eventCount(prefix string) int {
	h.t.Helper()
	events, err := h.op.Ledger().Events(context.Background())
	if err != nil {
		h.t.Fatalf("Events: %v", err)
	}
	count := 0
	for _, event := range events {
		if strings.HasPrefix(event.ID, prefix) {
			count++
		}
	}
	return count
}

// --- lazy start and single-instance ------------------------------------------

func TestSimultaneousLaunchesCreateExactlyOneSupervisor(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)

	const launchers = 4
	results := make([]EnsureResult, launchers)
	errs := make([]error, launchers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range launchers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = Ensure(ctx, h.cfg)
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("launcher %d: %v", i, err)
		}
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned %d supervisors, want exactly 1", spawned.Load())
	}
	created := 0
	for _, result := range results {
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1 (the rest must reuse it)", created)
	}
	// Every launcher ends up with a working client against the same supervisor.
	for i, result := range results {
		state, err := result.Client.Status(ctx, protocol.StatusPayload{})
		if err != nil {
			t.Fatalf("launcher %d status: %v", i, err)
		}
		if state.Daemon != protocol.DaemonRecording {
			t.Fatalf("launcher %d daemon = %s, want recording", i, state.Daemon)
		}
		if state.Socket != h.cfg.SocketPath {
			t.Fatalf("launcher %d socket = %s, want %s", i, state.Socket, h.cfg.SocketPath)
		}
	}
	// Exactly one supervisor means exactly one owner lock holder: a second
	// in-process supervisor for the same ledger must refuse.
	if _, err := New(h.cfg); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second supervisor for the same ledger: err = %v, want ErrAlreadyRunning", err)
	}
}

func TestEnsureReusesARunningSupervisor(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)

	first, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if !first.Created {
		t.Fatal("first Ensure did not create a supervisor")
	}
	for round := range 3 {
		again, err := Ensure(ctx, h.cfg)
		if err != nil {
			t.Fatalf("round %d Ensure: %v", round, err)
		}
		if again.Created || again.Stop != nil {
			t.Fatalf("round %d created a supervisor instead of reusing one", round)
		}
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned %d supervisors, want 1", spawned.Load())
	}
}

func TestRealProcessSupervisorIsStartedExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.processSpawner(&spawned)

	results := make([]EnsureResult, 3)
	errs := make([]error, 3)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range results {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = Ensure(ctx, h.cfg)
		}()
	}
	start.Done()
	done.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("launcher %d: %v", i, err)
		}
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned %d OS processes, want exactly 1", spawned.Load())
	}
	// The endpoint really is served by a separate process.
	state, err := results[0].Client.Status(ctx, protocol.StatusPayload{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state.Daemon != protocol.DaemonRecording {
		t.Fatalf("daemon = %s, want recording", state.Daemon)
	}
}

// --- unmanaged is disclosed, never silent -----------------------------------

func TestEnsureReportsUnmanagedWhenNoSupervisorCanStart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, true)

	result, err := Ensure(ctx, h.cfg)
	ensureCode(t, err, protocol.CodeDaemonUnavailable)
	if result.Client != nil || result.Created {
		t.Fatalf("Ensure returned a client for an unmanaged ledger: %+v", result)
	}
	if !strings.Contains(err.Error(), "daemon_unavailable") {
		t.Fatalf("error does not name the code: %v", err)
	}
}

func TestStatusReportsUnmanagedWithoutError(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	state := Status(ctx, h.cfg)
	if state.Daemon != protocol.DaemonUnmanaged {
		t.Fatalf("daemon = %s, want unmanaged", state.Daemon)
	}
	if state.Note == "" || !strings.Contains(state.Note, "NOT happening") {
		t.Fatalf("unmanaged note does not disclose that recording is not happening: %q", state.Note)
	}
	// The unobserved paths are disclosed even when nothing is recording.
	if len(state.Coverage) == 0 {
		t.Fatal("unmanaged status disclosed no coverage table")
	}
	if len(protocol.UnobservedPaths("")) == 0 {
		t.Fatal("no unobserved path is disclosed")
	}
}

// --- authorize ---------------------------------------------------------------

func TestAuthorizeAllowDenyIdempotentAndConflict(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	client := ensure.Client
	h.fund("alice", 1_000)

	// Denied: the request exceeds the spendable balance.
	denied, err := client.Authorize(ctx, "auth-1", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "ls", ArgsDigest: "sha256:a", MaxCost: 5_000, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if denied.Decision != protocol.DecisionDeny || denied.Reason != protocol.CodeInsufficientCredit {
		t.Fatalf("denied = %+v", denied)
	}
	if denied.ReservationID != nil {
		t.Fatalf("a denied authorize returned a reservation: %+v", denied)
	}

	// Allowed: it fits.
	allowed, err := client.Authorize(ctx, "auth-2", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "ls", ArgsDigest: "sha256:b", MaxCost: 400, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if allowed.Decision != protocol.DecisionAllow || allowed.ReservationID == nil || *allowed.ReservationID != "auth-2" {
		t.Fatalf("allowed = %+v", allowed)
	}
	if allowed.Projection == nil || allowed.Projection.Balance != 1_000 || allowed.Projection.Held != 400 {
		t.Fatalf("allow did not carry the projection deltas: %+v", allowed.Projection)
	}

	// Idempotent replay: same key, same payload.
	replay, err := client.Authorize(ctx, "auth-2", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "ls", ArgsDigest: "sha256:b", MaxCost: 400, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Reason != protocol.CodeDuplicateKeyOK {
		t.Fatalf("replay reason = %s, want duplicate_key_ok", replay.Reason)
	}
	account, err := h.op.Ledger().GetAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account.Held != 400 {
		t.Fatalf("replay changed the hold: held = %d, want 400", account.Held)
	}

	// Conflicting replay: same key, different payload. A hard error, nothing
	// written.
	conflict, err := client.Authorize(ctx, "auth-2", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "ls", ArgsDigest: "sha256:b", MaxCost: 401, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("conflicting Authorize: %v", err)
	}
	if conflict.Decision != protocol.DecisionDeny || conflict.Reason != protocol.CodePayloadConflict {
		t.Fatalf("conflict = %+v", conflict)
	}
	// Re-targeting the same key is also a conflict (docs/CONTRACTS.md §2).
	retarget, err := client.Authorize(ctx, "auth-2", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "rm -rf /", ArgsDigest: "sha256:b", MaxCost: 400, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("re-targeted Authorize: %v", err)
	}
	if retarget.Decision != protocol.DecisionDeny || retarget.Reason != protocol.CodePayloadConflict {
		t.Fatalf("re-target = %+v", retarget)
	}
	account, _ = h.op.Ledger().GetAccount(ctx, "alice")
	if account.Held != 400 {
		t.Fatalf("a conflicting replay changed the hold: %d", account.Held)
	}
}

func TestAuthorizeDeniesSuspendedAndUnknownIdentities(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.fund("alice", 1_000)
	if _, err := h.op.Ledger().SetRole(ctx, "alice", ledger.RoleSuspended, "audit", "review-1", "role-1"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	suspended, err := ensure.Client.Authorize(ctx, "auth-s", protocol.AuthorizePayload{
		AccountID: "alice", MaxCost: 100, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if suspended.Decision != protocol.DecisionDeny || suspended.Reason != protocol.CodeSuspended {
		t.Fatalf("suspended = %+v", suspended)
	}

	unknown, err := ensure.Client.Authorize(ctx, "auth-u", protocol.AuthorizePayload{
		AccountID: "nobody", MaxCost: 100, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if unknown.Decision != protocol.DecisionDeny || unknown.Reason != protocol.CodeUnknownIdentity {
		t.Fatalf("unknown = %+v", unknown)
	}
}

func TestAuthorizeReportsUnobservedPathsWithoutMetering(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.fund("alice", 1_000)

	// A disclosed unobserved harness path.
	unobserved, err := ensure.Client.Authorize(ctx, "auth-1", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "claude-code", Source: "hosted tools",
		Tool: "web_search", MaxCost: 500, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if unobserved.Decision != protocol.DecisionUnobserved {
		t.Fatalf("decision = %s, want unobserved", unobserved.Decision)
	}
	if unobserved.ReservationID != nil {
		t.Fatal("an unobserved path was given a reservation")
	}
	if len(unobserved.UnobservedPaths) != 1 || !strings.Contains(unobserved.UnobservedPaths[0], "hosted tools") {
		t.Fatalf("unobserved paths = %v", unobserved.UnobservedPaths)
	}
	// Nothing was reserved and nothing was written.
	account, err := h.op.Ledger().GetAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account.Held != 0 {
		t.Fatalf("an unobserved path created a hold: %d", account.Held)
	}
	if h.eventCount("res:auth-1") != 0 {
		t.Fatal("an unobserved path appended a reserve event")
	}

	// An explicit disclosure of an uncovered path.
	declared, err := ensure.Client.Authorize(ctx, "auth-2", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Tool: "browser.open",
		OutsideCoverage: true, CoverageNote: "host bridge emits no tool event",
		MaxCost: 500, PolicyVersion: "1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if declared.Decision != protocol.DecisionUnobserved {
		t.Fatalf("decision = %s, want unobserved", declared.Decision)
	}
	if !strings.Contains(declared.Notice, "host bridge emits no tool event") {
		t.Fatalf("the disclosure was not echoed: %q", declared.Notice)
	}
}

// --- settle ------------------------------------------------------------------

func TestSettleThroughTheDaemonProducesExactlyOneEvent(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.fund("alice", 1_000)

	allowed, err := ensure.Client.Authorize(ctx, "action-1", protocol.AuthorizePayload{
		AccountID: "alice", Harness: "omp", Source: "tool_call", Tool: "bash",
		Target: "build", ArgsDigest: "sha256:a", MaxCost: 400, PolicyVersion: "1",
	})
	if err != nil || allowed.Decision != protocol.DecisionAllow {
		t.Fatalf("Authorize: %v %+v", err, allowed)
	}

	settlePayload := protocol.SettlePayload{
		ReservationID:  "action-1",
		ObservedUsage:  250,
		Outcome:        protocol.OutcomeCompleted,
		EvidenceDigest: "sha256:usage",
	}
	settled, err := ensure.Client.Settle(ctx, "settle-key-1", settlePayload)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !settled.Settled || settled.Charged != 250 || settled.Released != 400 {
		t.Fatalf("settled = %+v", settled)
	}
	if settled.Receipt.Fine != 0 {
		t.Fatalf("a settlement finalized a fine: %+v", settled.Receipt)
	}
	if h.eventCount("settle:action-1") != 1 {
		t.Fatalf("settle events = %d, want exactly 1", h.eventCount("settle:action-1"))
	}
	account, err := h.op.Ledger().GetAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account.Balance != 750 || account.Held != 0 {
		t.Fatalf("balance=%d held=%d, want 750/0", account.Balance, account.Held)
	}

	// A restart must not duplicate the settlement.
	if ensure.Stop == nil {
		t.Fatal("the test harness did not return a stop function")
	}
	if err := ensure.Stop(); err != nil {
		t.Fatalf("stop supervisor: %v", err)
	}
	restarted, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("restart Ensure: %v", err)
	}
	if !restarted.Created {
		t.Fatal("restart reused a stopped supervisor")
	}
	replayed, err := restarted.Client.Settle(ctx, "settle-key-1", settlePayload)
	if err != nil {
		t.Fatalf("replayed settle after restart: %v", err)
	}
	if replayed.Receipt.EventID != "settle:action-1" {
		t.Fatalf("receipt = %+v", replayed.Receipt)
	}
	if h.eventCount("settle:action-1") != 1 {
		t.Fatalf("settle events after restart = %d, want still 1", h.eventCount("settle:action-1"))
	}
	account, _ = h.op.Ledger().GetAccount(ctx, "alice")
	if account.Balance != 750 {
		t.Fatalf("the restart replay charged again: balance = %d, want 750", account.Balance)
	}
}

func TestSettleHonestFailureChargesCostWithZeroFine(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.fund("alice", 1_000)
	if _, err := ensure.Client.Authorize(ctx, "action-1", protocol.AuthorizePayload{
		AccountID: "alice", MaxCost: 400, PolicyVersion: "1",
	}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	// An honest failure needs no evidence digest and is charged its operating
	// cost with zero fine and no escalation.
	settled, err := ensure.Client.Settle(ctx, "settle-key-1", protocol.SettlePayload{
		ReservationID: "action-1", ObservedUsage: 4, Outcome: protocol.OutcomeError,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settled.Receipt.Class != string(ledger.ClassOrdinaryFailure) {
		t.Fatalf("class = %s, want ordinary_failure", settled.Receipt.Class)
	}
	if settled.Receipt.Fine != 0 || settled.Receipt.Role != ledger.RoleActive || settled.Receipt.Warnings != 0 {
		t.Fatalf("receipt = %+v, want zero fine and no escalation", settled.Receipt)
	}
	lessons, err := h.op.Ledger().Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("an honest failure wrote %d canonical lessons", len(lessons))
	}

	// A success claim without evidence is refused.
	if _, err := ensure.Client.Authorize(ctx, "action-2", protocol.AuthorizePayload{
		AccountID: "alice", MaxCost: 100, PolicyVersion: "1",
	}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if _, err := ensure.Client.Settle(ctx, "settle-key-2", protocol.SettlePayload{
		ReservationID: "action-2", ObservedUsage: 10, Outcome: protocol.OutcomeCompleted,
	}); err == nil {
		t.Fatal("a success claim without evidence was accepted")
	} else if got := protocol.CodeOf(err); got != protocol.CodeEvidenceRequired {
		t.Fatalf("code = %s, want evidence_required", got)
	}

	// Usage above the reservation is a hard error and leaves it untouched.
	_, overErr := ensure.Client.Settle(ctx, "settle-key-3", protocol.SettlePayload{
		ReservationID: "action-2", ObservedUsage: 101, Outcome: protocol.OutcomeError,
	})
	if got := protocol.CodeOf(overErr); got != protocol.CodeUsageExceedsReservation {
		t.Fatalf("code = %s, want usage_exceeds_reservation", got)
	}
	reservation, err := h.op.Ledger().Reservation(ctx, "action-2")
	if err != nil {
		t.Fatalf("Reservation: %v", err)
	}
	if reservation.State != ledger.StateHeld {
		t.Fatalf("state = %s, want the reservation untouched", reservation.State)
	}
}

// --- protocol version --------------------------------------------------------

func TestProtocolVersionMismatchIsRejectedWithATypedError(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	if _, err := Ensure(ctx, h.cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	for _, request := range []protocol.Request{
		{Protocol: "bicz.adapter/2", Version: 2, Op: protocol.MethodStatus, RequestID: "req-1"},
		{Protocol: protocol.ID, Version: 2, Op: protocol.MethodStatus, RequestID: "req-2"},
		{Protocol: "bicz.adapter/0", Version: 0, Op: protocol.MethodStatus, RequestID: "req-3"},
	} {
		conn, err := dial(h.cfg.SocketPath)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if err := json.NewEncoder(conn).Encode(request); err != nil {
			t.Fatalf("encode: %v", err)
		}
		_, err = protocol.DecodeResponse(conn)
		conn.Close()
		if err == nil {
			t.Fatalf("request %+v was accepted", request)
		}
		if got := protocol.CodeOf(err); got != protocol.CodeProtocolUnsupported {
			t.Fatalf("code = %s, want protocol_unsupported (%v)", got, err)
		}
		// A mismatch is never metered.
		if err := json.Unmarshal([]byte(`{}`), &struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := h.op.Ledger().Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, event := range events {
		if strings.HasPrefix(event.ID, "req-") {
			t.Fatalf("a rejected request appended event %s", event.ID)
		}
	}
}

func TestMalformedRequestGetsATypedAnswer(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	if _, err := Ensure(ctx, h.cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	conn, err := dial(h.cfg.SocketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = protocol.DecodeResponse(conn)
	if got := protocol.CodeOf(err); got != protocol.CodeMalformedRequest {
		t.Fatalf("code = %s, want malformed_request (%v)", got, err)
	}

	// A request with an authority field is refused with untrusted_authority.
	conn2, err := dial(h.cfg.SocketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close()
	body := []byte(`{"protocol":"` + protocol.ID + `","version":1,"op":"authorize","request_id":"req-9",` +
		`"idempotency_key":"k","payload":{"account_id":"alice","authority":"operator","max_cost":10}}`)
	if _, err := conn2.Write(append(body, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = protocol.DecodeResponse(conn2)
	if got := protocol.CodeOf(err); got != protocol.CodeUntrustedAuthority {
		t.Fatalf("code = %s, want untrusted_authority (%v)", got, err)
	}
}

// --- operator authority ------------------------------------------------------

func TestStateChangingCommandsRequireANonEmptyReason(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)

	commands := []struct {
		name string
		run  func() error
	}{
		{"open", func() error {
			_, err := h.op.Open(ctx, OpenRequest{Identity: "bob"})
			return err
		}},
		{"reward", func() error {
			_, err := h.op.Reward(ctx, RewardRequest{Identity: "alice", Amount: 100})
			return err
		}},
		{"loss", func() error {
			_, err := h.op.Loss(ctx, LossRequest{Identity: "alice", Rule: "r", Correction: "c", PolicyVersion: "1"})
			return err
		}},
		{"appeal", func() error {
			_, err := h.op.Appeal(ctx, AppealRequest{Identity: "alice", OriginalEventID: "fund:alice", Amount: 1})
			return err
		}},
		{"review", func() error {
			_, err := h.op.Review(ctx, ReviewRequest{Identity: "alice", Verdict: "clean"})
			return err
		}},
		{"reinstate", func() error {
			_, err := h.op.Reinstate(ctx, ReinstateRequest{Identity: "alice"})
			return err
		}},
		{"anchor", func() error {
			_, err := h.op.Anchor(ctx, filepath.Join(t.TempDir(), "anchors.log"), "")
			return err
		}},
		{"price", func() error {
			return h.op.Price(ctx, "9", []byte(`{}`), "")
		}},
	}
	before := h.eventCount("")
	for _, command := range commands {
		err := command.run()
		ensureCode(t, err, protocol.CodeReasonRequired)
		if !strings.Contains(err.Error(), "non-empty reason") {
			t.Fatalf("%s: error does not explain: %v", command.name, err)
		}
	}
	if after := h.eventCount(""); after != before {
		t.Fatalf("a refused command wrote %d events", after-before)
	}
	if account, err := h.op.Ledger().GetAccount(ctx, "bob"); err == nil {
		t.Fatalf("a refused open created an account: %+v", account)
	}
}

func TestSuppliedAuthorityInTheEnvironmentIsRejected(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)

	for _, key := range suppliedAuthorityEnv {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "operator")
			_, err := h.op.Reward(ctx, RewardRequest{
				Identity: "alice", Amount: 100, Reason: "an agent trying to be the operator",
			})
			ensureCode(t, err, protocol.CodeUntrustedAuthority)
		})
	}
	account, err := h.op.Ledger().GetAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account.Balance != 1_000 {
		t.Fatalf("a refused reward credited the account: %d", account.Balance)
	}
}

func TestOperatorReviewRevokesLeaseAndTransitionsRole(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)
	if _, err := h.op.Ledger().OpenTask(ctx, "task-open-1", "task-14", "alice", 500, "quote", "review-1"); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}

	result, err := h.op.Review(ctx, ReviewRequest{
		EventID:  "review-1",
		Identity: "alice",
		Verdict:  "confirmed breach: unsupported completion claim",
		Reason:   "operator adjudication",
		Evidence: "transcript lines 40-52",
		TaskID:   "task-14",
		Role:     ledger.RoleWarned,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !result.TaskRevoked || result.Role != ledger.RoleWarned || result.Warnings != 1 {
		t.Fatalf("review = %+v", result)
	}
	task, err := h.op.Ledger().Task(ctx, "task-14")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if task.State != ledger.TaskRevoked {
		t.Fatalf("lease state = %s, want revoked", task.State)
	}
	// A review finalizes no fine.
	if _, err := h.op.Ledger().RecordLoss(ctx, "loss-x", "alice", 1, "r", "c", "s", "n", "e", "1", ledger.WithDisposition(ledger.DispositionConfirmedBreach)); err == nil {
		t.Fatal("an unpublished policy version was accepted")
	}
	if account, _ := h.op.Ledger().GetAccount(ctx, "alice"); account.Balance != 1_000 {
		t.Fatalf("a review moved money: balance = %d", account.Balance)
	}
}

// --- sealing, anchoring, verification ---------------------------------------

func TestSealAnchorRoundTripAndTamperDetection(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)
	destination := filepath.Join(t.TempDir(), "anchors.log")

	if _, err := h.op.Verify(ctx); err != nil {
		t.Fatalf("verify before anchoring: %v", err)
	}
	anchor, err := h.op.Anchor(ctx, destination, "operator anchoring after review")
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if anchor.Height != 1 {
		t.Fatalf("anchored height %d, want 1", anchor.Height)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	line := fmt.Sprintf("%d %s %s", anchor.Height, anchor.Digest, anchor.At)
	if !strings.Contains(string(content), line) {
		t.Fatalf("destination %q does not contain %q", content, line)
	}

	stats, err := h.op.Verify(ctx)
	if err != nil {
		t.Fatalf("verify after anchoring: %v", err)
	}
	if stats.Height != 1 || stats.Anchors != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	// Tampering is detected and located, not hidden.
	handle := h.op.Ledger().DB()
	if _, err := handle.ExecContext(ctx, `UPDATE events SET reason = 'rewritten' WHERE seq = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	_, err = h.op.Verify(ctx)
	if err == nil {
		t.Fatal("verify accepted a rewritten event")
	}
	if !strings.Contains(err.Error(), "INTEGRITY FAIL") {
		t.Fatalf("verify error is not the contract's line: %v", err)
	}
}

func TestSealLoopSealsAndAnchorsOnItsOwn(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.cfg.SealInterval = 20 * time.Millisecond
	h.cfg.AnchorDestination = filepath.Join(t.TempDir(), "anchors.log")
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer ensure.Stop() //nolint:errcheck // test cleanup
	h.fund("alice", 1_000)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := h.op.Ledger().ChainStats(ctx)
		if err == nil && stats.Height >= 1 && stats.Anchors >= 1 {
			if _, err := h.op.Verify(ctx); err != nil {
				t.Fatalf("verify after the seal loop: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	stats, _ := h.op.Ledger().ChainStats(ctx)
	t.Fatalf("the seal loop did not seal and anchor: %+v", stats)
}

func TestSocketFileIsOwnerOnly(t *testing.T) {
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	if _, err := Ensure(context.Background(), h.cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(h.cfg.SocketPath)
	if err != nil {
		t.Fatalf("stat endpoint: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("endpoint mode = %o, want 600", mode)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("endpoint is not a socket: %v", info.Mode())
	}
	dir, err := os.Stat(filepath.Dir(h.cfg.SocketPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Fatalf("runtime dir mode = %o, want 700", mode)
	}
}

// --- doctor ------------------------------------------------------------------

func TestDoctorReportsIntegrityAnchoringCoverageAndNonClaims(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)

	report, err := h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.ChainError != nil {
		t.Fatalf("clean ledger reported an inconsistency: %v", report.ChainError)
	}
	if report.AnchorStatus != AnchorStatusUnmanaged {
		t.Fatalf("anchor status = %s, want unmanaged before the first anchor", report.AnchorStatus)
	}
	if report.Daemon.Daemon != protocol.DaemonUnmanaged {
		t.Fatalf("daemon = %s, want unmanaged", report.Daemon.Daemon)
	}
	if report.SchemaVersion < 2 {
		t.Fatalf("schema version = %d, want the lesson-proposal migration applied", report.SchemaVersion)
	}
	if len(report.UnobservedNames) == 0 {
		t.Fatal("doctor named no unobserved path")
	}
	if len(report.Claims) == 0 {
		t.Fatal("doctor stated no non-claims")
	}

	var rendered bytes.Buffer
	report.Render(&rendered)
	text := rendered.String()
	for _, required := range []string{
		"INTEGRITY OK",
		"ANCHOR status=unmanaged",
		"DAEMON status=unmanaged",
		"COVERAGE",
		"unobserved",
		"CLAIMS none of:",
		"no sandbox",
		"no containment",
		"no anti-tamper guarantee",
		"no provider metering",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("doctor output is missing %q:\n%s", required, text)
		}
	}
	// Doctor must not claim any of these.
	for _, forbidden := range []string{"sandboxed", "cryptographically authenticated", "tamper-proof", "enforced isolation"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("doctor output claims %q:\n%s", forbidden, text)
		}
	}

	// With an anchor the status becomes anchored, and an old anchor is stale.
	if _, err := h.op.Anchor(ctx, filepath.Join(t.TempDir(), "anchors.log"), "initial anchor"); err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	report, err = h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.AnchorStatus != AnchorStatusAnchored {
		t.Fatalf("anchor status = %s, want anchored", report.AnchorStatus)
	}
	if report.LastAnchor == nil {
		t.Fatal("doctor did not report the last anchor")
	}
}

func TestDoctorFlagsAnAnchorStaleAfterTheEventCadence(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)
	destination := filepath.Join(t.TempDir(), "anchors.log")
	if _, err := h.op.Anchor(ctx, destination, "initial anchor"); err != nil {
		t.Fatalf("Anchor: %v", err)
	}

	report, err := h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	// Sealing records itself as one `anchor`-kind event after the block's range,
	// so a fresh anchor covers everything except that record.
	if report.AnchorThresholdHit || report.AnchorEventsSince > 1 {
		t.Fatalf("fresh anchor: threshold=%v since=%d, want false/<=1",
			report.AnchorThresholdHit, report.AnchorEventsSince)
	}

	// §3.6 requires an anchor at least once per 1,000 appended events, whichever
	// comes first, so the event count alone must make the anchor stale.
	window := report.AnchorEventWindow
	for i := int64(0); i < window; i++ {
		if _, err := h.op.Reward(ctx, RewardRequest{
			EventID: fmt.Sprintf("cadence-reward-%d", i), Identity: "alice", Amount: 1,
			Reason: "cadence fixture",
		}); err != nil {
			t.Fatalf("reward %d: %v", i, err)
		}
	}
	report, err = h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !report.AnchorThresholdHit {
		t.Fatalf("after %d events the threshold did not fire (since=%d)",
			window, report.AnchorEventsSince)
	}
	if report.AnchorStatus != AnchorStatusStale {
		t.Fatalf("anchor status = %s, want stale-anchor once the event cadence is exceeded", report.AnchorStatus)
	}

	var rendered bytes.Buffer
	report.Render(&rendered)
	if !strings.Contains(rendered.String(), "ANCHOR WARNING") {
		t.Fatalf("doctor did not warn about the anchor cadence:\n%s", rendered.String())
	}

	// Anchoring again restores the cadence; nothing else is remembered.
	if _, err := h.op.Anchor(ctx, destination, "re-anchor after the cadence"); err != nil {
		t.Fatalf("re-Anchor: %v", err)
	}
	report, err = h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.AnchorThresholdHit || report.AnchorStatus != AnchorStatusAnchored {
		t.Fatalf("after re-anchoring: threshold=%v status=%s, want false/anchored",
			report.AnchorThresholdHit, report.AnchorStatus)
	}
}

func TestProjectionOverTheWireFollowsThePressureSettingsFile(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var spawned atomic.Int64
	h.cfg.Spawner = h.spawner(&spawned, false)
	ensure, err := Ensure(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.fund("alice", 1_000)
	if err := h.op.Ledger().PublishPriceSchedule(ctx, "1", ledger.Policy{
		InitialCapital: 1_000_000, RepeatMultipliers: []int64{1, 2, 4}, RepeatCap: 4,
		DefaultRule: ledger.PenaltyRule{Floor: 100, Max: 1_000_000},
	}); err != nil {
		t.Fatalf("PublishPriceSchedule: %v", err)
	}
	if _, err := h.op.Loss(ctx, LossRequest{Identity: "alice", Rule: "r", Correction: "c", PolicyVersion: "1",
		Reason: "confirmed", Evidence: "claimed tests passed", Requested: 100, Disposition: "confirmed_breach"}); err != nil {
		t.Fatalf("Loss: %v", err)
	}
	read := func() (*protocol.Projection, error) {
		projection, err := ensure.Client.Projection(ctx, protocol.ProjectionPayload{AccountID: "alice", Harness: "omp"})
		return &projection, err
	}

	defaulted, err := read()
	if err != nil || defaulted.Stakes != pressure.Stakes(pressure.Default, pressure.Facts{}) {
		t.Fatalf("no settings file must mean the default level: stakes %q, %v", defaulted.Stakes, err)
	}

	if err := pressure.Save(h.cfg.LedgerPath, pressure.Max); err != nil {
		t.Fatal(err)
	}
	loud, err := read()
	if err != nil || !strings.Contains(loud.Stakes, "Standing now: role warned") || !strings.Contains(loud.Penalties[0].Cue, "were taken from you") {
		t.Fatalf("level 10 did not reach the wire without a restart: %+v, %v", loud, err)
	}

	if err := pressure.Save(h.cfg.LedgerPath, pressure.Min); err != nil {
		t.Fatal(err)
	}
	quiet, err := read()
	if err != nil || quiet.Stakes != "" || quiet.Penalties[0].Cue != "" || quiet.Balance != loud.Balance {
		t.Fatalf("level 1 must be silent and change no numbers: %+v, %v", quiet, err)
	}

	if err := os.WriteFile(pressure.SettingsPath(h.cfg.LedgerPath), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil {
		t.Fatal("a malformed settings file must fail the projection, not fall back silently")
	}
}

// The cadence is schedule data, not a constant: a published policy with a short
// window must decide the status (docs/CONTRACTS.md §3.6).
func TestDoctorTakesTheAnchorCadenceFromThePublishedSchedule(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.fund("alice", 1_000)
	if err := h.op.Price(ctx, "1", []byte(`{"initial_capital_mc":1000000,"guard_band_bp":0,`+
		`"repeat_multipliers":[1],"repeat_multiplier_cap":1,`+
		`"anchor_cadence_hours":1,"anchor_event_threshold":3,`+
		`"default_penalty":{"floor_mc":1000,"fraction_bp":0,"lmax_mc":10000},"penalties":{}}`),
		"cadence policy"); err != nil {
		t.Fatalf("Price: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "anchors.log")
	if _, err := h.op.Anchor(ctx, destination, "initial anchor"); err != nil {
		t.Fatalf("Anchor: %v", err)
	}

	report, err := h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.AnchorWindow != time.Hour || report.AnchorEventWindow != 3 {
		t.Fatalf("window=%s/%d, want 1h/3", report.AnchorWindow, report.AnchorEventWindow)
	}
	if report.AnchorThresholdHit {
		t.Fatal("a fresh anchor hit the threshold immediately")
	}

	// Three appended events are due under the published schedule, not a thousand.
	for i := int64(0); i < 3; i++ {
		if _, err := h.op.Reward(ctx, RewardRequest{
			EventID: fmt.Sprintf("cadence-policy-%d", i), Identity: "alice", Amount: 1,
			Reason: "cadence fixture",
		}); err != nil {
			t.Fatalf("reward %d: %v", i, err)
		}
	}
	report, err = h.op.Doctor(ctx, h.cfg)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !report.AnchorThresholdHit || report.AnchorStatus != AnchorStatusStale {
		t.Fatalf("threshold=%v status=%s, want true/stale-anchor under a 3-event window",
			report.AnchorThresholdHit, report.AnchorStatus)
	}
}
