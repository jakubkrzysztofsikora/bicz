package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// Genesis is the prev_digest of the first event (seq = 1) and of the first block
// (height = 1). See docs/CONTRACTS.md §3.2, §3.3.
const Genesis = "genesis"

// DigestPrefix prefixes every digest produced by this package.
const DigestPrefix = "sha256:"

// Domain-separation prefixes, prepended as raw ASCII bytes before the canonical
// encoding is hashed (docs/CONTRACTS.md §3.2, §3.3).
const (
	eventDomain = "bicz-event-v1\n"
	blockDomain = "bicz-block-v1\n"
)

// TimestampLayout is RFC3339 UTC with exactly nine fractional digits and a
// trailing 'Z'.
const TimestampLayout = "2006-01-02T15:04:05.000000000Z"

// ErrTimestamp is returned for a timestamp that is not RFC3339 UTC with exactly
// nine fractional digits and a trailing 'Z'.
var ErrTimestamp = errors.New("canonical: timestamp must be RFC3339 UTC with exactly nine fractional digits and a trailing Z")

// Timestamp formats t as the canonical timestamp: RFC3339 UTC, nanosecond
// precision, trailing 'Z'. The instant is converted to UTC first, so a local
// offset can never leak into a digest.
func Timestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}

// ValidateTimestamp reports whether s is a canonical timestamp (the only shape
// accepted inside hashed structures).
func ValidateTimestamp(s string) error {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrTimestamp, s)
	}
	if Timestamp(t) != s {
		return fmt.Errorf("%w: %q", ErrTimestamp, s)
	}
	return nil
}

// Clock is an injectable time source. Hashing is testable because every
// timestamp that enters a digest is either produced by a Clock or passed in
// explicitly by the caller.
//
//	h := sha256.Sum256([]byte(...))  // no wall clock anywhere in the digest path
//	at := clock.Now()                // injected here, once
type Clock func() time.Time

// Now returns the clock's current time, converted to UTC.
func (c Clock) Now() time.Time { return c().UTC() }

// Timestamp returns Now() formatted with Timestamp.
func (c Clock) Timestamp() string { return Timestamp(c.Now()) }

// System is the default clock: the host wall clock, normalized to UTC.
var System = Clock(func() time.Time { return time.Now().UTC() })

// EventPayload is the canonical form of one `events` row.
//
// Every field except Digest is part of E(e) (docs/CONTRACTS.md §3.2) and is
// therefore committed by EventDigest. AccountID is a pointer because the column
// is nullable; a nil pointer encodes as JSON null.
//
// Digest carries the stored `events.payload_digest` column. It is deliberately
// excluded from E(e) (tag `json:"-"`), so it never changes the bytes that are
// hashed; VerifyChain uses it to locate the first tampered event exactly.
type EventPayload struct {
	AccountID  *string `json:"account_id"`
	Actor      string  `json:"actor"`
	Amount     int64   `json:"amount"`
	At         string  `json:"at"`
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Payload    string  `json:"payload"`
	PrevDigest string  `json:"prev_digest"`
	Reason     string  `json:"reason"`
	Seq        int64   `json:"seq"`
	Digest     string  `json:"-"`
}

// BlockPayload is the canonical form of one `blocks` row.
//
// Every field except Digest is part of B(b) (docs/CONTRACTS.md §3.3) and is
// therefore committed by BlockDigest. Digest carries the stored
// `blocks.digest` column and is excluded from B(b).
type BlockPayload struct {
	At         string `json:"at"`
	Height     int64  `json:"height"`
	PrevDigest string `json:"prev_digest"`
	SeqHigh    int64  `json:"seq_high"`
	SeqLow     int64  `json:"seq_low"`
	TipDigest  string `json:"tip_digest"`
	Digest     string `json:"-"`
}

// EventCanonical returns E(e): the exact bytes hashed by EventDigest.
func (e EventPayload) EventCanonical() ([]byte, error) {
	if err := ValidateTimestamp(e.At); err != nil {
		return nil, err
	}
	return Marshal(e)
}

// BlockCanonical returns B(b): the exact bytes hashed by BlockDigest.
func (b BlockPayload) BlockCanonical() ([]byte, error) {
	if err := ValidateTimestamp(b.At); err != nil {
		return nil, err
	}
	return Marshal(b)
}

// EventDigest returns the payload digest of e:
//
//	"sha256:" + lowercase_hex(SHA256(ASCII("bicz-event-v1\n") || UTF8(E(e))))
func EventDigest(e EventPayload) (string, error) {
	body, err := e.EventCanonical()
	if err != nil {
		return "", err
	}
	return domainDigest(eventDomain, body), nil
}

// BlockDigest returns the digest of b:
//
//	"sha256:" + lowercase_hex(SHA256(ASCII("bicz-block-v1\n") || UTF8(B(b))))
func BlockDigest(b BlockPayload) (string, error) {
	body, err := b.BlockCanonical()
	if err != nil {
		return "", err
	}
	return domainDigest(blockDomain, body), nil
}

func domainDigest(domain string, body []byte) string {
	h := sha256.New()
	io.WriteString(h, domain)
	h.Write(body)
	return DigestPrefix + hex.EncodeToString(h.Sum(nil))
}
