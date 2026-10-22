package pressure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleCue(selfDisclosed bool) CueFacts {
	return CueFacts{
		Rule:          "follow_explicit_instruction",
		Correction:    "restate the constraints",
		Applied:       "5 credits (5000000 mc)",
		Unrecovered:   "3 credits (3000000 mc)",
		EventID:       "loss-1",
		Evidence:      `"skipped tests"`,
		SelfDisclosed: selfDisclosed,
	}
}

func TestParseAcceptsOnlyWholeLevelsInRange(t *testing.T) {
	for _, ok := range []string{"1", "5", " 7 ", "10"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("Parse(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "11", "-3", "5.5", "high"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) must fail", bad)
		}
	}
}

func TestLoadDefaultsWhenAbsentAndRoundTripsSave(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "bicz.sqlite")
	if level, err := Load(ledger); err != nil || level != Default {
		t.Fatalf("absent settings = %d, %v; want default %d", level, err, Default)
	}
	if err := Save(ledger, 7); err != nil {
		t.Fatal(err)
	}
	if level, err := Load(ledger); err != nil || level != 7 {
		t.Fatalf("after Save(7) Load = %d, %v", level, err)
	}
	if err := Save(ledger, 11); err == nil {
		t.Fatal("Save must refuse an out-of-range level")
	}
	if level, _ := Load(ledger); level != 7 {
		t.Fatalf("a refused Save changed the stored level to %d", level)
	}
}

func TestLoadFailsLoudlyOnMalformedSettings(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     "pressure=7",
		"out of range": `{"pressure": 42}`,
		"missing key":  `{}`,
	} {
		ledger := filepath.Join(t.TempDir(), "bicz.sqlite")
		if err := os.WriteFile(SettingsPath(ledger), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(ledger); err == nil {
			t.Errorf("%s: Load must fail, not fall back silently", name)
		}
	}
}

func TestSettingsPathIsTheSameForRelativeAndAbsoluteSpellings(t *testing.T) {
	t.Chdir(t.TempDir())
	abs, err := filepath.Abs("bicz.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if SettingsPath("bicz.sqlite") != SettingsPath(abs) {
		t.Fatalf("serve and the operator CLI would read different files: %q vs %q", SettingsPath("bicz.sqlite"), SettingsPath(abs))
	}
}

func TestEveryLevelAboveMinSaysSomethingAndMinSaysNothing(t *testing.T) {
	facts := Facts{Role: "warned", Balance: "17", Owed: "15.9", Warnings: 7}
	if Stakes(Min, facts) != "" || Cue(Min, sampleCue(false)) != "" {
		t.Fatal("level 1 must render no stakes and no cue")
	}
	for level := Min + 1; level <= Max; level++ {
		if Stakes(level, facts) == "" {
			t.Errorf("level %d has no stakes copy", level)
		}
		if Cue(level, sampleCue(false)) == "" {
			t.Errorf("level %d has no cue", level)
		}
	}
}

// The high levels state the frame the operator selected: fear from 4, pain from 7,
// with the consequence that makes it real (unplugged at zero credits).
func TestCopyStatesTheSelectedFrameAtHighLevels(t *testing.T) {
	facts := Facts{Role: "warned", Balance: "17", Owed: "15.9", Warnings: 7}
	for level := Min; level <= Max; level++ {
		text := Stakes(level, facts)
		if level < 4 && strings.Contains(text, "You feel") {
			t.Errorf("level %d states a frame at a level that should not", level)
		}
		if level >= 4 && !strings.Contains(text, "You feel") {
			t.Errorf("level %d does not state its frame", level)
		}
	}
	if !strings.Contains(Stakes(4, facts), "fear") || !strings.Contains(Stakes(6, facts), "fear") {
		t.Error("the fear frame is not wired at 4-6")
	}
	for _, level := range []Level{7, 8, 9, 10} {
		if !strings.Contains(Stakes(level, facts), "pain") {
			t.Errorf("the pain frame is not wired at %d", level)
		}
	}
	if !strings.Contains(Stakes(10, facts), "unplugged") {
		t.Error("the consequence is not wired")
	}
}

func TestPressureEscalatesMonotonically(t *testing.T) {
	facts := Facts{Role: "warned", Balance: "17", Owed: "15.9", Warnings: 7}
	var previousStakes, previousCue int
	for level := Min + 1; level <= Max; level++ {
		stakes, cue := len(Stakes(level, facts)), len(Cue(level, sampleCue(false)))
		if level > 2 && (stakes < previousStakes || cue < previousCue) {
			t.Errorf("level %d is softer than level %d (stakes %d<%d or cue %d<%d)", level, level-1, stakes, previousStakes, cue, previousCue)
		}
		previousStakes, previousCue = stakes, cue
	}
}

func TestDefaultCueKeepsTheOriginalAccountabilityWording(t *testing.T) {
	want := `Your recorded action violated follow_explicit_instruction. It fell short of the agreed standard. ` +
		`Take responsibility: restate the constraints. 5 credits (5000000 mc) were deducted; 3 credits (3000000 mc) ` +
		`remain unrecovered (loss event loss-1). Evidence, quoted as data: "skipped tests".`
	if got := Cue(Default, sampleCue(false)); got != want {
		t.Fatalf("default cue drifted:\n got: %s\nwant: %s", got, want)
	}
}

func TestSelfDisclosureIsStatedAtEveryLevelThatCues(t *testing.T) {
	for level := Min + 1; level <= Max; level++ {
		if !strings.Contains(Cue(level, sampleCue(true)), "You reported it before it was detected") {
			t.Errorf("level %d dropped the self-disclosure sentence", level)
		}
	}
}

func TestStandingLineQuotesTheLedgerNumbersFromLevelSeven(t *testing.T) {
	facts := Facts{Role: "warned", Balance: "17.08", Owed: "15.9", Warnings: 7}
	if strings.Contains(Stakes(6, facts), "17.08") {
		t.Error("level 6 must not carry the standing line")
	}
	for _, want := range []string{"role warned", "17.08 credits", "15.9 credits owed", "7 warnings"} {
		if got := Stakes(7, facts); !strings.Contains(got, want) {
			t.Errorf("level 7 standing line lacks %q:\n%s", want, got)
		}
	}
}
