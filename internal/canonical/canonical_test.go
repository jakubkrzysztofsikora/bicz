package canonical_test

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
)

// Golden event fixture. The expected bytes are the E(e) of docs/CONTRACTS.md
// §3.2 with keys in UTF-8 byte order:
//
//	account_id, actor, amount, at, id, kind, payload, prev_digest, reason, seq
//
// and the expected digest was derived independently with:
//
//	printf 'bicz-event-v1\n%s' '<goldenEventJSON>' | shasum -a 256
const (
	goldenEventJSON   = `{"account_id":"acct_demo","actor":"jakub","amount":1000000,"at":"2026-10-10T12:00:00.123456789Z","id":"evt_0001","kind":"grant","payload":"{\"policy_version\":\"1\"}","prev_digest":"genesis","reason":"provisioning","seq":1}`
	goldenEventDigest = "sha256:dff2789e63c665302d4c5bfa6c428f48bbe8ed753bcfe5f9bda70bea74467160"

	// Block 1 sealing only that event; B(b) keys in UTF-8 byte order:
	// at, height, prev_digest, seq_high, seq_low, tip_digest.
	// Derived with: printf 'bicz-block-v1\n%s' '<goldenBlockJSON>' | shasum -a 256
	goldenBlockJSON   = `{"at":"2026-10-10T12:00:05.250000000Z","height":1,"prev_digest":"genesis","seq_high":1,"seq_low":1,"tip_digest":"sha256:dff2789e63c665302d4c5bfa6c428f48bbe8ed753bcfe5f9bda70bea74467160"}`
	goldenBlockDigest = "sha256:45313b3c2e7c8589854a3daada36da532fbe67bd811d70115a66f0b7029898ef"
)

func goldenEvent() canonical.EventPayload {
	account := "acct_demo"
	return canonical.EventPayload{
		Seq:        1,
		AccountID:  &account,
		Kind:       "grant",
		Amount:     1000000,
		Payload:    `{"policy_version":"1"}`,
		Actor:      "jakub",
		Reason:     "provisioning",
		At:         "2026-10-10T12:00:00.123456789Z",
		PrevDigest: canonical.Genesis,
		ID:         "evt_0001",
		Digest:     "sha256:this-stored-value-must-not-affect-the-bytes",
	}
}

func goldenBlock() canonical.BlockPayload {
	return canonical.BlockPayload{
		Height:     1,
		At:         "2026-10-10T12:00:05.250000000Z",
		PrevDigest: canonical.Genesis,
		SeqLow:     1,
		SeqHigh:    1,
		TipDigest:  goldenEventDigest,
		Digest:     "sha256:this-stored-value-must-not-affect-the-bytes",
	}
}

func TestMarshalGoldenEvent(t *testing.T) {
	t.Parallel()
	got, err := canonical.Marshal(goldenEvent())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != goldenEventJSON {
		t.Fatalf("golden bytes mismatch\n got: %s\nwant: %s", got, goldenEventJSON)
	}
	digest, err := canonical.EventDigest(goldenEvent())
	if err != nil {
		t.Fatalf("EventDigest: %v", err)
	}
	if digest != goldenEventDigest {
		t.Fatalf("golden digest mismatch\n got: %s\nwant: %s", digest, goldenEventDigest)
	}
}

func TestMarshalGoldenBlock(t *testing.T) {
	t.Parallel()
	got, err := canonical.Marshal(goldenBlock())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != goldenBlockJSON {
		t.Fatalf("golden bytes mismatch\n got: %s\nwant: %s", got, goldenBlockJSON)
	}
	digest, err := canonical.BlockDigest(goldenBlock())
	if err != nil {
		t.Fatalf("BlockDigest: %v", err)
	}
	if digest != goldenBlockDigest {
		t.Fatalf("golden digest mismatch\n got: %s\nwant: %s", digest, goldenBlockDigest)
	}
}

// The stored Digest field is not part of E(e)/B(b), so it must not move a byte.
func TestStoredDigestFieldIsNotHashed(t *testing.T) {
	t.Parallel()
	event := goldenEvent()
	event.Digest = ""
	body, err := canonical.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(body) != goldenEventJSON {
		t.Fatalf("stored digest changed the event bytes: %s", body)
	}
	digest, err := canonical.EventDigest(event)
	if err != nil {
		t.Fatalf("EventDigest: %v", err)
	}
	if digest != goldenEventDigest {
		t.Fatalf("stored digest changed the event digest: %s", digest)
	}

	block := goldenBlock()
	block.Digest = ""
	body, err = canonical.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(body) != goldenBlockJSON {
		t.Fatalf("stored digest changed the block bytes: %s", body)
	}
	blockDigest, err := canonical.BlockDigest(block)
	if err != nil {
		t.Fatalf("BlockDigest: %v", err)
	}
	if blockDigest != goldenBlockDigest {
		t.Fatalf("stored digest changed the block digest: %s", blockDigest)
	}
}

// E(e) commits to exactly the §3.2 field set — no more, no less.
func TestEventCanonicalFieldSet(t *testing.T) {
	t.Parallel()
	body, err := canonical.Marshal(goldenEvent())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("golden bytes are not an object: %v", err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"account_id", "actor", "amount", "at", "id", "kind", "payload", "prev_digest", "reason", "seq"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("E(e) keys = %v, want %v", got, want)
	}
}

func TestMarshalScalars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "null"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", int(-7), "-7"},
		{"int8", int8(127), "127"},
		{"int64 min", int64(-9223372036854775808), "-9223372036854775808"},
		{"int64 max", int64(9223372036854775807), "9223372036854775807"},
		{"uint32", uint32(4294967295), "4294967295"},
		{"string", "hi", `"hi"`},
		{"empty object", map[string]any{}, "{}"},
		{"empty array", []any{}, "[]"},
		{"mixed array", []any{int64(1), "a", true, nil}, `[1,"a",true,null]`},
		{"sorted map", map[string]any{"b": int64(1), "a": int64(2)}, `{"a":2,"b":1}`},
		{"raw message", json.RawMessage(`{"b": 1, "a": [3, 2]}`), `{"a":[3,2],"b":1}`},
		{"json number", json.Number("42"), "42"},
		{"nested nil pointer", (*string)(nil), "null"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonical.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal(%v): %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("Marshal(%v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarshalKeyOrderIndependence(t *testing.T) {
	t.Parallel()
	type forward struct {
		AccountID *string `json:"account_id"`
		Amount    int64   `json:"amount"`
		At        string  `json:"at"`
		Seq       int64   `json:"seq"`
	}
	type reverse struct {
		Seq       int64   `json:"seq"`
		At        string  `json:"at"`
		Amount    int64   `json:"amount"`
		AccountID *string `json:"account_id"`
	}
	account := "acct"
	a := forward{AccountID: &account, Amount: 5, At: "t", Seq: 1}
	b := reverse{Seq: 1, At: "t", Amount: 5, AccountID: &account}
	left, err := canonical.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal(forward): %v", err)
	}
	right, err := canonical.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal(reverse): %v", err)
	}
	if string(left) != string(right) {
		t.Fatalf("field declaration order changed the bytes: %s vs %s", left, right)
	}
	if string(left) != `{"account_id":"acct","amount":5,"at":"t","seq":1}` {
		t.Fatalf("unexpected bytes: %s", left)
	}

	first := map[string]any{}
	first["z"] = int64(1)
	first["a"] = int64(2)
	first["m"] = int64(3)
	second := map[string]any{}
	second["m"] = int64(3)
	second["z"] = int64(1)
	second["a"] = int64(2)
	leftMap, err := canonical.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal(map1): %v", err)
	}
	rightMap, err := canonical.Marshal(second)
	if err != nil {
		t.Fatalf("Marshal(map2): %v", err)
	}
	if string(leftMap) != string(rightMap) || string(leftMap) != `{"a":2,"m":3,"z":1}` {
		t.Fatalf("map iteration order affected the bytes: %s vs %s", leftMap, rightMap)
	}
}

func TestMarshalRejectsFloats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
	}{
		{"float64", 1.5},
		{"float64 integral", float64(1)},
		{"float64 negative zero", float64(-0.0)},
		{"float32", float32(2.5)},
		{"raw decimal", json.RawMessage(`{"a":1.0}`)},
		{"raw exponent", json.RawMessage(`{"a":1e3}`)},
		{"raw float number", json.Number("1.5")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := canonical.Marshal(tc.in)
			if !errors.Is(err, canonical.ErrFloat) {
				t.Fatalf("Marshal(%v) error = %v, want ErrFloat", tc.in, err)
			}
		})
	}
}

func TestMarshalRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	t.Run("raw object", func(t *testing.T) {
		t.Parallel()
		_, err := canonical.Marshal(json.RawMessage(`{"a":1,"a":2}`))
		if !errors.Is(err, canonical.ErrDuplicateKey) {
			t.Fatalf("error = %v, want ErrDuplicateKey", err)
		}
	})
	t.Run("nested raw object", func(t *testing.T) {
		t.Parallel()
		_, err := canonical.Marshal(json.RawMessage(`{"a":{"x":1,"x":2}}`))
		if !errors.Is(err, canonical.ErrDuplicateKey) {
			t.Fatalf("error = %v, want ErrDuplicateKey", err)
		}
	})
	t.Run("struct fields", func(t *testing.T) {
		t.Parallel()
		// Field A's json tag collides with field B's implicit name.
		type dup struct {
			A int64 `json:"B"`
			B int64
		}
		_, err := canonical.Marshal(dup{A: 1, B: 2})
		if !errors.Is(err, canonical.ErrDuplicateKey) {
			t.Fatalf("error = %v, want ErrDuplicateKey", err)
		}
	})
}

func TestMarshalNegativeZero(t *testing.T) {
	t.Parallel()
	t.Run("raw -0 rejected", func(t *testing.T) {
		t.Parallel()
		_, err := canonical.Marshal(json.RawMessage(`{"a":-0}`))
		if !errors.Is(err, canonical.ErrInteger) {
			t.Fatalf("error = %v, want ErrInteger", err)
		}
	})
	t.Run("raw 0 accepted", func(t *testing.T) {
		t.Parallel()
		got, err := canonical.Marshal(json.RawMessage(`{"a":0}`))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(got) != `{"a":0}` {
			t.Fatalf("got %s, want {\"a\":0}", got)
		}
	})
	t.Run("json number -0 rejected", func(t *testing.T) {
		t.Parallel()
		_, err := canonical.Marshal(json.Number("-0"))
		if !errors.Is(err, canonical.ErrInteger) {
			t.Fatalf("error = %v, want ErrInteger", err)
		}
	})
	t.Run("leading zeros rejected", func(t *testing.T) {
		t.Parallel()
		for _, in := range []any{json.Number("00"), json.Number("01")} {
			if _, err := canonical.Marshal(in); !errors.Is(err, canonical.ErrInteger) {
				t.Fatalf("Marshal(%v) error = %v, want ErrInteger", in, err)
			}
		}
	})
	t.Run("leading plus rejected", func(t *testing.T) {
		t.Parallel()
		for _, in := range []any{json.Number("+1")} {
			if _, err := canonical.Marshal(in); !errors.Is(err, canonical.ErrInteger) {
				t.Fatalf("Marshal(%v) error = %v, want ErrInteger", in, err)
			}
		}
	})
	t.Run("malformed raw json rejected", func(t *testing.T) {
		t.Parallel()
		for _, in := range []string{`{"a":+1}`, `{"a":00}`, `{"a":1`, `{"a":1}{"b":2}`, ``} {
			if _, err := canonical.Marshal(json.RawMessage(in)); err == nil {
				t.Fatalf("Marshal(%s) = nil error, want a JSON error", in)
			}
		}
	})
	t.Run("out of range rejected", func(t *testing.T) {
		t.Parallel()
		for _, in := range []any{json.Number("9223372036854775808"), uint64(9223372036854775808)} {
			if _, err := canonical.Marshal(in); !errors.Is(err, canonical.ErrInteger) {
				t.Fatalf("Marshal(%v) error = %v, want ErrInteger", in, err)
			}
		}
	})
}

func TestMarshalStringEscaping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"control code points", "\x00\x01\x1f", `"\u0000\u0001\u001f"`},
		{"mandatory short escapes", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"quote and backslash", `"\`, `"\"\\"`},
		{"solidus is literal", "a/b", `"a/b"`},
		{"del is literal", "\x7f", "\"\x7f\""},
		{"unicode passthrough", "héllo😀\u2028\u2029", "\"héllo😀\u2028\u2029\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonical.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal(%q): %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("Marshal(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
	t.Run("invalid utf-8 rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := canonical.Marshal("\xff"); !errors.Is(err, canonical.ErrUTF8) {
			t.Fatalf("error = %v, want ErrUTF8", err)
		}
	})
}

func TestMarshalRejectsUnsupportedTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
	}{
		{"time", time.Now()},
		{"chan", make(chan int)},
		{"func", func() {}},
		{"complex", complex(1, 2)},
		{"int keyed map", map[int]string{1: "a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := canonical.Marshal(tc.in); !errors.Is(err, canonical.ErrUnsupported) {
				t.Fatalf("Marshal(%T) error = %v, want ErrUnsupported", tc.in, err)
			}
		})
	}
}

var canonicalTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`)

func TestTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			"utc nanos",
			time.Date(2026, 10, 10, 12, 0, 0, 123456789, time.UTC),
			"2026-10-10T12:00:00.123456789Z",
		},
		{
			"zero nanos keeps nine digits",
			time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
			"2026-10-10T12:00:00.000000000Z",
		},
		{
			"offset is normalized to utc",
			time.Date(2026, 10, 10, 14, 0, 0, 123456789, time.FixedZone("UTC+2", 2*3600)),
			"2026-10-10T12:00:00.123456789Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonical.Timestamp(tc.in)
			if got != tc.want {
				t.Fatalf("Timestamp(%v) = %s, want %s", tc.in, got, tc.want)
			}
			if !canonicalTimestamp.MatchString(got) {
				t.Fatalf("Timestamp(%v) = %s is not RFC3339 UTC with nine fractional digits", tc.in, got)
			}
		})
	}
}

func TestValidateTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"canonical", "2026-10-10T12:00:00.123456789Z", true},
		{"zero nanos", "2026-10-10T12:00:00.000000000Z", true},
		{"empty", "", false},
		{"no fraction", "2026-10-10T12:00:00Z", false},
		{"three digits", "2026-10-10T12:00:00.123Z", false},
		{"ten digits", "2026-10-10T12:00:00.1234567890Z", false},
		{"offset", "2026-10-10T12:00:00.123456789+00:00", false},
		{"lowercase z", "2026-10-10T12:00:00.123456789z", false},
		{"space separator", "2026-10-10 12:00:00.123456789Z", false},
		{"leap second", "2026-10-10T12:00:60.000000000Z", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := canonical.ValidateTimestamp(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateTimestamp(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok && !errors.Is(err, canonical.ErrTimestamp) {
				t.Fatalf("ValidateTimestamp(%q) = %v, want ErrTimestamp", tc.in, err)
			}
		})
	}
}

func TestClockInjection(t *testing.T) {
	t.Parallel()
	fixed := canonical.Clock(func() time.Time {
		return time.Date(2026, 10, 10, 12, 0, 5, 250000000, time.UTC)
	})
	if got, want := fixed.Timestamp(), "2026-10-10T12:00:05.250000000Z"; got != want {
		t.Fatalf("fixed clock Timestamp() = %s, want %s", got, want)
	}
	if got := fixed.Now(); got.Location() != time.UTC {
		t.Fatalf("Now() location = %v, want UTC", got.Location())
	}
	offset := canonical.Clock(func() time.Time {
		return time.Date(2026, 10, 10, 14, 0, 5, 250000000, time.FixedZone("UTC+2", 2*3600))
	})
	if got, want := offset.Timestamp(), "2026-10-10T12:00:05.250000000Z"; got != want {
		t.Fatalf("offset clock Timestamp() = %s, want %s", got, want)
	}
	if got := canonical.System.Now(); got.Location() != time.UTC {
		t.Fatalf("System.Now() location = %v, want UTC", got.Location())
	}
}

func TestEventDigestRejectsNonCanonicalTimestamp(t *testing.T) {
	t.Parallel()
	event := goldenEvent()
	event.At = "2026-10-10T12:00:00Z"
	if _, err := canonical.EventDigest(event); !errors.Is(err, canonical.ErrTimestamp) {
		t.Fatalf("EventDigest error = %v, want ErrTimestamp", err)
	}
	block := goldenBlock()
	block.At = "2026-10-10T12:00:05+00:00"
	if _, err := canonical.BlockDigest(block); !errors.Is(err, canonical.ErrTimestamp) {
		t.Fatalf("BlockDigest error = %v, want ErrTimestamp", err)
	}
}

func TestNullAccountIDEncodesAsNull(t *testing.T) {
	t.Parallel()
	event := goldenEvent()
	event.AccountID = nil
	body, err := canonical.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(body), `"account_id":null`) {
		t.Fatalf("nil account_id should encode as null: %s", body)
	}
}
