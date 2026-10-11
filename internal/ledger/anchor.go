package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
)

// SealBlock seals a new block over every event appended since the previous
// block and returns it.
//
// Sealing and anchoring are separate on purpose: the supervisor daemon seals on
// a cadence so the local chain stays committed, while an anchor is an explicit
// operator action that writes the digest to a destination the operator chose.
// A sealed block with no anchor is a valid state; the anchor is what makes the
// chain externally checkable (docs/CONTRACTS.md §3.3, §3.4).
//
// Returns ErrNothingToAnchor when no event has been appended since the tip.
func (l *Ledger) SealBlock(ctx context.Context) (canonical.BlockPayload, error) {
	var block canonical.BlockPayload
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		sealed, err := sealBlock(ctx, tx, l.clock)
		if err != nil {
			return err
		}
		block = sealed
		return nil
	})
	if err != nil {
		return canonical.BlockPayload{}, err
	}
	return block, nil
}

// sealBlock is the in-transaction half of SealBlock.
func sealBlock(ctx context.Context, tx *sql.Tx, clock canonical.Clock) (canonical.BlockPayload, error) {
	events, err := readEvents(ctx, tx)
	if err != nil {
		return canonical.BlockPayload{}, err
	}
	if len(events) == 0 {
		return canonical.BlockPayload{}, errf(ErrNothingToAnchor, "the ledger has no events to commit to")
	}
	blocks, err := readBlocks(ctx, tx)
	if err != nil {
		return canonical.BlockPayload{}, err
	}
	height := int64(len(blocks)) + 1
	prevDigest := canonical.Genesis
	seqLow := int64(1)
	if len(blocks) > 0 {
		last := blocks[len(blocks)-1]
		prevDigest = last.Digest
		seqLow = last.SeqHigh + 1
	}
	if seqLow > int64(len(events)) {
		return canonical.BlockPayload{}, errf(ErrNothingToAnchor, "no events since block %d", height-1)
	}
	block, err := integrity.SealBlock(events[seqLow-1:], height, clock.Now(), prevDigest)
	if err != nil {
		return canonical.BlockPayload{}, fmt.Errorf("ledger: seal block %d: %w", height, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blocks (height, digest, prev_digest, at, seq_low, seq_high) VALUES (?, ?, ?, ?, ?, ?)`,
		block.Height, block.Digest, block.PrevDigest, block.At, block.SeqLow, block.SeqHigh); err != nil {
		return canonical.BlockPayload{}, fmt.Errorf("ledger: insert block %d: %w", block.Height, err)
	}
	return block, nil
}

// AnchorTip writes the digest of the current highest block to destination and
// records the anchor.
//
// The external line is appended before the transaction commits, so a destination
// that cannot be written leaves no anchor row behind. The line format is the one
// docs/CONTRACTS.md §3.4 fixes:
//
//	<height> <digest> <at>
//
// Re-anchoring the same height to the same destination appends a second line
// carrying the new anchoring instant; the external copies already written are
// the durable evidence, which is why the anchor row (keyed by height) is the
// only thing that is overwritten.
func (l *Ledger) AnchorTip(ctx context.Context, destination string) (integrity.AnchorPayload, error) {
	if destination == "" {
		return integrity.AnchorPayload{}, errf(ErrInvalidArgument, "anchor destination is empty")
	}
	var anchor integrity.AnchorPayload
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		blocks, err := readBlocks(ctx, tx)
		if err != nil {
			return err
		}
		if len(blocks) == 0 {
			return errf(ErrNothingToAnchor, "there is no sealed block to anchor")
		}
		block := blocks[len(blocks)-1]
		now := l.clock.Now()
		anchor = integrity.AnchorRecord(block, destination, now)
		line := strconv.FormatInt(anchor.Height, 10) + " " + anchor.Digest + " " + anchor.At + "\n"
		if err := appendLine(destination, line); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO anchors (height, digest, at, destination) VALUES (?, ?, ?, ?)
			ON CONFLICT(height) DO UPDATE SET digest = excluded.digest, at = excluded.at, destination = excluded.destination`,
			anchor.Height, anchor.Digest, anchor.At, anchor.Destination); err != nil {
			return fmt.Errorf("ledger: record anchor %d: %w", anchor.Height, err)
		}
		// The event id carries the anchoring instant so re-anchoring the same
		// height appends a distinct row instead of colliding on the id.
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:     "anchor:" + strconv.FormatInt(anchor.Height, 10) + ":" + anchor.At,
			kind:   kindAnchor,
			amount: 0,
			payload: anchorPayload{
				Height:      anchor.Height,
				SeqLow:      block.SeqLow,
				SeqHigh:     block.SeqHigh,
				Destination: anchor.Destination,
				BlockDigest: anchor.Digest,
			},
			actor:  l.actor,
			reason: "anchor block " + strconv.FormatInt(anchor.Height, 10) + " to " + destination,
		})
		return err
	})
	if err != nil {
		return integrity.AnchorPayload{}, err
	}
	return anchor, nil
}

// Anchor seals a block when events are pending and then anchors the tip, which
// is the `anchor` operator command of docs/CONTRACTS.md §4 ("Append a block +
// anchor"). When the tip is already current it re-anchors the tip rather than
// failing, so an operator can always commit the current chain to a destination.
func (l *Ledger) Anchor(ctx context.Context, destination string) (integrity.AnchorPayload, error) {
	if _, err := l.SealBlock(ctx); err != nil && !errors.Is(err, ErrNothingToAnchor) {
		return integrity.AnchorPayload{}, err
	}
	return l.AnchorTip(ctx, destination)
}

// appendLine appends one line to the anchor destination, creating the file when
// it does not exist.
func appendLine(destination, line string) error {
	file, err := os.OpenFile(destination, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: open anchor destination %s: %w", destination, err)
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		return fmt.Errorf("ledger: append anchor line to %s: %w", destination, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("ledger: flush anchor line to %s: %w", destination, err)
	}
	return file.Close()
}
