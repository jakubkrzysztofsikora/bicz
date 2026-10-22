// Package adapter is the harness-neutral client and decision helpers shared by
// every per-harness adapter (the OMP extension in adapters/omp, and future
// Claude Code / Codex / OpenCode hooks).
//
// The package deliberately depends on nothing but the standard library and
// internal/protocol: an adapter must never transitively pull in the ledger,
// SQLite, or the daemon (see the package comment in internal/protocol). All
// money is integer microcredits; floats and booleans are rejected before they
// can reach a money path.
//
// The one non-negotiable rule of this package: there is no silent success. A
// supervisor that cannot be reached is surfaced as UnmanagedError (the wire
// code daemon_unavailable, docs/CONTRACTS.md §7: "adapter reports unmanaged;
// recording suspended, disclosed in doctor; never a fabricated state"). A
// denial is data, not an error. A path outside adapter coverage is the
// unobserved decision, never an allow.
package adapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// DefaultTimeout bounds a single protocol call (connect + request + response).
// The OMP extension may shorten it; a supervisor on the local socket answers in
// milliseconds.
const DefaultTimeout = 10 * time.Second

// UnmanagedError is the typed "no supervisor is recording" outcome
// (docs/CONTRACTS.md §5.5 daemon_unavailable). It is reported by the adapter,
// never by the daemon: the caller MUST surface it and suspend recording, and
// must never fabricate a state in its place.
type UnmanagedError struct {
	// Endpoint is the socket that was unreachable.
	Endpoint string
	// Cause is the underlying transport failure.
	Cause error
}

func (e *UnmanagedError) Error() string {
	return fmt.Sprintf("bicz adapter: no supervisor is recording at %s (%v); recording is suspended and no state may be assumed to have been metered",
		e.Endpoint, e.Cause)
}

func (e *UnmanagedError) Unwrap() error { return e.Cause }

// IsUnmanaged reports whether err is the explicit unmanaged outcome. A caller
// that gets true must disclose "recording unavailable" instead of pretending a
// charge was recorded.
func IsUnmanaged(err error) bool {
	if err == nil {
		return false
	}
	var unmanaged *UnmanagedError
	if errors.As(err, &unmanaged) {
		return true
	}
	var protocolErr *protocol.ProtocolError
	if errors.As(err, &protocolErr) && protocolErr.Code == protocol.CodeDaemonUnavailable {
		return true
	}
	return false
}

// Client is a protocol client for the supervisor socket.
//
// One call opens one connection, writes one request, reads one response, and
// closes: there is no session state to leak and no framing to desynchronize
// (docs/CONTRACTS.md §5.1). Mutating calls carry the caller's idempotency key;
// the daemon never invents one.
type Client struct {
	// Endpoint is the Unix socket the supervisor listens on.
	Endpoint string
	// Timeout bounds one call, connect included. Zero means no deadline (not
	// recommended for an adapter that sits in the harness call path).
	Timeout time.Duration
}

// NewClient builds a client for a socket endpoint.
func NewClient(endpoint string) *Client {
	return &Client{Endpoint: endpoint, Timeout: DefaultTimeout}
}

// Authorize asks for a bounded reservation before an action runs. The denial is
// returned as data (Decision/Reason), never as an error; only transport and
// protocol-level failures are errors, and an unreachable supervisor is an
// UnmanagedError.
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

// Settle reports observed usage for a reservation exactly once. A settlement
// denial (unknown reservation, usage over the reserved bound, replay conflict)
// is returned as an error carrying the typed protocol code.
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

// Autoreward records a bounded microreward to replenish spent credits.
func (c *Client) Autoreward(ctx context.Context, idempotencyKey string, payload protocol.AutorewardPayload) (protocol.AutorewardState, error) {
	response, err := c.call(ctx, protocol.MethodAutoreward, idempotencyKey, payload)
	if err != nil {
		return protocol.AutorewardState{}, err
	}
	if response.Decision == protocol.DecisionDeny {
		return protocol.AutorewardState{}, protocol.Errorf(response.Reason, "%s", response.Notice)
	}
	var state protocol.AutorewardState
	if err := json.Unmarshal(response.State, &state); err != nil {
		return protocol.AutorewardState{}, protocol.Errorf(protocol.CodeMalformedRequest, "autoreward state: %v", err)
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

// Status reads the supervisor's recording state and the disclosed coverage
// table.
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

// ProposeLesson stores a non-canonical lesson proposal for operator review. It
// never applies anything; only an operator resolution makes a proposal
// canonical (docs/CONTRACTS.md §5.4).
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

// call performs one request/response round trip over the socket.
//
// A denial is NOT an error: it is a normal response carrying a decision and a
// reason code. The error return is reserved for transport and protocol-level
// failures, so a caller can never confuse "the daemon said no" with "the daemon
// said nothing". Every unreachable-supervisor failure is wrapped as
// UnmanagedError.
func (c *Client) call(ctx context.Context, method protocol.Method, idempotencyKey string, payload any) (protocol.Response, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Response{}, &UnmanagedError{Endpoint: c.Endpoint, Cause: ctx.Err()}
	}
	// A mutating op without a key cannot be replayed safely, so it is refused
	// here rather than after a round trip: the caller gets the distinct
	// missing_idempotency_key code instead of a generic malformed_request from
	// the supervisor (docs/CONTRACTS.md §5, §5.5).
	if method.Mutating() && idempotencyKey == "" {
		return protocol.Response{}, protocol.Errorf(protocol.CodeMissingIdempotencyKey,
			"op %q is mutating and requires an idempotency key", method)
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

	var conn net.Conn
	dialTimeout := c.Timeout
	if dialTimeout <= 0 {
		conn, err = net.Dial("unix", c.Endpoint)
	} else {
		conn, err = net.DialTimeout("unix", c.Endpoint, dialTimeout)
	}
	if err != nil {
		return protocol.Response{}, &UnmanagedError{Endpoint: c.Endpoint, Cause: err}
	}
	defer conn.Close()
	if c.Timeout > 0 {
		if err := conn.SetDeadline(time.Now().Add(c.Timeout)); err != nil {
			return protocol.Response{}, &UnmanagedError{Endpoint: c.Endpoint, Cause: err}
		}
	}

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(request); err != nil {
		return protocol.Response{}, &UnmanagedError{Endpoint: c.Endpoint, Cause: fmt.Errorf("send request: %w", err)}
	}
	response, err := protocol.DecodeResponse(conn)
	if err != nil {
		if IsUnmanaged(err) {
			return response, &UnmanagedError{Endpoint: c.Endpoint, Cause: err}
		}
		return response, err
	}
	return response, nil
}

var requestCounter atomic.Uint64

// newRequestID is a correlation id. It carries no authority (docs/CONTRACTS.md
// §4) and is never used as an idempotency key.
func newRequestID() string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("req-%d", requestCounter.Add(1))
	}
	return "req-" + hex.EncodeToString(suffix[:])
}
