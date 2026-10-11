package integrity_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
)

var baseTime = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// wantInc names the first inconsistent position. Stored/Recomputed are checked
// only when non-empty.
type wantInc struct {
	kind       integrity.Kind
	height     int64
	seq        int64
	field      string
	stored     string
	recomputed string
}

func requireInconsistency(t *testing.T, err error, want wantInc) {
	t.Helper()
	if err == nil {
		t.Fatalf("verification = nil, want kind=%s height=%d seq=%d field=%s", want.kind, want.height, want.seq, want.field)
	}
	var got *integrity.Inconsistency
	if !errors.As(err, &got) {
		t.Fatalf("error %v (%T) is not *integrity.Inconsistency", err, err)
	}
	if got.Kind != want.kind || got.Height != want.height || got.Seq != want.seq || got.Field != want.field {
		t.Fatalf("inconsistency\n got: %s\nwant: kind=%s height=%d seq=%d field=%s",
			got, want.kind, want.height, want.seq, want.field)
	}
	if want.stored != "" && got.Stored != want.stored {
		t.Fatalf("stored = %q, want %q", got.Stored, want.stored)
	}
	if want.recomputed != "" && got.Recomputed != want.recomputed {
		t.Fatalf("recomputed = %q, want %q", got.Recomputed, want.recomputed)
	}
}

// buildChain returns n sealed, internally consistent events (seq 1..n).
func buildChain(t *testing.T, n int) []canonical.EventPayload {
	t.Helper()
	events := make([]canonical.EventPayload, n)
	for i := range events {
		account := "acct_demo"
		events[i] = canonical.EventPayload{
			Seq:       int64(i + 1),
			AccountID: &account,
			Kind:      "reward",
			Amount:    int64(1000 * (i + 1)),
			Payload:   `{"policy_version":"1"}`,
			Actor:     "jakub",
			Reason:    fmt.Sprintf("reason %d", i+1),
			At:        canonical.Timestamp(baseTime.Add(time.Duration(i+1) * time.Second)),
			ID:        fmt.Sprintf("evt_%04d", i+1),
		}
	}
	if err := integrity.SealEvents(events); err != nil {
		t.Fatalf("SealEvents: %v", err)
	}
	return events
}

func sealOne(t *testing.T, events []canonical.EventPayload) canonical.BlockPayload {
	t.Helper()
	block, err := integrity.SealBlock(events, 1, baseTime.Add(10*time.Minute), canonical.Genesis)
	if err != nil {
		t.Fatalf("SealBlock: %v", err)
	}
	return block
}

func TestSealBlockAndVerifyChain(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 3)
	block := sealOne(t, events)

	if block.Height != 1 {
		t.Fatalf("height = %d, want 1", block.Height)
	}
	if block.SeqLow != 1 || block.SeqHigh != 3 {
		t.Fatalf("range = [%d,%d], want [1,3]", block.SeqLow, block.SeqHigh)
	}
	if block.PrevDigest != canonical.Genesis {
		t.Fatalf("prev_digest = %q, want %q", block.PrevDigest, canonical.Genesis)
	}
	if block.At != canonical.Timestamp(baseTime.Add(10*time.Minute)) {
		t.Fatalf("at = %q", block.At)
	}
	tip, err := canonical.EventDigest(events[2])
	if err != nil {
		t.Fatalf("EventDigest: %v", err)
	}
	if block.TipDigest != tip {
		t.Fatalf("tip_digest = %q, want %q", block.TipDigest, tip)
	}
	wantDigest, err := canonical.BlockDigest(block)
	if err != nil {
		t.Fatalf("BlockDigest: %v", err)
	}
	if block.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", block.Digest, wantDigest)
	}
	if events[0].PrevDigest != canonical.Genesis {
		t.Fatalf("event 1 prev_digest = %q, want %q", events[0].PrevDigest, canonical.Genesis)
	}
	for i := 1; i < len(events); i++ {
		if events[i].PrevDigest != events[i-1].Digest {
			t.Fatalf("event %d prev_digest does not link to event %d", events[i].Seq, events[i-1].Seq)
		}
	}
	if err := integrity.VerifyChain(events, []canonical.BlockPayload{block}); err != nil {
		t.Fatalf("VerifyChain = %v, want nil", err)
	}
}

func TestVerifyChainEmpty(t *testing.T) {
	t.Parallel()
	if err := integrity.VerifyChain(nil, nil); err != nil {
		t.Fatalf("VerifyChain(nil, nil) = %v, want nil", err)
	}
}

// An event beyond the last sealed block is an allowed unsealed tail.
func TestVerifyChainAllowsUnsealedTail(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 3)
	block, err := integrity.SealBlock(events[:2], 1, baseTime.Add(time.Minute), canonical.Genesis)
	if err != nil {
		t.Fatalf("SealBlock: %v", err)
	}
	if err := integrity.VerifyChain(events, []canonical.BlockPayload{block}); err != nil {
		t.Fatalf("VerifyChain = %v, want nil for an unsealed tail", err)
	}
}

func TestVerifyChainDetectsTamperedEvent(t *testing.T) {
	t.Parallel()
	digestField := integrity.FieldPayloadDigest
	tests := []struct {
		name   string
		mutate func(events []canonical.EventPayload)
		want   wantInc
	}{
		{
			"first event amount",
			func(events []canonical.EventPayload) { events[0].Amount = 999999 },
			wantInc{kind: integrity.KindEvent, seq: 1, field: digestField},
		},
		{
			"middle event reason",
			func(events []canonical.EventPayload) { events[1].Reason = "tampered" },
			wantInc{kind: integrity.KindEvent, seq: 2, field: digestField},
		},
		{
			"tip event id",
			func(events []canonical.EventPayload) { events[2].ID = "evt_hacked" },
			wantInc{kind: integrity.KindEvent, seq: 3, field: digestField},
		},
		{
			"tip event payload text",
			func(events []canonical.EventPayload) { events[2].Payload = `{"policy_version":"9"}` },
			wantInc{kind: integrity.KindEvent, seq: 3, field: digestField},
		},
		{
			"tip event at, one nanosecond",
			func(events []canonical.EventPayload) {
				events[2].At = canonical.Timestamp(baseTime.Add(3*time.Second + time.Nanosecond))
			},
			wantInc{kind: integrity.KindEvent, seq: 3, field: digestField},
		},
		{
			"tip event account_id",
			func(events []canonical.EventPayload) {
				other := "acct_other"
				events[2].AccountID = &other
			},
			wantInc{kind: integrity.KindEvent, seq: 3, field: digestField},
		},
		{
			"tip event kind",
			func(events []canonical.EventPayload) { events[2].Kind = "loss" },
			wantInc{kind: integrity.KindEvent, seq: 3, field: digestField},
		},
		{
			"middle event prev_digest link",
			func(events []canonical.EventPayload) { events[1].PrevDigest = "sha256:0000" },
			wantInc{kind: integrity.KindEvent, seq: 2, field: integrity.FieldPrevDigest,
				stored: "sha256:0000"},
		},
		{
			"reordered event sequence",
			func(events []canonical.EventPayload) { events[2].Seq = 9 },
			wantInc{kind: integrity.KindEvent, seq: 9, field: integrity.FieldSeq, stored: "9", recomputed: "3"},
		},
		{
			"non-canonical timestamp",
			func(events []canonical.EventPayload) { events[1].At = "2026-10-10T12:00:02Z" },
			wantInc{kind: integrity.KindEvent, seq: 2, field: integrity.FieldAt,
				stored: "2026-10-10T12:00:02Z", recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := buildChain(t, 3)
			block := sealOne(t, events)
			tc.mutate(events)
			err := integrity.VerifyChain(events, []canonical.BlockPayload{block})
			requireInconsistency(t, err, tc.want)
			if tc.want.field != digestField {
				return
			}
			var got *integrity.Inconsistency
			errors.As(err, &got)
			if got.Stored == got.Recomputed || got.Stored == "" || got.Recomputed == "" {
				t.Fatalf("inconsistency does not report stored vs recomputed digests: %s", got)
			}
		})
	}
}

// A deleted event shifts every later seq, breaking the chain at that point.
func TestVerifyChainDetectsDeletedEvent(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 3)
	block := sealOne(t, events)
	short := []canonical.EventPayload{events[0], events[2]}
	requireInconsistency(t, integrity.VerifyChain(short, []canonical.BlockPayload{block}),
		wantInc{kind: integrity.KindEvent, seq: 3, field: integrity.FieldSeq, stored: "3", recomputed: "2"})
}

func TestVerifyChainDetectsTamperedBlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(block *canonical.BlockPayload)
		want   wantInc
	}{
		{
			"stored digest",
			func(block *canonical.BlockPayload) {
				block.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			},
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldDigest,
				stored: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		},
		{
			"at, one nanosecond",
			func(block *canonical.BlockPayload) {
				block.At = canonical.Timestamp(baseTime.Add(10*time.Minute + time.Nanosecond))
			},
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldDigest},
		},
		{
			"tip digest",
			func(block *canonical.BlockPayload) { block.TipDigest = "sha256:1111" },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldTipDigest,
				stored: "sha256:1111"},
		},
		{
			"prev digest",
			func(block *canonical.BlockPayload) { block.PrevDigest = "sha256:2222" },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldPrevDigest,
				stored: "sha256:2222", recomputed: canonical.Genesis},
		},
		{
			"height",
			func(block *canonical.BlockPayload) { block.Height = 2 },
			wantInc{kind: integrity.KindBlock, height: 2, seq: 3, field: integrity.FieldHeight,
				stored: "2", recomputed: "1"},
		},
		{
			"seq_low does not start at 1",
			func(block *canonical.BlockPayload) { block.SeqLow = 2 },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldSeqLow,
				stored: "2", recomputed: "1"},
		},
		{
			"seq_high past the chain tip",
			func(block *canonical.BlockPayload) { block.SeqHigh = 9 },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 9, field: integrity.FieldSeqHigh,
				stored: "9", recomputed: "3"},
		},
		{
			"seq_high moved inside the chain",
			func(block *canonical.BlockPayload) { block.SeqHigh = 2 },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 2, field: integrity.FieldTipDigest},
		},
		{
			"non-canonical at",
			func(block *canonical.BlockPayload) { block.At = "2026-10-10T12:10:00+00:00" },
			wantInc{kind: integrity.KindBlock, height: 1, seq: 3, field: integrity.FieldAt,
				stored: "2026-10-10T12:10:00+00:00", recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := buildChain(t, 3)
			block := sealOne(t, events)
			tc.mutate(&block)
			requireInconsistency(t, integrity.VerifyChain(events, []canonical.BlockPayload{block}), tc.want)
		})
	}
}

func TestTwoBlockChain(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 4)
	first, err := integrity.SealBlock(events[:2], 1, baseTime, canonical.Genesis)
	if err != nil {
		t.Fatalf("SealBlock(1): %v", err)
	}
	second, err := integrity.SealBlock(events[2:], 2, baseTime.Add(time.Minute), first.Digest)
	if err != nil {
		t.Fatalf("SealBlock(2): %v", err)
	}
	if second.PrevDigest != first.Digest {
		t.Fatalf("block 2 prev_digest = %q, want %q", second.PrevDigest, first.Digest)
	}
	blocks := []canonical.BlockPayload{first, second}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		t.Fatalf("VerifyChain = %v, want nil", err)
	}

	t.Run("tampered first block", func(t *testing.T) {
		t.Parallel()
		broken := []canonical.BlockPayload{first, second}
		broken[0].TipDigest = "sha256:tampered"
		requireInconsistency(t, integrity.VerifyChain(events, broken),
			wantInc{kind: integrity.KindBlock, height: 1, seq: 2, field: integrity.FieldTipDigest,
				stored: "sha256:tampered"})
	})
	t.Run("second block link broken", func(t *testing.T) {
		t.Parallel()
		broken := []canonical.BlockPayload{first, second}
		broken[1].PrevDigest = "sha256:deadbeef"
		requireInconsistency(t, integrity.VerifyChain(events, broken),
			wantInc{kind: integrity.KindBlock, height: 2, seq: 4, field: integrity.FieldPrevDigest,
				stored: "sha256:deadbeef", recomputed: first.Digest})
	})
	t.Run("second block range gap", func(t *testing.T) {
		t.Parallel()
		broken := []canonical.BlockPayload{first, second}
		broken[1].SeqLow = 4
		requireInconsistency(t, integrity.VerifyChain(events, broken),
			wantInc{kind: integrity.KindBlock, height: 2, seq: 4, field: integrity.FieldSeqLow,
				stored: "4", recomputed: "3"})
	})
	t.Run("height gap", func(t *testing.T) {
		t.Parallel()
		broken := []canonical.BlockPayload{first, second}
		broken[1].Height = 3
		requireInconsistency(t, integrity.VerifyChain(events, broken),
			wantInc{kind: integrity.KindBlock, height: 3, seq: 4, field: integrity.FieldHeight,
				stored: "3", recomputed: "2"})
	})
}

func TestSealBlockValidation(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 3)
	tests := []struct {
		name    string
		events  []canonical.EventPayload
		height  int64
		prev    string
		wantErr bool
	}{
		{"empty range", nil, 1, canonical.Genesis, true},
		{"zero height", events, 0, canonical.Genesis, true},
		{"negative height", events, -1, canonical.Genesis, true},
		{"height 1 without genesis", events, 1, "sha256:abcd", true},
		{"height 2 with genesis", events, 2, canonical.Genesis, true},
		{"empty prev digest", events, 1, "", true},
		{"non-contiguous events", []canonical.EventPayload{events[0], events[2]}, 1, canonical.Genesis, true},
		{"valid", events, 1, canonical.Genesis, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			block, err := integrity.SealBlock(tc.events, tc.height, baseTime, tc.prev)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SealBlock = %+v, want error", block)
				}
				return
			}
			if err != nil {
				t.Fatalf("SealBlock: %v", err)
			}
			if block.Digest == "" || block.TipDigest == "" {
				t.Fatalf("sealed block is missing digests: %+v", block)
			}
		})
	}
}

func TestSealEventsValidation(t *testing.T) {
	t.Parallel()
	t.Run("non-contiguous seq", func(t *testing.T) {
		t.Parallel()
		events := buildChain(t, 3)
		events[1].Seq = 7
		if err := integrity.SealEvents(events); err == nil {
			t.Fatal("SealEvents accepted a non-contiguous seq")
		}
	})
	t.Run("non-canonical timestamp", func(t *testing.T) {
		t.Parallel()
		account := "acct"
		bad := []canonical.EventPayload{{
			Seq: 1, AccountID: &account, Kind: "grant", Actor: "a", Reason: "r",
			At: "not a timestamp", ID: "evt",
		}}
		if err := integrity.SealEvents(bad); !errors.Is(err, canonical.ErrTimestamp) {
			t.Fatalf("SealEvents error = %v, want ErrTimestamp", err)
		}
	})
}

func TestAnchors(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 3)
	block := sealOne(t, events)
	blocks := []canonical.BlockPayload{block}

	anchor := integrity.AnchorRecord(block, "/var/lib/bicz/anchors.log", baseTime.Add(time.Hour))
	if anchor.Height != block.Height || anchor.Digest != block.Digest {
		t.Fatalf("anchor = %+v, want height %d digest %s", anchor, block.Height, block.Digest)
	}
	if anchor.At != canonical.Timestamp(baseTime.Add(time.Hour)) {
		t.Fatalf("anchor at = %s", anchor.At)
	}
	if anchor.Destination != "/var/lib/bicz/anchors.log" {
		t.Fatalf("anchor destination = %s", anchor.Destination)
	}
	if err := integrity.VerifyAnchors(blocks, []integrity.AnchorPayload{anchor}); err != nil {
		t.Fatalf("VerifyAnchors = %v, want nil", err)
	}

	tests := []struct {
		name   string
		mutate func(anchor *integrity.AnchorPayload)
		want   wantInc
	}{
		{
			"digest disagrees with the block",
			func(anchor *integrity.AnchorPayload) { anchor.Digest = "sha256:0000" },
			wantInc{kind: integrity.KindAnchor, height: 1, field: integrity.FieldDigest,
				stored: "sha256:0000", recomputed: block.Digest},
		},
		{
			"height newer than the local tip",
			func(anchor *integrity.AnchorPayload) { anchor.Height = 7 },
			wantInc{kind: integrity.KindAnchor, height: 7, field: integrity.FieldHeight,
				stored: "7", recomputed: "<no local block at this height>"},
		},
		{
			"non-canonical at",
			func(anchor *integrity.AnchorPayload) { anchor.At = "2026-10-10T13:00:00Z" },
			wantInc{kind: integrity.KindAnchor, height: 1, field: integrity.FieldAt,
				stored: "2026-10-10T13:00:00Z", recomputed: "<invalid RFC3339 UTC nanosecond timestamp>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			broken := anchor
			tc.mutate(&broken)
			requireInconsistency(t, integrity.VerifyAnchors(blocks, []integrity.AnchorPayload{broken}), tc.want)
		})
	}
}

// Anchors are checked in ascending height regardless of slice order.
func TestVerifyAnchorsOrdersByHeight(t *testing.T) {
	t.Parallel()
	events := buildChain(t, 4)
	first, err := integrity.SealBlock(events[:2], 1, baseTime, canonical.Genesis)
	if err != nil {
		t.Fatalf("SealBlock(1): %v", err)
	}
	second, err := integrity.SealBlock(events[2:], 2, baseTime.Add(time.Minute), first.Digest)
	if err != nil {
		t.Fatalf("SealBlock(2): %v", err)
	}
	blocks := []canonical.BlockPayload{first, second}
	anchors := []integrity.AnchorPayload{
		integrity.AnchorRecord(second, "dest", baseTime.Add(2*time.Hour)),
		integrity.AnchorRecord(first, "dest", baseTime.Add(time.Hour)),
	}
	if err := integrity.VerifyAnchors(blocks, anchors); err != nil {
		t.Fatalf("VerifyAnchors = %v, want nil", err)
	}
	anchors[1].Digest = "sha256:tampered"
	requireInconsistency(t, integrity.VerifyAnchors(blocks, anchors),
		wantInc{kind: integrity.KindAnchor, height: 1, field: integrity.FieldDigest,
			stored: "sha256:tampered", recomputed: first.Digest})
}

func TestInconsistencyErrorFormat(t *testing.T) {
	t.Parallel()
	got := (&integrity.Inconsistency{
		Kind:       integrity.KindEvent,
		Height:     0,
		Seq:        2,
		Field:      integrity.FieldPayloadDigest,
		Stored:     "sha256:aa",
		Recomputed: "sha256:bb",
	}).Error()
	want := "INTEGRITY FAIL kind=event height=0 seq=2 field=payload_digest stored=sha256:aa recomputed=sha256:bb"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
