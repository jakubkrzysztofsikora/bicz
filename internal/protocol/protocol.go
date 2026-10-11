// Package protocol defines the versioned wire contract between the bicz
// supervisor daemon and its callers, exactly as fixed by docs/CONTRACTS.md §5.
//
// Two properties of this package are deliberate and load-bearing:
//
//   - It depends on nothing but the standard library. An adapter imports this
//     package to speak the protocol; it must not transitively pull in the
//     ledger, SQLite, or the daemon. The daemon is the only thing that bridges
//     wire types onto ledger calls.
//   - It carries NO authority field. There is no `authority`, `actor`,
//     `operator`, or `principal` field anywhere in the envelope or in any
//     payload, and DecodeRequest rejects a payload that supplies one
//     (docs/CONTRACTS.md §4, "No side channels"). Authority is the local OS user
//     at the operator CLI, never a string on the wire.
//
// The protocol is NOT authentication. It says nothing about who the caller is,
// and it cannot: bicz is an incentive-and-accountability tool for a cooperative
// same-UID agent, not an anti-tamper boundary. Any process running as the same
// user can speak this protocol, can bypass the adapter path entirely, and can
// edit the database. Integrity is detectable, not preventable
// (docs/CONTRACTS.md opening section and §8).
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ID is the frozen protocol identifier (docs/CONTRACTS.md §5).
const ID = "bicz.adapter/1"

// Version is the numeric form of the same identifier. A request carries both:
// ID is what the contract names, Version is what code compares. A request whose
// ID does not parse to Version is rejected with protocol_unsupported.
const Version = 1

// MaxRequestBytes bounds a single request so a caller cannot make the daemon
// read an unbounded stream.
const MaxRequestBytes = 1 << 20

// Method is the operation discriminator. The JSON field is `op`, the name the
// contract fixes; it is the method discriminator of the envelope.
type Method string

const (
	// MethodAuthorize asks for a bounded reservation before an action runs.
	// Mutating: it creates a reservation and a ledger event.
	MethodAuthorize Method = "authorize"
	// MethodSettle reports observed usage for a reservation exactly once.
	// Mutating: it charges the reservation and appends a ledger event.
	MethodSettle Method = "settle"
	// MethodLessonPropose stores an agent-authored lesson proposal. Mutating,
	// but non-canonical: it never touches balances, roles, or `lessons`.
	MethodLessonPropose Method = "lesson.propose"
	// MethodProjection reads the identity's context block. Read-only.
	MethodProjection Method = "projection"
	// MethodStatus reads daemon and coverage status. Read-only.
	MethodStatus Method = "status"
)

// MethodAliasProject is accepted for MethodProjection. The frozen contract names
// the operation `projection`; the milestone-2 work order called it `project`.
// The contract spelling is canonical and this alias only widens what decodes.
const MethodAliasProject Method = "project"

// ParseMethod maps an `op` string onto a Method. It accepts the canonical
// contract spellings and the documented `project` alias.
func ParseMethod(op string) (Method, bool) {
	switch Method(op) {
	case MethodAuthorize:
		return MethodAuthorize, true
	case MethodSettle:
		return MethodSettle, true
	case MethodLessonPropose:
		return MethodLessonPropose, true
	case MethodProjection, MethodAliasProject:
		return MethodProjection, true
	case MethodStatus:
		return MethodStatus, true
	default:
		return "", false
	}
}

// Mutating reports whether the method changes ledger state. Every mutating
// method MUST carry an idempotency key (docs/CONTRACTS.md §5).
func (m Method) Mutating() bool {
	switch m {
	case MethodAuthorize, MethodSettle, MethodLessonPropose:
		return true
	default:
		return false
	}
}

// Valid reports whether the method is one of the contract's operations.
func (m Method) Valid() bool {
	_, ok := ParseMethod(string(m))
	return ok
}

// Decision is the three-valued outcome vocabulary of docs/CONTRACTS.md §5.3.
type Decision string

const (
	// DecisionAllow means the action is authorized and a bounded reservation
	// exists. ReservationID is set.
	DecisionAllow Decision = "allow"
	// DecisionDeny means ordinary in-scope work is denied. Reason carries the
	// stable error code; Notice carries the causal explanation and the lesson.
	DecisionDeny Decision = "deny"
	// DecisionUnobserved means the path is outside adapter coverage. No
	// authorization is granted and no spend is metered. This is a first-class
	// outcome, not an error: it is how the tool stays honest about the paths it
	// cannot see.
	DecisionUnobserved Decision = "unobserved"
)

// Valid reports whether the decision is one of the three contract outcomes.
func (d Decision) Valid() bool {
	return d == DecisionAllow || d == DecisionDeny || d == DecisionUnobserved
}

// DaemonState is the recording status of the supervisor (docs/CONTRACTS.md §5.4
// `status`).
type DaemonState string

const (
	// DaemonRecording means a daemon is running and metering.
	DaemonRecording DaemonState = "recording"
	// DaemonUnmanaged means no daemon is reachable. Callers MUST surface this
	// and suspend recording. It is never a silent fallback to an unrecorded
	// economy, and no state may be fabricated in its place.
	DaemonUnmanaged DaemonState = "unmanaged"
)

// Request is the request envelope.
type Request struct {
	// Protocol MUST equal ID. A caller that speaks a different version gets
	// protocol_unsupported and the call is not attempted.
	Protocol string `json:"protocol"`
	// Version MUST equal Version. It is the numeric mirror of Protocol so a
	// comparison never needs to parse a string.
	Version int `json:"version"`
	// Op is the method discriminator (the contract's field name for it).
	Op Method `json:"op"`
	// RequestID correlates the response with this request. The daemon echoes it
	// verbatim; it carries no authority.
	RequestID string `json:"request_id"`
	// IdempotencyKey is REQUIRED on every mutating op. Same key + same payload
	// is a no-op returning the prior receipt; same key + different payload is a
	// hard payload_conflict. It is the caller's, not the daemon's.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Payload is the method-specific body. It must not contain an authority
	// field (see ValidateNoAuthority).
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is the response envelope.
type Response struct {
	// Protocol and Version mirror the request's, so a caller can detect a
	// daemon/protocol mismatch even on a response it did not expect.
	Protocol string `json:"protocol"`
	Version  int    `json:"version"`
	// RequestID echoes Request.RequestID.
	RequestID string `json:"request_id"`
	// Decision is set for authorize and settle. It is empty for read-only ops
	// and for protocol-level failures, which use Error instead.
	Decision Decision `json:"decision,omitempty"`
	// Reason is the stable code explaining the decision (docs/CONTRACTS.md §5.5
	// for denials; duplicate_key_ok for an idempotent replay).
	Reason Code `json:"reason,omitempty"`
	// Notice is human-readable trusted context: why the decision was made and
	// which lesson applies. It is escaped as data, never executed.
	Notice string `json:"notice,omitempty"`
	// ReservationID is set on an allow that created or replayed a reservation.
	ReservationID *string `json:"reservation_id,omitempty"`
	// State is the method-specific result body.
	State json.RawMessage `json:"state,omitempty"`
	// Error is set ONLY for protocol-level failures (unsupported version,
	// malformed envelope, unknown op, daemon-side failure) where no decision
	// applies. A denied action uses Decision/Reason, not Error.
	Error *ProtocolError `json:"error,omitempty"`
}

// ProtocolError is the typed failure of a protocol-level operation.
type ProtocolError struct {
	// Code is the stable machine-readable code.
	Code Code `json:"code"`
	// Message is human-readable detail. It is advisory; Code is normative.
	Message string `json:"message"`
}

func (e *ProtocolError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Errorf builds a *ProtocolError.
func Errorf(code Code, format string, args ...any) *ProtocolError {
	return &ProtocolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Validate checks the envelope's framing: the protocol id and version, a known
// op, the idempotency key on mutating ops, and the absence of a caller-supplied
// authority field. It does not validate the method payload; each method's
// handler does that.
func (r Request) Validate() error {
	if r.Protocol != ID || r.Version != Version {
		return Errorf(CodeProtocolUnsupported,
			"expected %s (version %d), got %q (version %d)", ID, Version, r.Protocol, r.Version)
	}
	method, ok := ParseMethod(string(r.Op))
	if !ok {
		return Errorf(CodeMalformedRequest, "unknown op %q", string(r.Op))
	}
	if method.Mutating() && r.IdempotencyKey == "" {
		return Errorf(CodeMalformedRequest, "op %q is mutating and requires an idempotency_key", r.Op)
	}
	if err := ValidateNoAuthority(r.Payload); err != nil {
		return err
	}
	return nil
}

// DecodeRequest reads exactly one JSON request from r.
//
// The transport is one request per connection, so this reads to EOF: there is no
// framing to get wrong and no request can be split across a stream read.
func DecodeRequest(r io.Reader) (Request, error) {
	limited := io.LimitReader(r, MaxRequestBytes)
	var request Request
	if err := json.NewDecoder(limited).Decode(&request); err != nil {
		if errors.Is(err, io.EOF) {
			return Request{}, Errorf(CodeMalformedRequest, "empty request")
		}
		return Request{}, Errorf(CodeMalformedRequest, "cannot decode request: %v", err)
	}
	return request, nil
}

// EncodeResponse writes one JSON response to w.
func EncodeResponse(w io.Writer, response Response) error {
	response.Protocol = ID
	response.Version = Version
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(response); err != nil {
		return fmt.Errorf("protocol: encode response: %w", err)
	}
	return nil
}

// DecodeResponse reads exactly one JSON response from r.
func DecodeResponse(r io.Reader) (Response, error) {
	limited := io.LimitReader(r, MaxRequestBytes)
	var response Response
	if err := json.NewDecoder(limited).Decode(&response); err != nil {
		if errors.Is(err, io.EOF) {
			return Response{}, Errorf(CodeDaemonUnavailable, "daemon closed the connection without a response")
		}
		return Response{}, Errorf(CodeMalformedRequest, "cannot decode response: %v", err)
	}
	if response.Protocol != ID || response.Version != Version {
		return Response{}, Errorf(CodeProtocolUnsupported,
			"daemon answered %s (version %d)", response.Protocol, response.Version)
	}
	if response.Error != nil {
		return response, response.Error
	}
	return response, nil
}

// NewKey returns a fresh idempotency key.
//
// It is a convenience for callers that do not have a natural key of their own
// (an operator CLI command, for example). The key belongs to the CALLER: the
// daemon never invents one, because a key the caller cannot reproduce cannot
// make a retry a no-op.
func NewKey() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("key-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(suffix[:])
}

// MarshalPayload encodes a method payload into the envelope's Payload field.
func MarshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode payload: %w", err)
	}
	return encoded, nil
}

// MarshalState encodes a method state into the envelope's State field.
func MarshalState(state any) (json.RawMessage, error) {
	return MarshalPayload(state)
}
