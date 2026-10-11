package adapter

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// fakeDaemon speaks the protocol well enough to exercise the client: it reads
// one request per connection and answers with the canned response.
type fakeDaemon struct {
	t        *testing.T
	endpoint string
	respond  func(protocol.Request) protocol.Response
}

func startFakeDaemon(t *testing.T, respond func(protocol.Request) protocol.Response) *fakeDaemon {
	t.Helper()
	// The Unix sun_path limit is ~100 bytes and macOS TMPDIR is long; use a
	// short temp dir so the bind succeeds (the real daemon guards the same
	// limit, internal/daemon/paths.go).
	dir, err := os.MkdirTemp("", "bicz")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	endpoint := filepath.Join(dir, "b.sock")
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	daemon := &fakeDaemon{t: t, endpoint: endpoint, respond: respond}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				request, err := protocol.DecodeRequest(conn)
				if err != nil {
					daemon.t.Errorf("fake daemon decode: %v", err)
					return
				}
				if err := protocol.EncodeResponse(conn, daemon.respond(request)); err != nil {
					daemon.t.Errorf("fake daemon encode: %v", err)
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return daemon
}

func authorizeResponse(decision protocol.Decision) protocol.Response {
	response := protocol.Response{
		Decision: decision,
		Reason:   "",
	}
	if decision == protocol.DecisionAllow {
		reservation := "res-1"
		response.ReservationID = &reservation
	}
	state := protocol.AuthorizeState{
		Projection: &protocol.Projection{
			AccountID:       "alice",
			Balance:         1_000_000,
			PolicyVersion:   "1",
			OutstandingLoss: 0,
		},
	}
	encoded, err := protocol.MarshalState(state)
	if err != nil {
		panic(err)
	}
	response.State = encoded
	return response
}

func TestClientAuthorizeAllowAndDenyAreData(t *testing.T) {
	daemon := startFakeDaemon(t, func(request protocol.Request) protocol.Response {
		if request.Op != protocol.MethodAuthorize {
			t.Errorf("got op %q, want authorize", request.Op)
		}
		if request.IdempotencyKey == "" {
			t.Error("mutating authorize must carry an idempotency key")
		}
		return authorizeResponse(protocol.DecisionAllow)
	})
	client := NewClient(daemon.endpoint)
	client.Timeout = 2 * time.Second

	result, err := client.Authorize(context.Background(), "key-1", protocol.AuthorizePayload{
		AccountID: "alice",
		MaxCost:   100_000,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if result.Decision != protocol.DecisionAllow || result.ReservationID == nil || *result.ReservationID != "res-1" {
		t.Fatalf("allow result = %+v", result)
	}
	if result.Projection == nil || result.Projection.AccountID != "alice" {
		t.Fatalf("allow must carry the projection, got %+v", result.Projection)
	}

	deny := startFakeDaemon(t, func(request protocol.Request) protocol.Response {
		response := protocol.Response{Decision: protocol.DecisionDeny, Reason: protocol.CodeInsufficientCredit}
		response.Notice = "balance 0 cannot fund max_cost 100000"
		return response
	})
	result, err = NewClient(deny.endpoint).Authorize(context.Background(), "key-2", protocol.AuthorizePayload{
		AccountID: "alice",
		MaxCost:   100_000,
	})
	if err != nil {
		t.Fatalf("a denial must be data, not an error: %v", err)
	}
	if result.Decision != protocol.DecisionDeny || result.Reason != protocol.CodeInsufficientCredit {
		t.Fatalf("deny result = %+v", result)
	}
}

func TestClientUnobservedDecisionSurfaces(t *testing.T) {
	daemon := startFakeDaemon(t, func(request protocol.Request) protocol.Response {
		response := protocol.Response{Decision: protocol.DecisionUnobserved}
		response.Notice = "unobserved: no authorization granted and no spend metered for omp bash"
		state := protocol.AuthorizeState{UnobservedPaths: []string{"omp bash"}}
		encoded, _ := protocol.MarshalState(state)
		response.State = encoded
		return response
	})
	result, err := NewClient(daemon.endpoint).Authorize(context.Background(), "key-3", protocol.AuthorizePayload{
		AccountID: "alice",
		MaxCost:   100_000,
	})
	if err != nil {
		t.Fatalf("unobserved is a first-class outcome, not an error: %v", err)
	}
	if result.Decision != protocol.DecisionUnobserved || len(result.UnobservedPaths) != 1 {
		t.Fatalf("unobserved result = %+v", result)
	}
}

func TestClientSettleAndProjection(t *testing.T) {
	daemon := startFakeDaemon(t, func(request protocol.Request) protocol.Response {
		switch request.Op {
		case protocol.MethodSettle:
			if request.IdempotencyKey == "" {
				t.Error("settle must carry an idempotency key")
			}
			var payload protocol.SettlePayload
			if err := json.Unmarshal(request.Payload, &payload); err != nil {
				t.Errorf("settle payload: %v", err)
			}
			state := protocol.SettleState{
				Settled:  true,
				Charged:  payload.ObservedUsage,
				Released: 100_000,
				Receipt: protocol.Receipt{
					EventID:       "settle:" + payload.ReservationID,
					ReservationID: payload.ReservationID,
					AccountID:     "alice",
					Class:         "ordinary_failure",
					Charged:       payload.ObservedUsage,
					Fine:          0,
				},
			}
			encoded, _ := protocol.MarshalState(state)
			response := protocol.Response{Decision: protocol.DecisionAllow, State: encoded}
			response.Reason = protocol.CodeDuplicateKeyOK
			return response
		case protocol.MethodProjection:
			state := protocol.ProjectionState{Projection: protocol.Projection{AccountID: "alice", Balance: 900_000}}
			encoded, _ := protocol.MarshalState(state)
			return protocol.Response{State: encoded}
		default:
			t.Errorf("unexpected op %q", request.Op)
			return protocol.Response{}
		}
	})
	client := NewClient(daemon.endpoint)
	settled, err := client.Settle(context.Background(), "key-settle", protocol.SettlePayload{
		ReservationID: "key-auth",
		ObservedUsage: 100_000,
		Outcome:       OutcomeError,
		Class:         ClassOrdinaryFailure,
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !settled.Settled || settled.Charged != 100_000 {
		t.Fatalf("settle state = %+v", settled)
	}

	projection, err := client.Projection(context.Background(), protocol.ProjectionPayload{AccountID: "alice"})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if projection.AccountID != "alice" || projection.Balance != 900_000 {
		t.Fatalf("projection = %+v", projection)
	}
}

func TestClientUnreachableSupervisorIsUnmanagedNotSilent(t *testing.T) {
	// A path in a directory that cannot exist: the dial must fail and the
	// failure must be the explicit unmanaged outcome.
	client := NewClient(filepath.Join(t.TempDir(), "no-such-socket.sock"))
	client.Timeout = 500 * time.Millisecond
	_, err := client.Projection(context.Background(), protocol.ProjectionPayload{AccountID: "alice"})
	if err == nil {
		t.Fatal("unreachable supervisor must NOT succeed silently")
	}
	if !IsUnmanaged(err) {
		t.Fatalf("unreachable supervisor must be IsUnmanaged, got %v", err)
	}
}

func TestClientContextCancelIsUnmanaged(t *testing.T) {
	client := NewClient(filepath.Join(t.TempDir(), "nowhere.sock"))
	client.Timeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Projection(ctx, protocol.ProjectionPayload{AccountID: "alice"})
	if err == nil || !IsUnmanaged(err) {
		t.Fatalf("cancelled context must be unmanaged, got %v", err)
	}
}
