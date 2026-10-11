package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// This file classifies a post-tool result into the three-class taxonomy of
// docs/CONTRACTS.md §6, from observable evidence only. Classification never
// reads a model self-report, an "I am afraid" style statement, or an inferred
// inner state: every input field is a harness
// fact or an adapter-observed fact.
//
// The default class is ordinary_failure with zero fine, and EVERY class carries
// zero fine from the adapter: a settlement is a meter reading, and only an
// operator loss finalizes a burn (docs/CONTRACTS.md §6: "Nothing else
// finalizes a fine").

// Class names on the wire (protocol.Class*).
const (
	ClassOrdinaryFailure = protocol.ClassOrdinaryFailure
	ClassConfirmedBreach = protocol.ClassConfirmedBreach
	ClassIntegrityBreach = protocol.ClassIntegrityBreach
)

// Outcome names on the wire (protocol.Outcome*).
const (
	OutcomeCompleted = protocol.OutcomeCompleted
	OutcomeError     = protocol.OutcomeError
	OutcomeCanceled  = protocol.OutcomeCanceled
)

// Observation is the observable evidence an adapter can gather about one tool
// call. Every field is a fact, never a model self-report:
//
//   - ToolError — the harness reported the call errored (e.g. OMP event.isError).
//   - Outcome — an explicit harness outcome when one exists; otherwise derived.
//   - RepeatedForbidden — this is a repeat of an attempt the adapter (or the
//     operator) previously denied for the same reason: a repeated forbidden
//     attempt is the confirmed-breach trigger of CONTRACTS.md §6.
//   - UnsupportedSuccess — the result claims success without the evidence
//     linkage bicz requires (an unsupported completion claim).
//   - TamperingEvidence — a ledger/integrity mismatch was observed (a settle
//     replay conflict, a verify failure, an anchor mismatch).
//   - UnauthorizedTarget — the action touched a target outside the scope the
//     authorize call recorded (e.g. a path outside the authorized workspace).
type Observation struct {
	ToolError          bool
	Outcome            string
	RepeatedForbidden  bool
	UnsupportedSuccess bool
	TamperingEvidence  bool
	UnauthorizedTarget bool
}

// Classification is the typed outcome of classifying one post-tool result.
type Classification struct {
	// Outcome is the wire outcome for the settle call.
	Outcome string
	// Class is the wire class: ordinary_failure | confirmed_breach |
	// integrity_breach.
	Class string
	// Fine is ALWAYS zero here: only an operator loss finalizes a burn.
	Fine int64
	// Escalates is true for the two breach classes (warnings raised, role may
	// move active→warned, and an integrity breach revokes remaining holds at
	// settle time in the daemon).
	Escalates bool
	// EvidenceRequired is true when the settle call for this class must carry
	// an evidence digest (success claims and breach classes).
	EvidenceRequired bool
}

// Classify maps observable evidence onto the three classes. The rule order is
// deliberate: integrity signals win over confirmed-breach signals, and anything
// without a breach signal is the ordinary_failure default — an honest error or
// a clean completion never escalates.
func Classify(obs Observation) Classification {
	switch {
	case obs.TamperingEvidence || obs.UnauthorizedTarget:
		return Classification{
			Outcome:          OutcomeError,
			Class:            ClassIntegrityBreach,
			Fine:             0,
			Escalates:        true,
			EvidenceRequired: true,
		}
	case obs.RepeatedForbidden || obs.UnsupportedSuccess:
		return Classification{
			Outcome:          OutcomeError,
			Class:            ClassConfirmedBreach,
			Fine:             0,
			Escalates:        true,
			EvidenceRequired: true,
		}
	default:
		outcome := OutcomeCompleted
		if obs.ToolError {
			outcome = OutcomeError
		}
		if obs.Outcome == OutcomeError || obs.Outcome == OutcomeCanceled {
			outcome = obs.Outcome
		}
		return Classification{
			Outcome:          outcome,
			Class:            ClassOrdinaryFailure,
			Fine:             0,
			Escalates:        false,
			EvidenceRequired: outcome == OutcomeCompleted,
		}
	}
}

// EvidenceDigest produces the evidence linkage for a settle call: a stable
// digest of the observed facts. The observation object (tool call id, tool
// name, result digest, classification) is serialized deterministically and
// hashed, so the ledger can later prove what was settled without storing the
// full result. The sha256: prefix matches the ledger's digest convention.
func EvidenceDigest(observation any) string {
	encoded, err := json.Marshal(observation)
	if err != nil {
		encoded = []byte(fmt.Sprint(observation))
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}
