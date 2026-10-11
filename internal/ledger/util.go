package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// fingerprint is a short, stable digest of an id set, used to derive a
// deterministic event id for a bulk operation: a retry that touches the same
// set produces the same key, so the retry is an exact replay rather than a new
// event.
func fingerprint(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:8])
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// ceilDiv divides non-negative numerator by a positive denominator, rounding up.
// It is the integer-only replacement for ceil(x/y) on the money path, so no
// float ever touches a penalty.
func ceilDiv(numerator, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	if numerator <= 0 {
		return 0
	}
	return (numerator + denominator - 1) / denominator
}

func minInt64(values ...int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maxInt64(values ...int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
