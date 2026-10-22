package daemon

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// AnchorStaleMultiple is the staleness window: an anchor older than this many
// cadences is reported stale (docs/CONTRACTS.md §3.6). The cadence itself and the
// event threshold come from the newest published schedule, or the ledger's
// documented defaults.
const AnchorStaleMultiple = 2

// Anchor statuses reported by doctor.
const (
	// AnchorStatusAnchored means the most recent anchor is inside the cadence.
	AnchorStatusAnchored = "anchored"
	// AnchorStatusStale means the most recent anchor is older than 2x the
	// cadence.
	AnchorStatusStale = "stale-anchor"
	// AnchorStatusUnmanaged means no anchor exists at all.
	AnchorStatusUnmanaged = "unmanaged"
)

// DoctorReport is the disclosure report of `bicz doctor`.
//
// It states what is true and states what is NOT claimed. It never asserts
// containment, sandboxing, or cryptographic operator authentication — see
// Claims below and docs/CONTRACTS.md §8.
type DoctorReport struct {
	// LedgerPath is the database examined.
	LedgerPath string
	// SchemaVersion is the highest applied migration version.
	SchemaVersion int64
	// Stats are the chain tip positions.
	Stats ledger.ChainStats
	// ChainError is the first inconsistency found, or nil.
	ChainError error
	// UnsealedTail is how many events exist beyond the last sealed block. An
	// unsealed tail is allowed; it is simply not yet witnessed by a block.
	UnsealedTail int64
	// LastAnchor is the most recent anchor record, or nil.
	LastAnchor *integrity.AnchorPayload
	// AnchorAge is how long ago the last anchor was written.
	AnchorAge time.Duration
	// AnchorStatus is one of anchored, stale-anchor, unmanaged.
	AnchorStatus string
	// AnchorThresholdHit is true when the last anchor no longer covers the
	// current tip by more than the event threshold, i.e. §3.6's "once per 1,000
	// appended events" half of the cadence has come due.
	AnchorThresholdHit bool
	// AnchorEventsSince is how many events were appended after the block the
	// last anchor committed to.
	AnchorEventsSince int64
	// AnchorWindow is the anchoring cadence in force, from the newest published
	// schedule or the documented default (docs/CONTRACTS.md §3.6).
	AnchorWindow time.Duration
	// AnchorEventWindow is how many appended events make an anchor due.
	AnchorEventWindow int64
	// Daemon is the supervisor's recording state, or unmanaged.
	Daemon protocol.StatusState
	// Coverage is the disclosed observed/unobserved table.
	Coverage []protocol.Coverage
	// UnobservedNames are the paths bicz cannot see, named explicitly.
	UnobservedNames []string
	// Claims lists what this tool explicitly does NOT claim.
	Claims []string
}

// NonClaims is the honest-claims boundary of docs/CONTRACTS.md §8, restated
// wherever a user might otherwise assume otherwise.
var NonClaims = []string{
	"no sandbox and no per-action OS isolation",
	"no containment of the same-UID agent, the harness, or the harness's credentials",
	"no anti-tamper guarantee: integrity is detectable, not prevented",
	"no provider metering or capping of provider spend; internal action credits are not cash",
	"no cross-principal signing or cryptographic operator authentication beyond \"a human recorded this at the CLI\"",
	"no enforcement beyond cooperative same-user adapter observation; unobserved paths are named, not hidden",
	"bypassability is disclosed, not hidden: an agent that skips the adapter is not metered",
}

// Doctor assembles the report. It reports failures rather than raising them, so
// an unhealthy ledger still produces a complete report.
func (o *Operator) Doctor(ctx context.Context, cfg Config) (DoctorReport, error) {
	report := DoctorReport{
		LedgerPath: o.ledger.Path(),
		Claims:     NonClaims,
		Coverage:   protocol.CoverageFor(""),
	}
	version, err := o.ledger.SchemaVersion(ctx)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = version
	stats, err := o.ledger.ChainStats(ctx)
	if err != nil {
		return report, err
	}
	report.Stats = stats

	events, err := o.ledger.Events(ctx)
	if err != nil {
		return report, err
	}
	blocks, err := o.ledger.Blocks(ctx)
	if err != nil {
		return report, err
	}
	anchors, err := o.ledger.Anchors(ctx)
	if err != nil {
		return report, err
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		report.ChainError = err
	} else if err := integrity.VerifyAnchors(blocks, anchors); err != nil {
		report.ChainError = err
	} else if err := verifyAnchorDestinations(anchors); err != nil {
		report.ChainError = err
	}

	sealed := int64(0)
	if len(blocks) > 0 {
		sealed = blocks[len(blocks)-1].SeqHigh
	}
	report.UnsealedTail = stats.Seq - sealed

	cadence, threshold, err := o.ledger.AnchoringCadence(ctx)
	if err != nil {
		return report, err
	}
	report.AnchorWindow = cadence
	report.AnchorEventWindow = threshold

	report.AnchorStatus = AnchorStatusUnmanaged
	if len(anchors) > 0 {
		last := anchors[len(anchors)-1]
		report.LastAnchor = &last
		report.AnchorStatus = AnchorStatusAnchored
		// §3.6 requires an anchor at least once per cadence window AND at least
		// once per event threshold, whichever comes first, so the event count is
		// measured from the block the anchor committed to, not from the last seal.
		for _, block := range blocks {
			if block.Height == last.Height {
				report.AnchorEventsSince = stats.Seq - block.SeqHigh
				break
			}
		}
		report.AnchorThresholdHit = report.AnchorEventsSince >= threshold
		age, ageErr := anchorAge(last.At, o.ledger.Clock().Now())
		if ageErr == nil {
			report.AnchorAge = age
			if age > cadence*AnchorStaleMultiple || report.AnchorThresholdHit {
				report.AnchorStatus = AnchorStatusStale
			}
		} else {
			report.AnchorStatus = AnchorStatusStale
		}
	}

	report.Daemon = Status(ctx, cfg)
	report.UnobservedNames = protocol.UnobservedPaths("")
	return report, nil
}

// anchorAge is how long ago an anchor timestamp was written.
func anchorAge(at string, now time.Time) (time.Duration, error) {
	parsed, err := time.Parse(canonical.TimestampLayout, at)
	if err != nil {
		return 0, err
	}
	return now.Sub(parsed), nil
}

// Render writes the report in the terse, skimmable form the contract's §3.5
// output style uses.
func (r DoctorReport) Render(w io.Writer) {
	fmt.Fprintf(w, "LEDGER db=%s schema=%d\n", r.LedgerPath, r.SchemaVersion)
	if r.ChainError != nil {
		fmt.Fprintf(w, "%s\n", r.ChainError.Error())
	} else {
		fmt.Fprintf(w, "INTEGRITY OK height=%d events=%d anchors=%d\n", r.Stats.Height, r.Stats.Seq, r.Stats.Anchors)
	}
	fmt.Fprintf(w, "BLOCKS sealed=%d unsealed_tail=%d\n", r.Stats.Height, r.UnsealedTail)
	switch {
	case r.LastAnchor != nil:
		fmt.Fprintf(w, "ANCHOR status=%s height=%d age=%s events_since=%d window=%s/%d destination=%s\n",
			r.AnchorStatus, r.LastAnchor.Height, r.AnchorAge.Round(time.Second),
			r.AnchorEventsSince, r.AnchorWindow, r.AnchorEventWindow, r.LastAnchor.Destination)
	default:
		fmt.Fprintf(w, "ANCHOR status=%s window=%s/%d note=no external anchor exists; tampering is not externally detectable until one is written\n",
			r.AnchorStatus, r.AnchorWindow, r.AnchorEventWindow)
	}
	if r.AnchorThresholdHit {
		fmt.Fprintf(w, "ANCHOR WARNING %d events appended since the last anchor (cadence is one per %d)\n",
			r.AnchorEventsSince, r.AnchorEventWindow)
	}
	fmt.Fprintf(w, "DAEMON status=%s socket=%s\n", r.Daemon.Daemon, r.Daemon.Socket)
	if r.Daemon.Note != "" {
		fmt.Fprintf(w, "DAEMON note=%s\n", r.Daemon.Note)
	}
	fmt.Fprintf(w, "COVERAGE\n")
	for _, row := range r.Coverage {
		state := "unobserved"
		if row.Observed {
			state = "observed"
		}
		fmt.Fprintf(w, "  %-12s %-28s %s", row.Harness, row.Source, state)
		if row.Mechanism != "" {
			fmt.Fprintf(w, "  %s", row.Mechanism)
		}
		if row.Note != "" {
			fmt.Fprintf(w, "  [%s]", row.Note)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "UNOBSERVED %d path(s) named above are not seen, not enforced, and not metered\n", len(r.UnobservedNames))
	fmt.Fprintf(w, "CLAIMS none of:")
	for _, claim := range r.Claims {
		fmt.Fprintf(w, "\n  - %s", claim)
	}
	fmt.Fprintln(w)
}
