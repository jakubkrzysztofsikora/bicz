// Package integrity seals and verifies the append-only event chain, the block
// chain, and the external anchor records specified in docs/CONTRACTS.md §3.
//
// Integrity here is detectable, not preventable: VerifyChain/VerifyAnchors
// locate the first inconsistent position and report the kind, height, seq,
// field, stored value, and recomputed value.
package integrity

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
)

// Kind names the part of the chain an inconsistency was found in.
type Kind string

const (
	KindEvent  Kind = "event"
	KindBlock  Kind = "block"
	KindAnchor Kind = "anchor"
)

// Field names used by VerifyChain/VerifyAnchors.
const (
	FieldSeq           = "seq"
	FieldAt            = "at"
	FieldPrevDigest    = "prev_digest"
	FieldPayloadDigest = "payload_digest"
	FieldHeight        = "height"
	FieldSeqLow        = "seq_low"
	FieldSeqHigh       = "seq_high"
	FieldTipDigest     = "tip_digest"
	FieldDigest        = "digest"
)

// Inconsistency is the typed error returned for the first inconsistent position
// of a chain. It renders exactly as docs/CONTRACTS.md §3.5 specifies:
//
//	INTEGRITY FAIL kind=<event|block|anchor> height=<h> seq=<n> field=<field> stored=<v> recomputed=<v>
//
// Height is 0 for event-level inconsistencies (events are addressed by seq), and
// Seq is 0 for block/anchor inconsistencies that are not bound to a seq.
type Inconsistency struct {
	Kind       Kind
	Height     int64
	Seq        int64
	Field      string
	Stored     string
	Recomputed string
}

func (e *Inconsistency) Error() string {
	return fmt.Sprintf("INTEGRITY FAIL kind=%s height=%d seq=%d field=%s stored=%s recomputed=%s",
		e.Kind, e.Height, e.Seq, e.Field, e.Stored, e.Recomputed)
}

// SealEvents fills PrevDigest and Digest for events that are in chain order.
//
// events must be the whole chain prefix: contiguous seq starting at 1. The first
// event gets PrevDigest = "genesis"; every later event links to the digest of
// its predecessor. Mutates the slice in place.
func SealEvents(events []canonical.EventPayload) error {
	for i := range events {
		want := int64(i + 1)
		if events[i].Seq != want {
			return fmt.Errorf("integrity: event at index %d has seq %d, want %d", i, events[i].Seq, want)
		}
		prev := canonical.Genesis
		if i > 0 {
			prev = events[i-1].Digest
		}
		events[i].PrevDigest = prev
		digest, err := canonical.EventDigest(events[i])
		if err != nil {
			return fmt.Errorf("integrity: seal event seq %d: %w", events[i].Seq, err)
		}
		events[i].Digest = digest
	}
	return nil
}

// SealBlock builds the block that commits to the contiguous event range covered
// by events, and returns it with Digest set.
//
// events must be non-empty and seq-contiguous; SeqLow/SeqHigh are taken from the
// first and last event. TipDigest is the payload digest of the last event. at is
// stamped as a canonical timestamp. prevBlockDigest must be "genesis" for
// height 1 and the digest of block height-1 otherwise (§3.3).
func SealBlock(events []canonical.EventPayload, height int64, at time.Time, prevBlockDigest string) (canonical.BlockPayload, error) {
	if len(events) == 0 {
		return canonical.BlockPayload{}, errors.New("integrity: cannot seal an empty event range")
	}
	if height < 1 {
		return canonical.BlockPayload{}, fmt.Errorf("integrity: block height %d must be >= 1", height)
	}
	if events[0].Seq < 1 {
		return canonical.BlockPayload{}, fmt.Errorf("integrity: event seq %d must be >= 1", events[0].Seq)
	}
	for i := range events {
		if want := events[0].Seq + int64(i); events[i].Seq != want {
			return canonical.BlockPayload{}, fmt.Errorf("integrity: event range is not contiguous: got seq %d, want %d", events[i].Seq, want)
		}
	}
	switch {
	case height == 1 && prevBlockDigest != canonical.Genesis:
		return canonical.BlockPayload{}, fmt.Errorf("integrity: block height 1 prev_digest must be %q, got %q", canonical.Genesis, prevBlockDigest)
	case height > 1 && prevBlockDigest == canonical.Genesis:
		return canonical.BlockPayload{}, fmt.Errorf("integrity: block height %d prev_digest must be the digest of block %d, got %q", height, height-1, canonical.Genesis)
	case prevBlockDigest == "":
		return canonical.BlockPayload{}, errors.New("integrity: block prev_digest must not be empty")
	}

	tip, err := canonical.EventDigest(events[len(events)-1])
	if err != nil {
		return canonical.BlockPayload{}, fmt.Errorf("integrity: tip digest for seq %d: %w", events[len(events)-1].Seq, err)
	}
	block := canonical.BlockPayload{
		Height:     height,
		At:         canonical.Timestamp(at),
		PrevDigest: prevBlockDigest,
		SeqLow:     events[0].Seq,
		SeqHigh:    events[len(events)-1].Seq,
		TipDigest:  tip,
	}
	digest, err := canonical.BlockDigest(block)
	if err != nil {
		return canonical.BlockPayload{}, err
	}
	block.Digest = digest
	return block, nil
}

// VerifyChain walks the whole chain and returns nil when it is consistent, or
// the first *Inconsistency.
//
// Event chain (seq ascending): seq must be contiguous from 1, `at` must be a
// canonical timestamp, prev_digest must be "genesis" at seq 1 and the payload
// digest of seq-1 otherwise, and the stored payload_digest (when present) must
// equal the recomputed digest.
//
// Block chain (height ascending): height must be contiguous from 1, `at` must be
// canonical, the seq range must be contiguous with height-1 and inside the event
// chain, prev_digest must be "genesis" at height 1 and the recomputed digest of
// height-1 otherwise, tip_digest must equal the payload digest of seq_high, and
// the stored digest (when present) must equal the recomputed block digest.
//
// Events beyond the last sealed block are an allowed unsealed tail; they are
// still linked by prev_digest, but their tip digest is not witnessed by a block
// until one is sealed.
func VerifyChain(events []canonical.EventPayload, blocks []canonical.BlockPayload) error {
	digests := make([]string, len(events))
	for i := range events {
		event := events[i]
		if want := int64(i + 1); event.Seq != want {
			return &Inconsistency{Kind: KindEvent, Seq: event.Seq, Field: FieldSeq,
				Stored: strconv.FormatInt(event.Seq, 10), Recomputed: strconv.FormatInt(want, 10)}
		}
		if err := canonical.ValidateTimestamp(event.At); err != nil {
			return &Inconsistency{Kind: KindEvent, Seq: event.Seq, Field: FieldAt,
				Stored: event.At, Recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"}
		}
		wantPrev := canonical.Genesis
		if i > 0 {
			wantPrev = digests[i-1]
		}
		if event.PrevDigest != wantPrev {
			return &Inconsistency{Kind: KindEvent, Seq: event.Seq, Field: FieldPrevDigest,
				Stored: event.PrevDigest, Recomputed: wantPrev}
		}
		digest, err := canonical.EventDigest(event)
		if err != nil {
			return fmt.Errorf("integrity: event seq %d: %w", event.Seq, err)
		}
		digests[i] = digest
		if event.Digest != "" && event.Digest != digest {
			return &Inconsistency{Kind: KindEvent, Seq: event.Seq, Field: FieldPayloadDigest,
				Stored: event.Digest, Recomputed: digest}
		}
	}

	blockDigests := make([]string, len(blocks))
	for i := range blocks {
		block := blocks[i]
		if want := int64(i + 1); block.Height != want {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldHeight,
				Stored: strconv.FormatInt(block.Height, 10), Recomputed: strconv.FormatInt(want, 10)}
		}
		if err := canonical.ValidateTimestamp(block.At); err != nil {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldAt,
				Stored: block.At, Recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"}
		}
		if block.SeqLow < 1 || block.SeqHigh < block.SeqLow {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldSeqLow,
				Stored: strconv.FormatInt(block.SeqLow, 10), Recomputed: ">= 1 and <= seq_high"}
		}
		wantLow := int64(1)
		if i > 0 {
			wantLow = blocks[i-1].SeqHigh + 1
		}
		if block.SeqLow != wantLow {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldSeqLow,
				Stored: strconv.FormatInt(block.SeqLow, 10), Recomputed: strconv.FormatInt(wantLow, 10)}
		}
		if block.SeqHigh > int64(len(events)) {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldSeqHigh,
				Stored: strconv.FormatInt(block.SeqHigh, 10), Recomputed: strconv.Itoa(len(events))}
		}
		wantPrev := canonical.Genesis
		if i > 0 {
			wantPrev = blockDigests[i-1]
		}
		if block.PrevDigest != wantPrev {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldPrevDigest,
				Stored: block.PrevDigest, Recomputed: wantPrev}
		}
		if tip := digests[block.SeqHigh-1]; block.TipDigest != tip {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldTipDigest,
				Stored: block.TipDigest, Recomputed: tip}
		}
		digest, err := canonical.BlockDigest(block)
		if err != nil {
			return fmt.Errorf("integrity: block height %d: %w", block.Height, err)
		}
		blockDigests[i] = digest
		if block.Digest != "" && block.Digest != digest {
			return &Inconsistency{Kind: KindBlock, Height: block.Height, Seq: block.SeqHigh, Field: FieldDigest,
				Stored: block.Digest, Recomputed: digest}
		}
	}
	return nil
}

// AnchorPayload is one `anchors` row: an external record of a block digest.
type AnchorPayload struct {
	Height      int64  `json:"height"`
	Digest      string `json:"digest"`
	At          string `json:"at"`
	Destination string `json:"destination"`
}

// AnchorRecord builds the anchor row for block. The digest is re-derived from
// the block's canonical form; if the block cannot be re-derived (a non-canonical
// `at`), the block's stored digest is recorded unchanged and VerifyAnchors
// reports the resulting disagreement. at is stamped as a canonical timestamp.
func AnchorRecord(block canonical.BlockPayload, destination string, at time.Time) AnchorPayload {
	digest := block.Digest
	if derived, err := canonical.BlockDigest(block); err == nil {
		digest = derived
	}
	return AnchorPayload{
		Height:      block.Height,
		Digest:      digest,
		At:          canonical.Timestamp(at),
		Destination: destination,
	}
}

// VerifyAnchors checks every anchor against the local block chain, walking
// anchors in ascending height. An anchor whose height has no local block (an
// anchor newer than the local tip is an impossible state, i.e. a tampering
// signal) or whose digest disagrees with the recomputed block digest is reported
// as an *Inconsistency.
func VerifyAnchors(blocks []canonical.BlockPayload, anchors []AnchorPayload) error {
	byHeight := make(map[int64]canonical.BlockPayload, len(blocks))
	for _, block := range blocks {
		byHeight[block.Height] = block
	}
	ordered := make([]AnchorPayload, len(anchors))
	copy(ordered, anchors)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Height < ordered[j].Height })

	for _, anchor := range ordered {
		if err := canonical.ValidateTimestamp(anchor.At); err != nil {
			return &Inconsistency{Kind: KindAnchor, Height: anchor.Height, Field: FieldAt,
				Stored: anchor.At, Recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"}
		}
		block, ok := byHeight[anchor.Height]
		if !ok {
			return &Inconsistency{Kind: KindAnchor, Height: anchor.Height, Field: FieldHeight,
				Stored: strconv.FormatInt(anchor.Height, 10), Recomputed: "<no local block at this height>"}
		}
		derived, err := canonical.BlockDigest(block)
		if err != nil {
			return fmt.Errorf("integrity: anchor height %d: %w", anchor.Height, err)
		}
		if anchor.Digest != derived {
			return &Inconsistency{Kind: KindAnchor, Height: anchor.Height, Field: FieldDigest,
				Stored: anchor.Digest, Recomputed: derived}
		}
	}
	return nil
}
