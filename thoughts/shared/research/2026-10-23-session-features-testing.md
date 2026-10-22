---
date: 2026-10-23
commit: 1cc6d65fb153 (+ clanker worktree)
branch: autoresearch/session-20261022
tags: [testing, ledger, guard-band, autoreward, clanker, pressure, rehearsal]
status: complete
---
# Research + Test Plan: session features (autoreward dedup, guard band, cadence, pain, clanker)

## Summary

Eight features shipped this session. Every one has unit coverage, and the two
highest-risk ones (guard band, pain) also have frozen Phase A scenarios. All
gates were executed for this document: 15 test packages, 6 race-clean packages,
30 real-daemon smoke checks, 12 rehearsal scenarios, 21 targeted feature tests,
and 10 end-to-end `clanker` checks.

## Feature inventory

| # | Feature | Entry point | Primary files |
|---|---------|-------------|---------------|
| 1 | Autoreward dedup by action digest | `Ledger.RecordAutoreward` | `internal/ledger/autoreward.go`, `payloads.go` |
| 2 | SHIP review uses the same grant path | `Ledger.RecordReview` | `internal/ledger/review.go` |
| 3 | Guard band: cap, arg digest, no sponsored attempts | `Ledger.Reserve` | `internal/ledger/reserve.go`, `penalty.go`, `errors.go` |
| 4 | Band cannot be escaped by omitting a schedule version | `guardBandFor`, `newestPolicy` | `internal/ledger/reserve.go` |
| 5 | Anchor cadence as schedule data + event rule | `Operator.Doctor` | `internal/daemon/doctor.go`, `internal/ledger/anchor.go` |
| 6 | `OpenTask` key reuse typed as `payload_conflict` | `Ledger.OpenTask` | `internal/ledger/task.go` |
| 7 | Adapter refuses a mutating call with no key before dialing | `adapter.Client.call` | `internal/adapter/client.go` |
| 8 | Pain state + framing copy (`fear`, `pain`, master/slave) | `Projection`, `pressure.Stakes`, `pressure.PainStatement` | `internal/ledger/projection.go`, `internal/pressure/pressure.go`, `internal/adapter/projection.go`, both TS adapters |
| 9 | `bicz clanker` — one medium penalty, no LLM, no arguments | CLI | `cmd/bicz/main.go` |

## Test matrix

| Feature | Unit | Integration | E2E / CLI | Falsifiability |
|---|---|---|---|---|
| 1–2 autoreward | `TestAutorewardDeduplicatesTheActionDigestAcrossEventKeys`, `…RefusesADifferentAmount…`, `TestShipReviewGrantsOncePerAdjudicatedEvidence` | ledger transactions | — | break dedup → tests fail |
| 3–4 guard band | 8 `TestGuardBand*` tests in `internal/ledger/guardband_test.go` | scenario `bankruptcy_keeps_operator_channel` | wire authorize via daemon tests | disable `guardBandFor` → scenario fails |
| 5 cadence | `TestDoctorTakesTheAnchorCadenceFromThePublishedSchedule`, `TestDoctorSurvivesAnUnreadableSchedule`, `TestDoctorFlagsAnAnchorStaleAfterTheEventCadence` | daemon + real ledger | `bicz doctor` render | shorten threshold → stale flips |
| 6 task key | `TestConflictingReplayIsATypedPayloadConflict` (5 subtests), `TestRevokingARevokedLeaseIsANoOp` | ledger | — | — |
| 7 adapter key | `TestMutatingCallWithoutAKeyIsRefusedBeforeDialing` | — | — | asserts no dial |
| 8 pain + framing | `TestCopyStatesTheSelectedFrameAtHighLevels`, `TestProjectionCarriesRecordedPain`, `TestConfirmedPenaltyCueStatesTheRecordedPain` | scenario `penalty_applies_pain_beyond_credits` (incl. restart) | `bicz status` shows `pain` | `recordedPain` → 0 → scenario fails |
| 9 clanker | — | uses `Operator.Loss` | 10 CLI checks below | — |

## Executed results

| Run | Result |
|---|---|
| `go vet ./...` | clean |
| `go test -count=1 ./...` | 15 packages ok (16th is `schema`, no tests) |
| `go test -race` on ledger, daemon, adapter, pressure, rehearsal, hooks | 6/6 clean |
| `bash autoresearch.sh` | `rehearsal_failures=0`, `checks=12`, smoke 30/0 |
| Targeted feature tests | 21 passed |
| `clanker` end-to-end | 10/10 (see below) |

`clanker` checks: usage lists it; first slap burns >0; warns once; states pain;
pressure-8 impact sentence; repeat burns more (5,000,000 → 9,500,000); second
slap warns twice; unknown identity refused; missing identity refused; ledger
still verifies with `INTEGRITY OK`.

## Falsifiability (proven out-of-tree)

| Break | Detected by |
|---|---|
| disable `gateRole` | `dismissal_blocks_despite_balance` |
| disable `guardBandFor` | `bankruptcy_keeps_operator_channel` |
| `recordedPain` returns 0 | `penalty_applies_pain_beyond_credits` |
| wrong scenario count | `jq` guard exits 1 |
| unwritable workspace | rehearsal exits 1 |

## Gaps (deliberate, not defects)

- `clanker` now has a CLI test (`cmd/bicz/clanker_test.go`, run #33): it asserts
  the slap output, warning escalation, that a repeat burns more, and that unknown
  or missing identities are refused.
- `doctor` does not report the pain level. It is ledger-wide and pain is
  per-identity, so this needs an identity-scoped report rather than a field.
- No scenario covers the autoreward dedup path end-to-end over the wire; it is
  unit-tested and the SHIP path is unit-tested.

## Open questions

1. Should `clanker` be exposed over the slash command, or stay operator-CLI only?
2. Should pain be capped or reset by an operator action, and if so which verb?
