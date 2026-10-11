package ledger

import (
	"math"
	"strconv"
	"strings"
)

// Money in bicz is an integer count of microcredits, and only that. There is no
// float anywhere on a money path: docs/CONTRACTS.md §1 rejects booleans, floats,
// and non-integers at the protocol boundary, so the boundary is here.
//
// ParseMicrocredits accepts exactly the base-10 integer literal grammar
// (optional leading '-', then digits) and rejects everything else — "1.5",
// "1e6", "true", "1_000", "0x10", and empty strings all fail with
// ErrInvalidAmount.
func ParseMicrocredits(text string) (int64, error) {
	if text == "" {
		return 0, errf(ErrInvalidAmount, "empty amount")
	}
	for i := range len(text) {
		c := text[i]
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			return 0, errf(ErrInvalidAmount, "%q is not a base-10 integer count of microcredits", text)
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, errw(ErrInvalidAmount, err)
	}
	return value, nil
}

// ParseCreditAmount parses a strictly positive microcredit amount. Zero and
// negatives are refused: a reservation, reward, compensation, or requested burn
// is meaningless without a positive amount.
func ParseCreditAmount(text string) (int64, error) {
	value, err := ParseMicrocredits(text)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, errf(ErrInvalidAmount, "%d is not a positive amount", value)
	}
	return value, nil
}

// ParseCredits parses a human credit amount ("5", "0.25", "12.345678") into
// integer microcredits without ever touching a float: digits, then optionally a
// '.' and one to six fractional digits. Everything else is refused — no sign, no
// exponent, no thousands separator, no seventh decimal (which would silently
// round), no bare ".5" or "5.", and nothing that overflows int64. The result is
// strictly positive.
func ParseCredits(text string) (int64, error) {
	whole, frac, hasDot := strings.Cut(text, ".")
	if whole == "" || (hasDot && (frac == "" || len(frac) > 6)) {
		return 0, errf(ErrInvalidAmount, "%q is not a credit amount: use digits with at most 6 decimals, e.g. 5 or 0.25", text)
	}
	for _, part := range []string{whole, frac} {
		for i := range len(part) {
			if part[i] < '0' || part[i] > '9' {
				return 0, errf(ErrInvalidAmount, "%q is not a credit amount: use digits with at most 6 decimals, e.g. 5 or 0.25", text)
			}
		}
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || units > math.MaxInt64/1_000_000 {
		return 0, errf(ErrInvalidAmount, "%q is too large", text)
	}
	micro := units * 1_000_000
	if frac != "" {
		padded := frac + strings.Repeat("0", 6-len(frac))
		part, err := strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, errw(ErrInvalidAmount, err)
		}
		micro += part
	}
	if micro <= 0 {
		return 0, errf(ErrInvalidAmount, "%q is not a positive amount", text)
	}
	return micro, nil
}
