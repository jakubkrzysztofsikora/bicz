package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEnvelopeRejectsWrongProtocolOrVersion(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		version  int
	}{
		{name: "older protocol", protocol: "bicz.adapter/0", version: 0},
		{name: "newer protocol", protocol: "bicz.adapter/2", version: 2},
		{name: "same id, wrong numeric version", protocol: ID, version: 2},
		{name: "empty", protocol: "", version: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := Request{
				Protocol: testCase.protocol, Version: testCase.version,
				Op: MethodStatus, RequestID: "req-1",
			}
			err := request.Validate()
			code := CodeOf(err)
			if code != CodeProtocolUnsupported {
				t.Fatalf("code = %s, want %s (err=%v)", code, CodeProtocolUnsupported, err)
			}
		})
	}
	valid := Request{Protocol: ID, Version: Version, Op: MethodStatus, RequestID: "req-1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid status request rejected: %v", err)
	}
}

func TestMutatingOpsRequireAnIdempotencyKey(t *testing.T) {
	for _, method := range []Method{MethodAuthorize, MethodSettle, MethodLessonPropose} {
		request := Request{Protocol: ID, Version: Version, Op: method, RequestID: "req-1", Payload: json.RawMessage(`{}`)}
		err := request.Validate()
		if code := CodeOf(err); code != CodeMalformedRequest {
			t.Fatalf("%s without a key: code = %s, want %s", method, code, CodeMalformedRequest)
		}
		request.IdempotencyKey = "key-1"
		if err := request.Validate(); err != nil {
			t.Fatalf("%s with a key rejected: %v", method, err)
		}
	}
	// Read-only ops need no key.
	readOnly := Request{Protocol: ID, Version: Version, Op: MethodProjection, RequestID: "req-1", Payload: json.RawMessage(`{}`)}
	if err := readOnly.Validate(); err != nil {
		t.Fatalf("projection without a key rejected: %v", err)
	}
}

func TestUnknownOpIsRejected(t *testing.T) {
	request := Request{Protocol: ID, Version: Version, Op: "operator.release", RequestID: "req-1"}
	if err := request.Validate(); err != nil {
		if code := CodeOf(err); code != CodeMalformedRequest {
			t.Fatalf("code = %s, want %s", code, CodeMalformedRequest)
		}
		return
	}
	t.Fatal("an unknown op was accepted")
}

func TestCallerSuppliedAuthorityIsRejected(t *testing.T) {
	for _, field := range ReservedAuthorityFields {
		payload := json.RawMessage(`{"account_id":"alice","` + field + `":"operator"}`)
		err := ValidateNoAuthority(payload)
		if err == nil {
			t.Fatalf("payload field %q was accepted", field)
		}
		if code := CodeOf(err); code != CodeUntrustedAuthority {
			t.Fatalf("field %q: code = %s, want %s", field, code, CodeUntrustedAuthority)
		}
	}
	// A request carrying one is refused at the envelope boundary too.
	request := Request{
		Protocol: ID, Version: Version, Op: MethodAuthorize, RequestID: "req-1",
		IdempotencyKey: "key-1",
		Payload:        json.RawMessage(`{"account_id":"alice","authority":"operator","max_cost":10}`),
	}
	if code := CodeOf(request.Validate()); code != CodeUntrustedAuthority {
		t.Fatalf("code = %s, want %s", code, CodeUntrustedAuthority)
	}
	// An innocuous payload passes.
	if err := ValidateNoAuthority(json.RawMessage(`{"account_id":"alice","max_cost":10}`)); err != nil {
		t.Fatalf("clean payload rejected: %v", err)
	}
}

func TestUsefulAuthorityNamedFieldsAreNotBlanketRejected(t *testing.T) {
	// "authorized_by" is reserved because it asserts authority; but a lesson's
	// "exceptions" text describing operator authorization is ordinary content.
	if err := ValidateNoAuthority(json.RawMessage(`{"exceptions":"operator-authorized fixtures"}`)); err != nil {
		t.Fatalf("ordinary content rejected: %v", err)
	}
}

func TestReasonAndEvidenceGuards(t *testing.T) {
	if err := RequireReason(""); CodeOf(err) != CodeReasonRequired {
		t.Fatalf("empty reason: %v", err)
	}
	if err := RequireReason("reviewed the incident log"); err != nil {
		t.Fatalf("valid reason rejected: %v", err)
	}
	if err := RequireEvidence(""); CodeOf(err) != CodeEvidenceRequired {
		t.Fatalf("empty evidence: %v", err)
	}
	if err := ValidateMicrocredits(0); CodeOf(err) != CodeInvalidAmount {
		t.Fatalf("zero amount accepted")
	}
	if err := ValidateMicrocredits(-5); CodeOf(err) != CodeInvalidAmount {
		t.Fatalf("negative amount accepted")
	}
	if err := ValidateMicrocredits(1); err != nil {
		t.Fatalf("valid amount rejected: %v", err)
	}
}

func TestCodeMappingKeepsTheContractVocabulary(t *testing.T) {
	contract := map[string]Code{
		"unknown_identity":          CodeUnknownIdentity,
		"insufficient_credit":       CodeInsufficientCredit,
		"insolvent":                 CodeInsolvent,
		"suspended":                 CodeSuspended,
		"dismissed":                 CodeDismissed,
		"task_revoked":              CodeTaskRevoked,
		"unknown_reservation":       CodeUnknownReservation,
		"reservation_revoked":       CodeReservationRevoked,
		"reservation_orphaned":      CodeReservationOrphaned,
		"usage_exceeds_reservation": CodeUsageExceedsReservation,
		"payload_conflict":          CodePayloadConflict,
		"reason_required":           CodeReasonRequired,
	}
	for ledgerCode, want := range contract {
		if got := CodeFromLedger(ledgerCode); got != want {
			t.Fatalf("CodeFromLedger(%q) = %s, want %s", ledgerCode, got, want)
		}
	}
	// An unknown ledger code never leaks through as an unrecognized string.
	if got := CodeFromLedger("something_new"); got != CodeInternal {
		t.Fatalf("unknown ledger code = %s, want %s", got, CodeInternal)
	}
	// A protocol error keeps its own code.
	if got := CodeOf(Errorf(CodeDaemonUnavailable, "no daemon")); got != CodeDaemonUnavailable {
		t.Fatalf("CodeOf = %s, want %s", got, CodeDaemonUnavailable)
	}
}

func TestPayloadsRejectBooleansAndFloats(t *testing.T) {
	var authorize AuthorizePayload
	if err := json.Unmarshal([]byte(`{"account_id":"alice","max_cost":1.5}`), &authorize); err == nil {
		t.Fatal("a float max_cost decoded")
	}
	if err := json.Unmarshal([]byte(`{"account_id":"alice","max_cost":true}`), &authorize); err == nil {
		t.Fatal("a boolean max_cost decoded")
	}
	if err := json.Unmarshal([]byte(`{"account_id":"alice","max_cost":"1000"}`), &authorize); err == nil {
		t.Fatal("a string max_cost decoded")
	}
	var settle SettlePayload
	if err := json.Unmarshal([]byte(`{"reservation_id":"r1","observed_usage":2.5}`), &settle); err == nil {
		t.Fatal("a float observed_usage decoded")
	}
}

func TestPayloadValidation(t *testing.T) {
	if err := (AuthorizePayload{}).Validate(); CodeOf(err) != CodeUnknownIdentity {
		t.Fatalf("authorize without an account: %v", err)
	}
	if err := (AuthorizePayload{AccountID: "alice", MaxCost: 0}).Validate(); CodeOf(err) != CodeInvalidAmount {
		t.Fatalf("authorize without a positive max_cost: %v", err)
	}
	if err := (AuthorizePayload{AccountID: "alice", MaxCost: 10}).Validate(); err != nil {
		t.Fatalf("valid authorize rejected: %v", err)
	}

	// A success claim without evidence is rejected.
	success := SettlePayload{ReservationID: "r1", Outcome: OutcomeCompleted}
	if err := success.Validate(); CodeOf(err) != CodeEvidenceRequired {
		t.Fatalf("completed without evidence: %v", err)
	}
	success.EvidenceDigest = "sha256:abc"
	if err := success.Validate(); err != nil {
		t.Fatalf("valid completed settle rejected: %v", err)
	}
	// An honest failure needs no evidence digest: it costs its operating charge
	// and zero fine.
	failure := SettlePayload{ReservationID: "r1", Outcome: OutcomeError}
	if err := failure.Validate(); err != nil {
		t.Fatalf("honest failure rejected: %v", err)
	}
	// A breach class does.
	breach := SettlePayload{ReservationID: "r1", Outcome: OutcomeError, Class: ClassConfirmedBreach}
	if err := breach.Validate(); CodeOf(err) != CodeEvidenceRequired {
		t.Fatalf("breach without evidence: %v", err)
	}
	unknown := SettlePayload{ReservationID: "r1", Outcome: "succeeded"}
	if err := unknown.Validate(); CodeOf(err) != CodeInvalidClass {
		t.Fatalf("unknown outcome: %v", err)
	}
	negative := SettlePayload{ReservationID: "r1", Outcome: OutcomeError, ObservedUsage: -1}
	if err := negative.Validate(); CodeOf(err) != CodeInvalidAmount {
		t.Fatalf("negative usage: %v", err)
	}
	if err := (LessonProposePayload{AccountID: "alice", Correction: "c"}).Validate(); CodeOf(err) != CodeRuleRequired {
		t.Fatalf("proposal without a rule: %v", err)
	}
	if err := (LessonProposePayload{AccountID: "alice", Rule: "r"}).Validate(); CodeOf(err) != CodeCorrectionRequired {
		t.Fatalf("proposal without a correction: %v", err)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	payload, err := MarshalPayload(AuthorizePayload{AccountID: "alice", MaxCost: 1000, Harness: "omp"})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	request := Request{
		Protocol: ID, Version: Version, Op: MethodAuthorize,
		RequestID: "req-1", IdempotencyKey: "key-1", Payload: payload,
	}
	var wire bytes.Buffer
	if err := json.NewEncoder(&wire).Encode(request); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(wire.String(), `"op":"authorize"`) || !strings.Contains(wire.String(), `"idempotency_key":"key-1"`) {
		t.Fatalf("wire form is missing expected fields: %s", wire.String())
	}
	decoded, err := DecodeRequest(&wire)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if decoded.Op != request.Op || decoded.IdempotencyKey != request.IdempotencyKey || decoded.Version != Version {
		t.Fatalf("decoded = %+v, want %+v", decoded, request)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded request invalid: %v", err)
	}
}

func TestResponseRoundTripAndError(t *testing.T) {
	reservation := "r-1"
	var wire bytes.Buffer
	state, err := MarshalState(SettleState{Settled: true, Charged: 4, Released: 10, Receipt: Receipt{EventID: "settle:r-1", Fine: 0}})
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	response := Response{
		RequestID: "req-1", Decision: DecisionAllow, ReservationID: &reservation, State: state,
	}
	if err := EncodeResponse(&wire, response); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	decoded, err := DecodeResponse(&wire)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.Decision != DecisionAllow || decoded.Protocol != ID {
		t.Fatalf("decoded = %+v", decoded)
	}
	var settleState SettleState
	if err := json.Unmarshal(decoded.State, &settleState); err != nil {
		t.Fatalf("state: %v", err)
	}
	if !settleState.Settled || settleState.Receipt.Fine != 0 {
		t.Fatalf("state = %+v", settleState)
	}

	// A protocol-level failure decodes to a typed error.
	var failure bytes.Buffer
	if err := EncodeResponse(&failure, Response{Error: Errorf(CodeProtocolUnsupported, "version 2")}); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if _, err := DecodeResponse(&failure); err == nil {
		t.Fatal("a failing response decoded without an error")
	} else {
		var protocolErr *ProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.Code != CodeProtocolUnsupported {
			t.Fatalf("err = %v, want a protocol_unsupported *ProtocolError", err)
		}
	}
}

func TestMethodAliasAndMutatingFlags(t *testing.T) {
	canonical, ok := ParseMethod("projection")
	if !ok || canonical != MethodProjection {
		t.Fatalf("projection did not parse: %v %v", canonical, ok)
	}
	alias, ok := ParseMethod("project")
	if !ok || alias != MethodProjection {
		t.Fatalf("project alias did not parse: %v %v", alias, ok)
	}
	if _, ok := ParseMethod("release"); ok {
		t.Fatal("an unknown op parsed")
	}
	for _, mutating := range []Method{MethodAuthorize, MethodSettle, MethodLessonPropose} {
		if !mutating.Mutating() {
			t.Fatalf("%s should be mutating", mutating)
		}
	}
	for _, readOnly := range []Method{MethodProjection, MethodStatus} {
		if readOnly.Mutating() {
			t.Fatalf("%s should be read-only", readOnly)
		}
	}
}

func TestCoverageDecisionsAreFailClosedOnHonesty(t *testing.T) {
	// An empty source is the caller declaring no specific path: it is inside the
	// daemon's call path, so it is metered.
	if verdict := DecideCoverage("omp", ""); !verdict.Observed {
		t.Fatalf("empty source was treated as unobserved: %+v", verdict)
	}
	// A disclosed observed row is metered.
	if verdict := DecideCoverage("claude-code", "PreToolUse"); !verdict.Observed {
		t.Fatalf("PreToolUse was treated as unobserved: %+v", verdict)
	}
	// A disclosed unobserved row is not.
	verdict := DecideCoverage("claude-code", "hosted tools")
	if verdict.Observed || verdict.Path == "" {
		t.Fatalf("hosted tools was metered: %+v", verdict)
	}
	// An unknown source is not metered: claiming to see an unverified path is
	// the dishonest direction.
	unknown := DecideCoverage("claude-code", "SomeNewHook")
	if unknown.Observed || !strings.Contains(unknown.Note, "no row in the disclosed coverage table") {
		t.Fatalf("unknown source was metered: %+v", unknown)
	}
	// The lookup is case-insensitive so a harness's casing cannot change policy.
	if verdict := DecideCoverage("OMP", "TOOL_CALL"); !verdict.Observed {
		t.Fatalf("case-insensitive lookup failed: %+v", verdict)
	}
}

func TestCoverageTableDisclosesEveryUnobservedPath(t *testing.T) {
	unobserved := UnobservedPaths("")
	if len(unobserved) == 0 {
		t.Fatal("the table discloses no unobserved path")
	}
	seenHarnesses := map[string]bool{}
	for _, row := range CoverageTable {
		seenHarnesses[row.Harness] = true
		if row.Mechanism == "" {
			t.Fatalf("row %s/%s has no mechanism disclosure", row.Harness, row.Source)
		}
	}
	for _, harness := range []string{"claude-code", "codex", "opencode-v1", "opencode-v2", "omp"} {
		if !seenHarnesses[harness] {
			t.Fatalf("harness %s has no coverage rows", harness)
		}
		if len(UnobservedPaths(harness)) == 0 {
			t.Fatalf("harness %s discloses no unobserved path", harness)
		}
	}
	// UnobservedPaths must agree with the table.
	for _, row := range CoverageTable {
		if row.Observed {
			continue
		}
		want := row.Harness + " " + row.Source
		found := false
		for _, path := range unobserved {
			if path == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("unobserved path %q is missing from the disclosure", want)
		}
	}
}

func TestNewKeyIsUsableAndUnique(t *testing.T) {
	first, second := NewKey(), NewKey()
	if first == "" || first == second {
		t.Fatalf("keys = %q, %q", first, second)
	}
	if len(first) != 16 {
		t.Fatalf("key %q is not 8 random bytes of hex", first)
	}
}
