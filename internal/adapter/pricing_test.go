package adapter

import (
	"strings"
	"testing"
)

func TestLoadScheduleValidatesNegativeMoney(t *testing.T) {
	cases := []string{
		`{"tool_max_cost_mc":{"bash":-5}}`,
		`{"default_max_cost_mc":-1}`,
		`{"max_cost_cap_mc":-10}`,
		`{"default_penalty":{"floor_mc":-2}}`,
		`{"penalties":{"r":{"fraction_bp":-3}}}`,
		`{"tool_max_cost_mc":{"":100}}`, // empty tool name refused
		`not json`,
	}
	for _, body := range cases {
		if _, err := LoadSchedule([]byte(body)); err == nil {
			t.Errorf("LoadSchedule(%q): expected error, got none", body)
		}
	}
}

func TestQuoteFromPublishedScheduleEntry(t *testing.T) {
	schedule := &Schedule{
		ToolMaxCostMC: map[string]int64{
			"bash":  250_000,
			"write": 50_000,
			"read":  0, // zero entry is not a price
		},
		MaxCostCapMC: 1_000_000,
	}
	quote := schedule.Quote("bash")
	if quote.State != PriceStateQuoted || quote.Amount != 250_000 {
		t.Fatalf("bash quote = %+v, want quoted 250000", quote)
	}
	if !quote.Priced() {
		t.Fatal("quoted must be Priced()")
	}
}

func TestQuoteCapBinds(t *testing.T) {
	schedule := &Schedule{
		ToolMaxCostMC: map[string]int64{"bash": 5_000_000},
		MaxCostCapMC:  1_000_000,
	}
	quote := schedule.Quote("bash")
	if quote.State != PriceStateQuoted || quote.Amount != 1_000_000 {
		t.Fatalf("capped quote = %+v, want quoted 1000000", quote)
	}
}

func TestQuoteDefaultsConservativelyWithoutToolEntry(t *testing.T) {
	schedule := &Schedule{
		DefaultMaxCostMC: 100_000,
		MaxCostCapMC:     1_000_000,
	}
	quote := schedule.Quote("bash")
	if quote.State != PriceStateDefaulted || quote.Amount != 100_000 {
		t.Fatalf("defaulted quote = %+v, want defaulted 100000", quote)
	}
	if !quote.Priced() {
		t.Fatal("defaulted must be Priced()")
	}
	if !strings.Contains(quote.Reason, "conservative default") {
		t.Fatalf("defaulted quote must disclose the conservative default, reason=%q", quote.Reason)
	}
}

func TestQuoteNoScheduleIsTypedStateNeverAnAmount(t *testing.T) {
	// No schedule published at all.
	quote := (*Schedule)(nil).Quote("bash")
	if quote.State != PriceStateNoSchedule || quote.Amount != 0 || quote.Priced() {
		t.Fatalf("nil schedule quote = %+v, want no_schedule with zero amount and Priced()=false", quote)
	}

	// Schedule published but unusable for this tool: no entry, no default.
	empty := &Schedule{}
	quote = empty.Quote("bash")
	if quote.State != PriceStateNoSchedule || quote.Amount != 0 {
		t.Fatalf("empty schedule quote = %+v, want no_schedule with zero amount", quote)
	}
	if !strings.Contains(quote.Reason, "no published price schedule") {
		t.Fatalf("reason must name the missing schedule, got %q", quote.Reason)
	}
}

func TestQuoteDisclosesDefaulting(t *testing.T) {
	schedule := &Schedule{DefaultMaxCostMC: 42_000}
	quote := schedule.Quote("edit")
	if !strings.Contains(quote.Reason, "no price for edit") {
		t.Fatalf("defaulted quote reason must name the tool, got %q", quote.Reason)
	}
}
