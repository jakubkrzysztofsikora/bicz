package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
)

// Event kinds written by this package. docs/CONTRACTS.md §2 fixes an initial set
// and leaves the vocabulary open; `price` and `grant` bookkeeping events extend
// it for the operator-price and provisioning commands.
const (
	kindSpend        = "spend"
	kindReward       = "reward"
	kindLoss         = "loss"
	kindCompensation = "compensation"
	kindRole         = "role"
	kindAnchor       = "anchor"
	kindGrant        = "grant"
	kindPrice        = "price"
	kindTask         = "task"
	kindLesson       = "lesson"
	kindReview       = "review"
)

// Event is one decoded `events` row as this package reads it back.
type Event struct {
	ID            string
	Seq           int64
	AccountID     *string
	Kind          string
	Amount        int64
	Payload       string
	Actor         string
	Reason        string
	At            string
	PrevDigest    string
	PayloadDigest string
}

// eventSpec is one pending ledger row.
type eventSpec struct {
	id        string
	accountID *string
	kind      string
	amount    int64
	payload   any
	actor     string
	reason    string
}

// appendEvent writes one event row, advancing seq and linking the hash chain:
// prev_digest is 'genesis' for seq 1 and otherwise the payload_digest of the
// immediately preceding row (docs/CONTRACTS.md §3.2). It must run inside the
// caller's transaction so the event commits atomically with the state change it
// records.
func appendEvent(ctx context.Context, tx *sql.Tx, clock canonical.Clock, spec eventSpec) (int64, string, error) {
	if spec.id == "" {
		return 0, "", errf(ErrInvalidArgument, "event id is empty")
	}
	if spec.actor == "" {
		return 0, "", errf(ErrActorRequired, "event %q", spec.id)
	}
	if spec.reason == "" {
		return 0, "", errf(ErrReasonRequired, "event %q", spec.id)
	}
	payload := "{}"
	if spec.payload != nil {
		encoded, err := canonical.Marshal(spec.payload)
		if err != nil {
			return 0, "", fmt.Errorf("ledger: canonical payload for event %s: %w", spec.id, err)
		}
		payload = string(encoded)
	}
	at := clock.Timestamp()
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM events`).Scan(&seq); err != nil {
		return 0, "", fmt.Errorf("ledger: next event seq: %w", err)
	}
	prev := canonical.Genesis
	if seq > 1 {
		if err := tx.QueryRowContext(ctx, `SELECT payload_digest FROM events WHERE seq = ?`, seq-1).Scan(&prev); err != nil {
			return 0, "", fmt.Errorf("ledger: prev_digest for seq %d: %w", seq, err)
		}
	}
	event := canonical.EventPayload{
		AccountID:  spec.accountID,
		Actor:      spec.actor,
		Amount:     spec.amount,
		At:         at,
		ID:         spec.id,
		Kind:       spec.kind,
		Payload:    payload,
		PrevDigest: prev,
		Reason:     spec.reason,
		Seq:        seq,
	}
	digest, err := canonical.EventDigest(event)
	if err != nil {
		return 0, "", fmt.Errorf("ledger: digest event %s: %w", spec.id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events
		(id, seq, account_id, kind, amount, payload, actor, reason, at, prev_digest, payload_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		spec.id, seq, nullableString(spec.accountID), spec.kind, spec.amount,
		payload, spec.actor, spec.reason, at, prev, digest); err != nil {
		return 0, "", fmt.Errorf("ledger: insert event %s: %w", spec.id, err)
	}
	return seq, digest, nil
}

// loadEvent reads one event by idempotency key.
func loadEvent(ctx context.Context, q querier, id string) (Event, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT id, seq, account_id, kind, amount, payload, actor, reason, at, prev_digest, payload_digest
		FROM events WHERE id = ?`, id)
	var event Event
	err := row.Scan(&event.ID, &event.Seq, &event.AccountID, &event.Kind, &event.Amount,
		&event.Payload, &event.Actor, &event.Reason, &event.At, &event.PrevDigest, &event.PayloadDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("ledger: load event %s: %w", id, err)
	}
	return event, true, nil
}

// decodePayload unmarshals a stored payload into out.
func decodePayload(event Event, out any) error {
	if err := json.Unmarshal([]byte(event.Payload), out); err != nil {
		return errw(ErrPayloadConflict, fmt.Errorf("event %s payload: %w", event.ID, err))
	}
	return nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// Events returns the whole event chain in seq order, ready for
// integrity.VerifyChain. Digest is filled from the stored payload_digest.
func (l *Ledger) Events(ctx context.Context) ([]canonical.EventPayload, error) {
	return readEvents(ctx, l.db)
}

// Blocks returns the whole block chain in height order, ready for
// integrity.VerifyChain / VerifyAnchors.
//
// TipDigest is the stored payload_digest of the event at seq_high — the value
// B(b) commits to (docs/CONTRACTS.md §3.3); the blocks table itself does not
// store it.
func (l *Ledger) Blocks(ctx context.Context) ([]canonical.BlockPayload, error) {
	return readBlocks(ctx, l.db)
}

// Anchors returns the anchor records in height order. The row shape is
// integrity.AnchorPayload, so it feeds integrity.VerifyAnchors directly.
func (l *Ledger) Anchors(ctx context.Context) ([]integrity.AnchorPayload, error) {
	return readAnchors(ctx, l.db)
}

func readEvents(ctx context.Context, q querier) ([]canonical.EventPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, seq, account_id, kind, amount, payload, actor, reason, at, prev_digest, payload_digest
		FROM events ORDER BY seq ASC`)
	if err != nil {
		return nil, fmt.Errorf("ledger: read events: %w", err)
	}
	defer rows.Close()
	out := make([]canonical.EventPayload, 0, 64)
	for rows.Next() {
		var event canonical.EventPayload
		if err := rows.Scan(&event.ID, &event.Seq, &event.AccountID, &event.Kind, &event.Amount,
			&event.Payload, &event.Actor, &event.Reason, &event.At, &event.PrevDigest, &event.Digest); err != nil {
			return nil, fmt.Errorf("ledger: scan event: %w", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read events: %w", err)
	}
	return out, nil
}

func readBlocks(ctx context.Context, q querier) ([]canonical.BlockPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.height, b.digest, b.prev_digest, b.at, b.seq_low, b.seq_high, e.payload_digest
		FROM blocks b JOIN events e ON e.seq = b.seq_high ORDER BY b.height ASC`)
	if err != nil {
		return nil, fmt.Errorf("ledger: read blocks: %w", err)
	}
	defer rows.Close()
	out := make([]canonical.BlockPayload, 0, 16)
	for rows.Next() {
		var block canonical.BlockPayload
		if err := rows.Scan(&block.Height, &block.Digest, &block.PrevDigest, &block.At,
			&block.SeqLow, &block.SeqHigh, &block.TipDigest); err != nil {
			return nil, fmt.Errorf("ledger: scan block: %w", err)
		}
		out = append(out, block)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read blocks: %w", err)
	}
	return out, nil
}

func readAnchors(ctx context.Context, q querier) ([]integrity.AnchorPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT height, digest, at, destination FROM anchors ORDER BY height ASC`)
	if err != nil {
		return nil, fmt.Errorf("ledger: read anchors: %w", err)
	}
	defer rows.Close()
	out := make([]integrity.AnchorPayload, 0, 16)
	for rows.Next() {
		var anchor integrity.AnchorPayload
		if err := rows.Scan(&anchor.Height, &anchor.Digest, &anchor.At, &anchor.Destination); err != nil {
			return nil, fmt.Errorf("ledger: scan anchor: %w", err)
		}
		out = append(out, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read anchors: %w", err)
	}
	return out, nil
}

// ChainStats summarizes the ledger's integrity position for `bicz verify` and
// `bicz doctor`.
type ChainStats struct {
	Height  int64 // highest block height, 0 when no block exists
	Seq     int64 // highest event seq, 0 when the ledger is empty
	Events  int64
	Blocks  int64
	Anchors int64
}

// ChainStats reads the tip positions without decoding the chain.
func (l *Ledger) ChainStats(ctx context.Context) (ChainStats, error) {
	var stats ChainStats
	err := l.db.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT MAX(height) FROM blocks), 0),
		COALESCE((SELECT MAX(seq) FROM events), 0),
		(SELECT COUNT(*) FROM events),
		(SELECT COUNT(*) FROM blocks),
		(SELECT COUNT(*) FROM anchors)`).Scan(&stats.Height, &stats.Seq, &stats.Events, &stats.Blocks, &stats.Anchors)
	if err != nil {
		return ChainStats{}, fmt.Errorf("ledger: read chain stats: %w", err)
	}
	return stats, nil
}

// SchemaVersion is the highest applied migration version.
func (l *Ledger) SchemaVersion(ctx context.Context) (int64, error) {
	return highestApplied(ctx, l.db)
}
