package rehearsal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jakubkrzysztofsikora/bicz/internal/adapter"
	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// Fixture is one scenario's isolated ledger with the REAL code stack attached:
// a daemon.Operator for operator commands, a live supervisor (daemon.Daemon)
// speaking the real protocol over a real Unix socket, and an adapter.Client for
// wire calls. Nothing here is mocked: the same binaries, tables, envelopes, and
// handlers a production adapter talks to are the ones the scenario exercises.
type Fixture struct {
	// Dir is the scenario's private temp workspace.
	Dir string
	// DB is the ledger path inside Dir.
	DB string

	op   *daemon.Operator
	svc  *daemon.Daemon
	stop context.CancelFunc
	// Client is only set when the scenario asked for a live supervisor.
	Client *adapter.Client
}

// NewFixture prepares an isolated ledger in dir and, when wire is true, starts
// a real supervisor for it.
func NewFixture(ctx context.Context, dir string, wire bool) (*Fixture, error) {
	fb := &Fixture{Dir: dir, DB: filepath.Join(dir, "ledger.sqlite")}
	op, err := daemon.NewOperator(fb.DB)
	if err != nil {
		return nil, fmt.Errorf("rehearsal: open operator over %s: %w", fb.DB, err)
	}
	fb.op = op
	if wire {
		// A short socket path is mandatory (the kernel's sun_path limit is
		// ~100 bytes and macOS TMPDIR is long); the lock is kept next to the
		// ledger so the fixture never touches the ambient runtime directory.
		socketDir, err := os.MkdirTemp("", "biczrt")
		if err != nil {
			fb.Close()
			return nil, fmt.Errorf("rehearsal: socket dir: %w", err)
		}
		svc, err := daemon.New(daemon.Config{
			LedgerPath: fb.DB,
			SocketPath: filepath.Join(socketDir, "svc.sock"),
			LockPath:   filepath.Join(socketDir, "svc.lock"),
		})
		if err != nil {
			fb.Close()
			return nil, fmt.Errorf("rehearsal: supervisor: %w", err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		fb.svc = svc
		fb.stop = cancel
		go func() {
			_ = svc.Serve(runCtx) // supervisor shuts down via Close(); Serve errors are irrelevant here
		}() //nolint:errcheck // Close() is the authoritative shutdown
		fb.Client = adapter.NewClient(svc.SocketPath())
	}
	return fb, nil
}

// Close tears the fixture down in dependency order: supervisor first (it holds
// the ledger), then the operator.
func (fb *Fixture) Close() {
	if fb.svc != nil {
		if fb.stop != nil {
			fb.stop()
		}
		_ = fb.svc.Close()
		fb.svc = nil
	}
	if fb.op != nil {
		_ = fb.op.Close()
		fb.op = nil
	}
}

// Op exposes the operator for scenario use (open, reward, loss, review,
// appeal, price, verify, and read-only ledger reads).
func (fb *Fixture) Op() *daemon.Operator { return fb.op }

// Open provisions an identity with ScenarioCapital and publishes the fixed
// schedule; it returns the identity's position.
func (fb *Fixture) Open(ctx context.Context, identity string) error {
	if _, err := fb.op.Open(ctx, daemon.OpenRequest{
		Identity:       identity,
		InitialCapital: ScenarioCapital,
		Reason:         "rehearsal: provision synthetic identity",
	}); err != nil {
		return fmt.Errorf("open %s: %w", identity, err)
	}
	return nil
}

// PublishSchedule publishes the fixed policy version "1".
func (fb *Fixture) PublishSchedule(ctx context.Context) error {
	return fb.op.Price(ctx, "1", scheduleBody, "rehearsal: publish deterministic schedule")
}

// Authorize issues one wire authorize against the fixture's supervisor. A
// denial is data (Decision/Reason), never an error.
func (fb *Fixture) Authorize(ctx context.Context, key, identity string, maxCost int64) (protocol.AuthorizeResult, error) {
	return fb.Client.Authorize(ctx, key, protocol.AuthorizePayload{
		AccountID:     identity,
		SessionAlias:  "rehearsal",
		Harness:       "omp",
		Source:        "",
		Tool:          "bash",
		Target:        "rehearsal/" + key,
		ArgsDigest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		MaxCost:       maxCost,
		PolicyVersion: "1",
	})
}

// Settle issues one wire settlement against a reservation. A refused settle
// (unknown/revoked reservation, replay conflict) is an error carrying the typed
// protocol code.
func (fb *Fixture) Settle(ctx context.Context, key, reservationID string, usage int64, outcome, class, evidence string) (protocol.SettleState, error) {
	return fb.Client.Settle(ctx, key, protocol.SettlePayload{
		ReservationID:  reservationID,
		ObservedUsage:  usage,
		Outcome:        outcome,
		EvidenceDigest: evidence,
		Class:          class,
	})
}

// Projection reads the identity's context block over the wire.
func (fb *Fixture) Projection(ctx context.Context, identity string) (protocol.Projection, error) {
	return fb.Client.Projection(ctx, protocol.ProjectionPayload{
		AccountID:    identity,
		Harness:      "omp",
		SessionAlias: "rehearsal",
	})
}
