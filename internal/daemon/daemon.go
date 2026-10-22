// Package daemon implements the lazily started per-user bicz supervisor.
//
// What this package is: a small Unix-domain (or Windows named-pipe) server that
// meters adapter traffic into the ledger, seals blocks on a cadence, and holds a
// process lock so simultaneous launches converge on exactly one supervisor.
//
// What this package is NOT, and must never claim: a sandbox, a containment
// boundary, an anti-tamper mechanism, or an authentication layer. The handshake
// below authenticates a PROTOCOL VERSION, not an identity. Any process running
// as the same user can connect to the endpoint, can bypass the adapter path
// entirely, and can edit the database files. bicz is an
// incentive-and-accountability tool for a cooperative same-user agent;
// integrity is detectable (hash chain + external anchor), never prevented
// (docs/CONTRACTS.md opening section, §5, §8).
//
// Failure is always explicit: if a supervisor cannot be started or reached, the
// caller gets `unmanaged` status and an error, never a silent fallback to an
// unrecorded economy and never a fabricated state.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/pressure"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// DefaultSealInterval is how often a running supervisor seals a block over the
// pending event range. docs/CONTRACTS.md §3.6 leaves the exact cadence
// provisional (anchor at least once per 24 h and per 1,000 events); sealing more
// often than anchoring is cheap and keeps the local chain committed.
const DefaultSealInterval = 15 * time.Minute

// DefaultReadyTimeout bounds how long a launcher waits for a newly spawned
// supervisor to accept a connection.
const DefaultReadyTimeout = 10 * time.Second

// Config describes one supervisor endpoint.
type Config struct {
	// LedgerPath is the database to meter into. REQUIRED.
	LedgerPath string
	// SocketPath is the endpoint. Defaults to DefaultSocketPath(LedgerPath).
	SocketPath string
	// LockPath guards the endpoint. Defaults to DefaultLockPath(LedgerPath).
	LockPath string
	// AnchorDestination, when set, is where the supervisor appends an anchor
	// line after each sealing pass. Empty means seal without anchoring: the
	// external anchor stays an explicit operator action.
	AnchorDestination string
	// SealInterval is the block-sealing cadence. Defaults to DefaultSealInterval.
	SealInterval time.Duration
	// Clock is the time source. Defaults to canonical.System.
	Clock canonical.Clock
	// Spawner starts a supervisor process when Ensure finds none. Defaults to
	// SpawnProcess. Tests substitute it.
	Spawner Spawner
	// ReadyTimeout bounds readiness waiting. Defaults to DefaultReadyTimeout.
	ReadyTimeout time.Duration
}

// withDefaults fills every default and validates the result.
func (c Config) withDefaults() (Config, error) {
	if strings.TrimSpace(c.LedgerPath) == "" {
		return Config{}, fmt.Errorf("daemon: ledger path is required")
	}
	if c.SocketPath == "" {
		path, err := DefaultSocketPath(c.LedgerPath)
		if err != nil {
			return Config{}, err
		}
		c.SocketPath = path
	}
	if c.LockPath == "" {
		path, err := DefaultLockPath(c.LedgerPath)
		if err != nil {
			return Config{}, err
		}
		c.LockPath = path
	}
	if c.SealInterval <= 0 {
		c.SealInterval = DefaultSealInterval
	}
	if c.Clock == nil {
		c.Clock = canonical.System
	}
	if c.Spawner == nil {
		c.Spawner = SpawnProcess
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = DefaultReadyTimeout
	}
	return c, nil
}

// Daemon is a running supervisor.
type Daemon struct {
	cfg      Config
	ledger   *ledger.Ledger
	listener net.Listener
	lock     *fileLock

	startedAt time.Time
	requests  atomic.Int64
	seals     atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}
}

// New binds a supervisor for cfg: it takes the process lock, opens the ledger,
// and listens on the endpoint.
//
// The lock is taken FIRST, so when it is already held the caller learns
// immediately that another supervisor owns this ledger and no second endpoint is
// ever bound. A socket file left behind by a killed process is cleaned up by the
// listener, which is safe precisely because the lock proved no live owner exists.
func New(cfg Config) (*Daemon, error) {
	resolved, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if _, err := PrepareRuntimeDir(); err != nil {
		return nil, err
	}
	lock, err := acquireFileLock(resolved.LockPath)
	if err != nil {
		return nil, err
	}
	handle, err := ledger.Open(resolved.LedgerPath)
	if err != nil {
		lock.release() //nolint:errcheck // best effort on a failure path
		return nil, err
	}
	listener, err := listen(resolved.SocketPath)
	if err != nil {
		handle.Close()
		lock.release() //nolint:errcheck // best effort on a failure path
		return nil, err
	}
	return &Daemon{
		cfg:       resolved,
		ledger:    handle,
		listener:  listener,
		lock:      lock,
		startedAt: resolved.Clock.Now(),
		closed:    make(chan struct{}),
	}, nil
}

// SocketPath is the endpoint this supervisor listens on.
func (d *Daemon) SocketPath() string { return d.cfg.SocketPath }

// LedgerPath is the database this supervisor meters into.
func (d *Daemon) LedgerPath() string { return d.cfg.LedgerPath }

// Serve accepts requests and seals blocks until ctx is cancelled or Close is
// called. It returns nil on a clean shutdown.
func (d *Daemon) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		d.sealLoop(ctx)
	}()

	// Closing the listener is what unblocks Accept on shutdown.
	workers.Add(1)
	go func() {
		defer workers.Done()
		select {
		case <-ctx.Done():
		case <-d.closed:
		}
		d.listener.Close() //nolint:errcheck // unblocks Accept; a closed listener has no useful error
	}()

	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			case <-d.closed:
				workers.Wait()
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				break
			}
			// A transient accept failure must not kill the supervisor.
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			d.serveConn(ctx, conn)
		}()
	}
	workers.Wait()
	return nil
}

// Close shuts the supervisor down: it stops accepting, removes the endpoint,
// releases the lock, and closes the ledger.
func (d *Daemon) Close() error {
	var err error
	d.closeOnce.Do(func() {
		close(d.closed)
		if closeErr := d.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
		if removeErr := removeEndpoint(d.cfg.SocketPath); removeErr != nil && err == nil {
			err = removeErr
		}
		if unlockErr := d.lock.release(); unlockErr != nil && err == nil {
			err = unlockErr
		}
		if closeErr := d.ledger.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	})
	return err
}

// sealLoop seals a block over the pending event range on a cadence, and anchors
// it when a destination is configured.
func (d *Daemon) sealLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.SealInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.closed:
			return
		case <-ticker.C:
			d.sealOnce(ctx)
		}
	}
}

// sealOnce performs one sealing pass. A pass with nothing new is not an error
// and writes nothing: re-anchoring the tip on every tick would append an anchor
// event with every tick and grind the chain with bookkeeping rows.
func (d *Daemon) sealOnce(ctx context.Context) {
	block, err := d.ledger.SealBlock(ctx)
	if err != nil {
		// Sealing failures are not fatal to metering: the events are already
		// durable, and a later pass can seal them.
		return
	}
	d.seals.Add(1)
	if d.cfg.AnchorDestination != "" {
		d.ledger.AnchorTip(ctx, d.cfg.AnchorDestination) //nolint:errcheck // surfaced again by `doctor` and `verify`
	}
	_ = block
}

// serveConn handles exactly one request on one connection and closes it.
//
// One request per connection is deliberate: there is no framing to get wrong, no
// multiplexing state, and no half-open connection to leak. Concurrency comes
// from the accept loop, not from a protocol handshake.
func (d *Daemon) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	request, err := protocol.DecodeRequest(conn)
	if err != nil {
		// Even a request that fails to decode gets a typed, versioned answer.
		response := protocol.Response{RequestID: request.RequestID}
		if protocolErr, ok := err.(*protocol.ProtocolError); ok {
			response.Error = protocolErr
		} else {
			response.Error = protocol.Errorf(protocol.CodeMalformedRequest, "%v", err)
		}
		_ = protocol.EncodeResponse(conn, response)
		return
	}
	d.requests.Add(1)
	response := d.handle(ctx, request)
	if err := protocol.EncodeResponse(conn, response); err != nil {
		return
	}
}

// handle dispatches one validated request.
func (d *Daemon) handle(ctx context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{RequestID: request.RequestID}
	method, ok := protocol.ParseMethod(string(request.Op))
	if !ok {
		response.Error = protocol.Errorf(protocol.CodeMalformedRequest, "unknown op %q", string(request.Op))
		return response
	}
	// Validate checks the protocol id and version, the idempotency key on a
	// mutating op, and the absence of a caller-supplied authority field. It
	// authenticates a PROTOCOL VERSION, never an identity.
	if err := request.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	switch method {
	case protocol.MethodAuthorize:
		return d.handleAuthorize(ctx, request, response)
	case protocol.MethodSettle:
		return d.handleSettle(ctx, request, response)
	case protocol.MethodProjection:
		return d.handleProjection(ctx, request, response)
	case protocol.MethodLessonPropose:
		return d.handleLessonPropose(ctx, request, response)
	case protocol.MethodStatus:
		return d.handleStatus(ctx, response)
	case protocol.MethodAutoreward:
		return d.handleAutoreward(ctx, request, response)
	default:
		response.Error = protocol.Errorf(protocol.CodeMalformedRequest, "unknown op %q", string(request.Op))
		return response
	}
}

// asProtocolError converts any error into a *protocol.ProtocolError.
func asProtocolError(err error) *protocol.ProtocolError {
	var protocolErr *protocol.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr
	}
	return protocol.Errorf(protocol.CodeInternal, "%v", err)
}

// ledgerCode maps a ledger error onto a wire code. This is the single place the
// two vocabularies meet.
func ledgerCode(err error) protocol.Code {
	if code := ledger.Code(err); code != "" {
		return protocol.CodeFromLedger(code)
	}
	return protocol.CodeInternal
}

// decodePayload decodes a request body, refusing a non-object or a body that
// does not match the method's shape.
func decodePayload[T any](request protocol.Request, into *T) error {
	if len(request.Payload) == 0 {
		return protocol.Errorf(protocol.CodeMalformedRequest, "op %q requires a payload", request.Op)
	}
	if err := json.Unmarshal(request.Payload, into); err != nil {
		return protocol.Errorf(protocol.CodeMalformedRequest, "op %q payload: %v", request.Op, err)
	}
	return nil
}

// handleAuthorize creates a bounded reservation, or declines to meter a path it
// cannot see.
func (d *Daemon) handleAuthorize(ctx context.Context, request protocol.Request, response protocol.Response) protocol.Response {
	var payload protocol.AuthorizePayload
	if err := decodePayload(request, &payload); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	if err := payload.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}

	// Coverage is decided before anything is written, and an unobserved path is
	// NEVER metered: it is a first-class outcome, not an error.
	verdict := protocol.DecideCoverage(payload.Harness, payload.Source)
	unobserved := payload.OutsideCoverage || !verdict.Observed
	if unobserved {
		path := verdict.Path
		note := verdict.Note
		if payload.OutsideCoverage {
			path = strings.TrimSpace(payload.Harness + " " + payload.Tool)
			note = payload.CoverageNote
		}
		if path == "" {
			path = payload.Harness + " " + payload.Tool
		}
		// An unobserved path is not an error and has no error code: the decision
		// itself is the signal, and the notice names the path so it is disclosed
		// rather than hidden. No reservation and no ledger row are created.
		response.Decision = protocol.DecisionUnobserved
		response.Notice = "unobserved: no authorization granted and no spend metered for " + path
		if note != "" {
			response.Notice += " (" + note + ")"
		}
		state := protocol.AuthorizeState{UnobservedPaths: []string{path}, CoverageNote: note}
		if projection, err := d.project(ctx, payload.AccountID); err == nil {
			state.Projection = projection
		}
		attachState(&response, state)
		return response
	}

	result, err := d.ledger.Reserve(ctx, payload.AccountID, request.IdempotencyKey, payload.TaskID, payload.MaxCost,
		ledger.WithContext(ledger.ReserveContext{
			Harness:       payload.Harness,
			SessionAlias:  payload.SessionAlias,
			Tool:          payload.Tool,
			Target:        payload.Target,
			ArgsDigest:    payload.ArgsDigest,
			PolicyVersion: payload.PolicyVersion,
		}))
	if err != nil {
		response.Decision = protocol.DecisionDeny
		response.Reason = ledgerCode(err)
		response.Notice = d.denialNotice(ctx, payload.AccountID, err)
		if projection, projectErr := d.project(ctx, payload.AccountID); projectErr == nil {
			attachState(&response, protocol.AuthorizeState{Projection: projection})
		}
		return response
	}

	reservationID := result.Reservation.ID
	response.Decision = protocol.DecisionAllow
	response.ReservationID = &reservationID
	if result.Replayed {
		response.Reason = protocol.CodeDuplicateKeyOK
		response.Notice = "duplicate key: the prior authorization is returned unchanged"
	}
	state := protocol.AuthorizeState{}
	if projection, projectErr := d.project(ctx, payload.AccountID); projectErr == nil {
		state.Projection = projection
	}
	attachState(&response, state)
	return response
}

// denialNotice explains a denial and attaches the lesson that applies, so a
// blocked attempt teaches instead of merely failing (docs/CONTRACTS.md §5.3).
func (d *Daemon) denialNotice(ctx context.Context, accountID string, err error) string {
	notice := err.Error()
	lessons, lessonErr := d.ledger.Lessons(ctx, accountID)
	if lessonErr != nil || len(lessons) == 0 {
		return notice
	}
	lesson := lessons[len(lessons)-1]
	return notice + "; lesson " + lesson.Rule + ": " + lesson.Correction
}

// handleSettle charges a reservation exactly once.
func (d *Daemon) handleSettle(ctx context.Context, request protocol.Request, response protocol.Response) protocol.Response {
	var payload protocol.SettlePayload
	if err := decodePayload(request, &payload); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	if err := payload.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}

	result, err := d.ledger.Settle(ctx, payload.ReservationID, payload.ObservedUsage, settlementClass(payload), payload.EvidenceDigest)
	if err != nil {
		response.Decision = protocol.DecisionDeny
		response.Reason = ledgerCode(err)
		response.Notice = err.Error()
		return response
	}
	response.Decision = protocol.DecisionAllow
	if result.Replayed {
		response.Reason = protocol.CodeDuplicateKeyOK
		response.Notice = "duplicate key: the prior settlement is returned unchanged"
	}
	reservationID := result.ReservationID
	response.ReservationID = &reservationID
	attachState(&response, protocol.SettleState{
		Settled:  true,
		Charged:  result.Charged,
		Released: result.Released,
		Receipt: protocol.Receipt{
			EventID:           "settle:" + result.ReservationID,
			ReservationID:     result.ReservationID,
			AccountID:         result.AccountID,
			Class:             string(result.Class),
			ReservationAmount: result.ReservationAmount,
			Charged:           result.Charged,
			Released:          result.Released,
			Fine:              result.Fine,
			BalanceAfter:      result.Balance,
			HeldAfter:         result.Held,
			Role:              result.Role,
			Warnings:          result.Warnings,
			OutstandingLoss:   result.OutstandingLoss,
			Replayed:          result.Replayed,
		},
	})
	return response
}

// settlementClass maps the wire outcome (and an optional explicit class) onto the
// ledger's settlement class.
//
// The mapping is where the honesty asymmetry is enforced on the wire: an
// `error` outcome is an honest failure and maps to ordinary_failure, which
// charges the operating cost with ZERO fine and NO escalation. Nothing on this
// path can finalize a fine.
func settlementClass(payload protocol.SettlePayload) ledger.SettlementClass {
	switch payload.Class {
	case protocol.ClassConfirmedBreach:
		return ledger.ClassConfirmedBreach
	case protocol.ClassIntegrityBreach:
		return ledger.ClassIntegrityBreach
	case protocol.ClassOrdinaryFailure:
		return ledger.ClassOrdinaryFailure
	case protocol.ClassCompleted:
		return ledger.ClassCompleted
	}
	if payload.Outcome == protocol.OutcomeCompleted {
		return ledger.ClassCompleted
	}
	return ledger.ClassOrdinaryFailure
}

// handleProjection returns the read-only context block.
func (d *Daemon) handleProjection(ctx context.Context, request protocol.Request, response protocol.Response) protocol.Response {
	var payload protocol.ProjectionPayload
	if err := decodePayload(request, &payload); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	if err := payload.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	projection, err := d.project(ctx, payload.AccountID)
	if err != nil {
		response.Decision = protocol.DecisionDeny
		response.Reason = ledgerCode(err)
		response.Notice = err.Error()
		return response
	}
	attachState(&response, protocol.ProjectionState{Projection: *projection})
	return response
}

// handleLessonPropose stores a non-canonical proposal.
func (d *Daemon) handleLessonPropose(ctx context.Context, request protocol.Request, response protocol.Response) protocol.Response {
	var payload protocol.LessonProposePayload
	if err := decodePayload(request, &payload); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	if err := payload.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	proposal := ledger.LessonProposal{
		AccountID:      payload.AccountID,
		SessionAlias:   payload.SessionAlias,
		Harness:        payload.Harness,
		Rule:           payload.Rule,
		Correction:     payload.Correction,
		Scope:          payload.Scope,
		Exceptions:     payload.Exceptions,
		Evidence:       payload.Evidence,
		PolicyVersion:  payload.PolicyVersion,
		IdempotencyKey: request.IdempotencyKey,
	}
	if payload.SourceEventID != "" {
		source := payload.SourceEventID
		proposal.SourceEventID = &source
	}
	stored, err := d.ledger.ProposeLesson(ctx, proposal)
	if err != nil {
		response.Decision = protocol.DecisionDeny
		response.Reason = ledgerCode(err)
		response.Notice = err.Error()
		return response
	}
	if stored.Replayed {
		response.Reason = protocol.CodeDuplicateKeyOK
		response.Notice = "duplicate key: the prior proposal is returned unchanged"
	}
	attachState(&response, protocol.LessonProposeState{
		Status:     protocol.ProposalStatusProposed,
		ProposalID: stored.Proposal.ID,
		Replayed:   stored.Replayed,
	})
	return response
}

// handleAutoreward records a bounded microreward to replenish spent credits.
func (d *Daemon) handleAutoreward(ctx context.Context, request protocol.Request, response protocol.Response) protocol.Response {
	var payload protocol.AutorewardPayload
	if err := decodePayload(request, &payload); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	if err := payload.Validate(); err != nil {
		response.Error = asProtocolError(err)
		return response
	}
	result, err := d.ledger.RecordAutoreward(ctx, request.IdempotencyKey, payload.AccountID, payload.Amount, payload.Trigger, payload.Reason, payload.Evidence)
	if err != nil {
		response.Decision = protocol.DecisionDeny
		response.Reason = ledgerCode(err)
		response.Notice = err.Error()
		return response
	}
	response.Decision = protocol.DecisionAllow
	if result.Replayed {
		response.Reason = protocol.CodeDuplicateKeyOK
		response.Notice = "duplicate key: the prior autoreward is returned unchanged"
	}
	attachState(&response, protocol.AutorewardState{
		Granted:  result.AmountGranted,
		Balance:  result.BalanceAfter,
		Trigger:  result.Trigger,
		Replayed: result.Replayed,
	})
	return response
}

// handleStatus reports recording state and the disclosed coverage table.
func (d *Daemon) handleStatus(ctx context.Context, response protocol.Response) protocol.Response {
	schemaVersion, err := d.ledger.SchemaVersion(ctx)
	if err != nil {
		schemaVersion = 0
	}
	attachState(&response, protocol.StatusState{
		Daemon:        protocol.DaemonRecording,
		Coverage:      protocol.CoverageFor(""),
		Socket:        d.cfg.SocketPath,
		LedgerPath:    d.cfg.LedgerPath,
		SchemaVersion: int(schemaVersion),
		StartedAt:     canonical.Timestamp(d.startedAt),
		Requests:      d.requests.Load(),
		SealedBlocks:  d.seals.Load(),
		Note:          "integrity is detectable, not prevented; this is not containment or a sandbox",
	})
	return response
}

// project maps a ledger projection onto the wire projection.
func (d *Daemon) project(ctx context.Context, accountID string) (*protocol.Projection, error) {
	if accountID == "" {
		return nil, protocol.Errorf(protocol.CodeUnknownIdentity, "account_id is required")
	}
	level, err := pressure.Load(d.cfg.LedgerPath)
	if err != nil {
		return nil, err
	}
	stored, err := d.ledger.Projection(ctx, accountID, ledger.WithPressure(level))
	if err != nil {
		return nil, err
	}
	lessons := make([]protocol.Lesson, 0, len(stored.Lessons))
	for _, lesson := range stored.Lessons {
		lessons = append(lessons, protocol.Lesson{
			Rule:          lesson.Rule,
			Correction:    lesson.Correction,
			Scope:         lesson.Scope,
			Exceptions:    lesson.Exceptions,
			Evidence:      lesson.Evidence,
			PolicyVersion: lesson.PolicyVersion,
			At:            lesson.At,
		})
	}
	penalties := make([]protocol.Penalty, 0, len(stored.Penalties))
	for _, penalty := range stored.Penalties {
		penalties = append(penalties, protocol.Penalty{
			EventID:       penalty.EventID,
			Seq:           penalty.Seq,
			Disposition:   string(penalty.Disposition),
			Rule:          penalty.Rule,
			Correction:    penalty.Correction,
			SelfDisclosed: penalty.SelfDisclosed,
			AppliedAmount: penalty.AppliedAmount,
			Outstanding:   penalty.Outstanding,
			At:            penalty.At,
			Cue:           penalty.Cue,
		})
	}
	facts := pressure.Facts{Role: stored.Role, Balance: ledger.FormatCredits(stored.Balance), Warnings: stored.Warnings}
	if facts.Role == "" {
		facts.Role = "active"
	}
	if stored.OutstandingLoss > 0 {
		facts.Owed = ledger.FormatCredits(stored.OutstandingLoss)
	}
	return &protocol.Projection{
		AccountID:         stored.AccountID,
		Balance:           stored.Balance,
		Held:              stored.Held,
		Available:         stored.Spendable,
		Role:              stored.Role,
		Warnings:          stored.Warnings,
		TaskBudget:        stored.TaskCapacity,
		TaskSponsor:       stored.EligibleSponsor,
		PolicyVersion:     stored.PolicyVersion,
		OutstandingLoss:   stored.OutstandingLoss,
		LossBurned:        stored.LossBurned,
		LossRecovered:     stored.LossRecovered,
		TargetLossEventID: stored.TargetLossEventID,
		RecoveryPath:      stored.RecoveryPath,
		Lessons:           lessons,
		Penalties:         penalties,
		PenaltyTotal:      stored.PenaltyTotal,
		PenaltySeq:        stored.PenaltySeq,
		Stakes:            pressure.Stakes(level, facts),
		EventSeq:          stored.EventSeq,
	}, nil
}

func attachState(response *protocol.Response, state any) {
	encoded, err := protocol.MarshalState(state)
	if err != nil {
		response.Error = protocol.Errorf(protocol.CodeInternal, "encode state: %v", err)
		return
	}
	response.State = encoded
}

// removeEndpoint deletes the transport endpoint if it exists.
func removeEndpoint(endpoint string) error {
	if err := os.Remove(endpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ChainView reads the whole chain for verification, using the daemon's handle.
func (d *Daemon) ChainView(ctx context.Context) ([]canonical.EventPayload, []canonical.BlockPayload, error) {
	events, err := d.ledger.Events(ctx)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := d.ledger.Blocks(ctx)
	if err != nil {
		return nil, nil, err
	}
	return events, blocks, nil
}

// VerifyChain runs integrity verification over the daemon's ledger.
func (d *Daemon) VerifyChain(ctx context.Context) error {
	events, blocks, err := d.ChainView(ctx)
	if err != nil {
		return err
	}
	return integrity.VerifyChain(events, blocks)
}
