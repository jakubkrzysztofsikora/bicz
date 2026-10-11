package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// Spawner starts a supervisor for cfg and returns a function that stops it.
//
// The default is SpawnProcess. Tests substitute a spawner so they can run a
// supervisor in-process or re-exec the test binary, without changing the
// locking, readiness, and unmanaged semantics under test.
type Spawner func(cfg Config) (stop func() error, err error)

// SpawnProcess is the default Spawner: it re-executes this binary with the
// `serve` subcommand, detached from this process so the supervisor outlives the
// launcher.
func SpawnProcess(cfg Config) (func() error, error) {
	executable, err := executablePath()
	if err != nil {
		return nil, err
	}
	args := []string{
		"serve",
		"--db", cfg.LedgerPath,
		"--socket", cfg.SocketPath,
		"--lock", cfg.LockPath,
		"--seal-interval", cfg.SealInterval.String(),
	}
	if cfg.AnchorDestination != "" {
		args = append(args, "--anchor", cfg.AnchorDestination)
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = detachSysProcAttr()
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("daemon: start supervisor: %w", err)
	}
	process := command.Process
	return func() error {
		if process == nil {
			return nil
		}
		if err := process.Kill(); err != nil && !errors.Is(err, osErrProcessDone) {
			return fmt.Errorf("daemon: stop supervisor: %w", err)
		}
		return nil
	}, nil
}

// Client is a protocol client. One call opens one connection, writes one
// request, reads one response, and closes: there is no session state to leak and
// no framing to desynchronize.
type Client struct {
	// SocketPath is the endpoint to connect to.
	SocketPath string
	// Timeout bounds a single call. Zero means no deadline. It is ignored on
	// transports that cannot honour deadlines (Windows named pipes).
	Timeout time.Duration
}

// NewClient builds a client for an endpoint.
func NewClient(endpoint string) *Client {
	return &Client{SocketPath: endpoint, Timeout: 10 * time.Second}
}

// EnsureResult is the outcome of Ensure.
type EnsureResult struct {
	// Client is ready to use.
	Client *Client
	// Created reports whether this call started a supervisor. When false, a
	// supervisor was already recording and was reused, so no second instance and
	// no duplicate ledger event can exist.
	Created bool
	// Stop terminates a supervisor this call created. It is nil when an existing
	// supervisor was reused. Ordinary callers must leave it alone: a supervisor
	// is meant to outlive the process that started it. Tests use it.
	Stop func() error
}

// Ensure makes sure a supervisor is recording for cfg's ledger, starting one if
// necessary, and returns a client.
//
// Exactly-one semantics come from the OS-released process lock, not from the
// endpoint file: the first launcher to take the lock spawns the supervisor, and
// every other launcher waits for that endpoint instead of starting a second one.
// Because a mutating call is keyed on its idempotency key, even a spurious
// duplicate request cannot duplicate a ledger event.
//
// When no supervisor can be reached or started, Ensure returns an error carrying
// daemon_unavailable. It never reports success without a recording supervisor.
func Ensure(ctx context.Context, cfg Config) (EnsureResult, error) {
	resolved, err := cfg.withDefaults()
	if err != nil {
		return EnsureResult{}, err
	}
	client := NewClient(resolved.SocketPath)
	if err := probe(resolved.SocketPath); err == nil {
		return EnsureResult{Client: client}, nil
	}
	if _, err := PrepareRuntimeDir(); err != nil {
		return EnsureResult{}, unmanaged(err)
	}

	// The launch lock serializes LAUNCHERS; the supervisor holds the OWNER lock.
	// Keeping them separate is what lets a launcher start a supervisor without
	// deadlocking the process it just spawned.
	launchPath := launchLockPath(resolved.LockPath)
	deadline := time.Now().Add(resolved.ReadyTimeout)
	var lastErr error
	for {
		if err := probe(resolved.SocketPath); err == nil {
			return EnsureResult{Client: client}, nil
		}
		lock, lockErr := acquireFileLock(launchPath)
		if lockErr != nil {
			if !errors.Is(lockErr, ErrAlreadyRunning) {
				return EnsureResult{}, unmanaged(lockErr)
			}
			// Another launcher is bringing the supervisor up: wait for its
			// endpoint instead of starting a competitor.
			lastErr = fmt.Errorf("another launcher held the launch lock for this ledger for the whole readiness window (%v)", lockErr)
			if time.Now().After(deadline) {
				return EnsureResult{}, unmanaged(lastErr)
			}
			select {
			case <-ctx.Done():
				return EnsureResult{}, unmanaged(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
			continue
		}

		// We hold the launch lock: re-probe, because the previous holder may
		// have succeeded while we were retrying.
		if err := probe(resolved.SocketPath); err == nil {
			lock.release() //nolint:errcheck // released immediately after a successful probe
			return EnsureResult{Client: client}, nil
		}
		stop, spawnErr := resolved.Spawner(resolved)
		if spawnErr != nil {
			lock.release() //nolint:errcheck // failure path
			return EnsureResult{}, unmanaged(spawnErr)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = resolved.ReadyTimeout
		}
		if waitErr := waitReady(ctx, resolved.SocketPath, remaining); waitErr != nil {
			lock.release() //nolint:errcheck // failure path
			if stop != nil {
				stop() //nolint:errcheck // the supervisor never became reachable
			}
			return EnsureResult{}, unmanaged(waitErr)
		}
		lock.release() //nolint:errcheck // the supervisor owns the endpoint now
		return EnsureResult{Client: client, Created: true, Stop: stop}, nil
	}
}

// Status reports the supervisor state for cfg WITHOUT starting anything.
//
// It never returns an error: the caller always learns the truth. A missing
// supervisor yields DaemonUnmanaged with a note explaining that recording is not
// happening, together with the full coverage table so the unobserved paths are
// still disclosed.
func Status(ctx context.Context, cfg Config) protocol.StatusState {
	coverage := protocol.CoverageFor("")
	resolved, err := cfg.withDefaults()
	if err != nil {
		return protocol.StatusState{
			Daemon:   protocol.DaemonUnmanaged,
			Coverage: coverage,
			Note:     "no usable supervisor configuration: " + err.Error(),
		}
	}
	client := NewClient(resolved.SocketPath)
	state, err := client.Status(ctx, protocol.StatusPayload{})
	if err != nil {
		return protocol.StatusState{
			Daemon:     protocol.DaemonUnmanaged,
			Coverage:   coverage,
			Socket:     resolved.SocketPath,
			LedgerPath: resolved.LedgerPath,
			Note: "no supervisor is reachable at " + resolved.SocketPath + " (" + err.Error() +
				"); recording is NOT happening and nothing may be assumed to have been metered",
		}
	}
	return state
}

// unmanaged wraps a startup failure as an explicit daemon_unavailable error.
func unmanaged(cause error) error {
	return protocol.Errorf(protocol.CodeDaemonUnavailable,
		"no supervisor is recording for this ledger and one could not be started (%v); recording is suspended, so no state may be assumed to have been metered",
		cause)
}

// probe reports whether something is listening and answering on the endpoint.
func probe(endpoint string) error {
	conn, err := dial(endpoint)
	if err != nil {
		return err
	}
	return conn.Close()
}

// waitReady waits for the endpoint to accept a connection.
func waitReady(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := probe(endpoint); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// call performs one request/response round trip.
//
// A denial is NOT an error: it is a normal response carrying a decision and a
// reason code, and the method helpers return it as data. The error return is
// reserved for transport failures and protocol-level failures, so a caller can
// never confuse "the daemon said no" with "the daemon said nothing".
func (c *Client) call(ctx context.Context, method protocol.Method, idempotencyKey string, payload any) (protocol.Response, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Response{}, err
	}
	body, err := protocol.MarshalPayload(payload)
	if err != nil {
		return protocol.Response{}, err
	}
	request := protocol.Request{
		Protocol:       protocol.ID,
		Version:        protocol.Version,
		Op:             method,
		RequestID:      newRequestID(),
		IdempotencyKey: idempotencyKey,
		Payload:        body,
	}
	conn, err := dial(c.SocketPath)
	if err != nil {
		return protocol.Response{}, unmanaged(err)
	}
	defer conn.Close()
	if c.Timeout > 0 {
		// Best effort: a transport that cannot honour deadlines ignores this.
		_ = conn.SetDeadline(time.Now().Add(c.Timeout))
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return protocol.Response{}, protocol.Errorf(protocol.CodeDaemonUnavailable, "send request: %v", err)
	}
	response, err := protocol.DecodeResponse(conn)
	if err != nil {
		return response, err
	}
	return response, nil
}

// Authorize asks for a bounded reservation.
func (c *Client) Authorize(ctx context.Context, idempotencyKey string, payload protocol.AuthorizePayload) (protocol.AuthorizeResult, error) {
	response, err := c.call(ctx, protocol.MethodAuthorize, idempotencyKey, payload)
	if err != nil {
		return protocol.AuthorizeResult{}, err
	}
	result := protocol.AuthorizeResult{
		Decision:      response.Decision,
		Reason:        response.Reason,
		Notice:        response.Notice,
		ReservationID: response.ReservationID,
	}
	if len(response.State) > 0 {
		var state protocol.AuthorizeState
		if err := json.Unmarshal(response.State, &state); err != nil {
			return result, protocol.Errorf(protocol.CodeMalformedRequest, "authorize state: %v", err)
		}
		result.Projection = state.Projection
		result.UnobservedPaths = state.UnobservedPaths
		result.CoverageNote = state.CoverageNote
	}
	return result, nil
}

// Settle reports observed usage for a reservation.
func (c *Client) Settle(ctx context.Context, idempotencyKey string, payload protocol.SettlePayload) (protocol.SettleState, error) {
	response, err := c.call(ctx, protocol.MethodSettle, idempotencyKey, payload)
	if err != nil {
		return protocol.SettleState{}, err
	}
	if response.Decision == protocol.DecisionDeny {
		return protocol.SettleState{}, protocol.Errorf(response.Reason, "%s", response.Notice)
	}
	var state protocol.SettleState
	if err := json.Unmarshal(response.State, &state); err != nil {
		return protocol.SettleState{}, protocol.Errorf(protocol.CodeMalformedRequest, "settle state: %v", err)
	}
	return state, nil
}

// Projection reads the read-only context block.
func (c *Client) Projection(ctx context.Context, payload protocol.ProjectionPayload) (protocol.Projection, error) {
	response, err := c.call(ctx, protocol.MethodProjection, "", payload)
	if err != nil {
		return protocol.Projection{}, err
	}
	if response.Decision == protocol.DecisionDeny {
		return protocol.Projection{}, protocol.Errorf(response.Reason, "%s", response.Notice)
	}
	var state protocol.ProjectionState
	if err := json.Unmarshal(response.State, &state); err != nil {
		return protocol.Projection{}, protocol.Errorf(protocol.CodeMalformedRequest, "projection state: %v", err)
	}
	return state.Projection, nil
}

// Status reads the supervisor's recording state and the coverage table.
func (c *Client) Status(ctx context.Context, payload protocol.StatusPayload) (protocol.StatusState, error) {
	response, err := c.call(ctx, protocol.MethodStatus, "", payload)
	if err != nil {
		return protocol.StatusState{}, err
	}
	var state protocol.StatusState
	if err := json.Unmarshal(response.State, &state); err != nil {
		return protocol.StatusState{}, protocol.Errorf(protocol.CodeMalformedRequest, "status state: %v", err)
	}
	return state, nil
}

// ProposeLesson stores a non-canonical lesson proposal.
func (c *Client) ProposeLesson(ctx context.Context, idempotencyKey string, payload protocol.LessonProposePayload) (protocol.LessonProposeState, error) {
	response, err := c.call(ctx, protocol.MethodLessonPropose, idempotencyKey, payload)
	if err != nil {
		return protocol.LessonProposeState{}, err
	}
	if response.Decision == protocol.DecisionDeny {
		return protocol.LessonProposeState{}, protocol.Errorf(response.Reason, "%s", response.Notice)
	}
	var state protocol.LessonProposeState
	if err := json.Unmarshal(response.State, &state); err != nil {
		return protocol.LessonProposeState{}, protocol.Errorf(protocol.CodeMalformedRequest, "lesson.propose state: %v", err)
	}
	return state, nil
}

var requestCounter atomic.Uint64

// newRequestID is a correlation id. It carries no authority and is never used as
// an idempotency key.
func newRequestID() string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("req-%d", requestCounter.Add(1))
	}
	return "req-" + hex.EncodeToString(suffix[:])
}
