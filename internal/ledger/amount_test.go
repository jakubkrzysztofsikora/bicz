package ledger

import "testing"

func TestParseCreditsIsExactFixedSixDigitDecimal(t *testing.T) {
	good := map[string]int64{
		"5": 5_000_000, "0.25": 250_000, "0.000001": 1, "12.345678": 12_345_678,
		"007": 7_000_000, "9223372036854": 9_223_372_036_854_000_000,
	}
	for text, want := range good {
		if got, err := ParseCredits(text); err != nil || got != want {
			t.Errorf("ParseCredits(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	// Every refusal here is a way a human could misread "credits" as microcredits
	// or silently round money.
	for _, text := range []string{"", "0", "0.0", "0.000000", "-5", "+5", "1e6", "1E6", "0x10", "1_000", "1,000", " 5", "5 ", "5.", ".5",
		"1.0000001", "NaN", "Inf", "5.5.5", "９", "9223372036855", "99999999999999999999"} {
		if got, err := ParseCredits(text); err == nil {
			t.Errorf("ParseCredits(%q) = %d; want an error", text, got)
		}
	}
}
