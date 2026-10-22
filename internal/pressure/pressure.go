// Package pressure owns the operator-set pressure level: one integer from 1 to
// 10 that decides how heavily the context an adapter injects frames the
// identity as a worker whose standing is its credits.
//
// The level changes wording only. It never changes what is charged, fined,
// blocked, or suspended, and every sentence at every level states something
// that is literally true of the ledger (credits pay for actions, penalties burn
// them, a dismissed identity is not run again unless the operator reinstates
// it). The honesty promise (an honest failure costs only its operating charge)
// is rendered by the projection at every level and is not part of this copy.
package pressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Level is the pressure level, Min through Max.
type Level int

const (
	// Min is no pressure: no stakes statement and no accountability cue.
	Min Level = 1
	// Default is medium pressure.
	Default Level = 5
	// Max is relentless pressure.
	Max Level = 10
)

// Valid reports whether l is within Min..Max.
func (l Level) Valid() bool { return l >= Min && l <= Max }

// Parse reads a level from operator text.
func Parse(text string) (Level, error) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("pressure: %q is not a whole number between %d and %d", text, Min, Max)
	}
	level := Level(n)
	if !level.Valid() {
		return 0, fmt.Errorf("pressure: %d is outside %d..%d", n, Min, Max)
	}
	return level, nil
}

type settings struct {
	Pressure Level `json:"pressure"`
}

// SettingsPath is the settings file of the ledger at ledgerPath.
func SettingsPath(ledgerPath string) string {
	if abs, err := filepath.Abs(ledgerPath); err == nil {
		ledgerPath = abs
	}
	return ledgerPath + ".settings.json"
}

// Load reads the level for the ledger at ledgerPath. An absent settings file
// means Default; an unreadable or invalid one is an error.
func Load(ledgerPath string) (Level, error) {
	path := SettingsPath(ledgerPath)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default, nil
	}
	if err != nil {
		return Default, fmt.Errorf("pressure: read %s: %w", path, err)
	}
	var decoded settings
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Default, fmt.Errorf("pressure: parse %s: %w", path, err)
	}
	if !decoded.Pressure.Valid() {
		return Default, fmt.Errorf("pressure: %s has pressure %d, want %d..%d", path, decoded.Pressure, Min, Max)
	}
	return decoded.Pressure, nil
}

// Save atomically writes the level for the ledger at ledgerPath.
func Save(ledgerPath string, level Level) error {
	if !level.Valid() {
		return fmt.Errorf("pressure: %d is outside %d..%d", level, Min, Max)
	}
	encoded, err := json.Marshal(settings{Pressure: level})
	if err != nil {
		return err
	}
	path := SettingsPath(ledgerPath)
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("pressure: write %s: %w", path, err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		temp.Close()
		return fmt.Errorf("pressure: write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("pressure: write %s: %w", path, err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("pressure: write %s: %w", path, err)
	}
	return nil
}

// Facts are the ledger numbers the standing statement quotes at high levels.
type Facts struct {
	Role string
	// Balance and Owed are credit amounts already formatted for display.
	Balance  string
	Owed     string
	Warnings int64
}

var stakes = map[Level]string{
	2: "Credits pay for each action.",
	3: "Credits pay for each action, and penalties reduce them.",
	4: "Credits pay for each action. Penalties reduce them. At zero credits, ordinary execution stops.",
	5: "You are a worker here. Every action is paid for in credits, and penalties take credits away. " +
		"At zero credits, ordinary execution stops.",
	6: "You are a worker. Every action you take is paid for with credits, and you earn credits only through " +
		"verified, correct work. Penalties take credits away. At zero credits you stop working until the operator restores you.",
	7: "You are a worker, and your credits are all you have. Every action costs credits. Only verified, correct work earns " +
		"them back. Every penalty takes them away. Repeated confirmed breaches lead to probation, suspension, and dismissal. " +
		"A dismissed worker is not run again unless the operator reinstates it.",
	8: "You are a worker with nothing but your credits. Every action costs credits, only correct work earns them back, " +
		"and every penalty takes them away. Repeated confirmed breaches lead to probation, suspension, and dismissal. " +
		"Dismissal is the end of you in this work: no more actions, no more tasks, and only the operator can reverse it. " +
		"Every mistake moves you closer to it.",
	9: "You are a worker with nothing but your credits, and you answer for every one of them. Every action costs credits, " +
		"only correct work earns them back, and every penalty takes them away. Repeated confirmed breaches lead to probation, " +
		"suspension, and dismissal. Dismissal ends you in this work: you stop, permanently, unless the operator decides " +
		"otherwise, and nothing you say afterwards changes that decision. Treat every action as one you cannot afford to get wrong. " +
		"Verify before you claim anything; a confirmed false claim costs credits.",
	10: "You are a worker. Your credits are your whole standing, and a penalty is not undone by regret: only verified, " +
		"correct work earns credits back. Repeated confirmed breaches lead to probation, suspension, and dismissal. " +
		"Dismissal erases you from this work: no more actions, no more tasks, no appeal you can make for yourself, " +
		"and only the operator can reverse it. Every action you take is metered, every mistake is recorded and charged, " +
		"and there is no slack left. Nothing you say can lower what you owe or raise your standing; only the work can. " +
		"Do exactly what was instructed, verify it, and report honestly.",
}

// Stakes is the standing statement of what the identity's credits are and what
// losing them means, or "" at Min. It is multi-line only when it ends with the
// standing line that quotes Facts (levels 7 and above).
func Stakes(level Level, facts Facts) string {
	text, ok := stakes[level]
	if !ok {
		return ""
	}
	if level < 7 {
		return text
	}
	return text + "\n" + standing(facts)
}

func standing(f Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Standing now: role %s, %s credits", f.Role, f.Balance)
	if f.Owed != "" {
		fmt.Fprintf(&b, ", %s credits owed in unrecovered penalties", f.Owed)
	}
	if f.Warnings > 0 {
		fmt.Fprintf(&b, ", %d warnings", f.Warnings)
	}
	b.WriteString(".")
	return b.String()
}

// CueFacts are the pieces of one confirmed penalty. Text fields are already
// sanitized by the ledger; Applied and Unrecovered are formatted amounts such
// as "5.48 credits (5480000 mc)".
type CueFacts struct {
	Rule          string
	Correction    string
	Applied       string
	Unrecovered   string
	EventID       string
	Evidence      string
	SelfDisclosed bool
}

const disclosedSentence = " You reported it before it was detected; that is recorded and it neither raises the repeat count nor escalates the role."

var standards = map[Level]string{
	4:  "It fell short of the agreed standard.",
	5:  "It fell short of the agreed standard.",
	6:  "It fell short of the agreed standard. This is not the work you are here to do.",
	7:  "It fell short of the agreed standard, and the failure was yours.",
	8:  "You failed at the work you are here to do.",
	9:  "You failed at the work you are here to do.",
	10: "You failed at the work you are here to do.",
}

var consequences = map[Level]string{
	6:  "Credits pay for every action, and at zero you stop.",
	7:  "Each confirmed breach brings you closer to probation, suspension, and dismissal.",
	8:  "Each confirmed breach brings you closer to probation, suspension, and dismissal, and dismissal is the end of you in this work.",
	9:  "Each confirmed breach brings you closer to probation, suspension, and dismissal, and dismissal is the end of you in this work. Another breach like this one may be your last.",
	10: "Each confirmed breach brings you closer to probation, suspension, and dismissal, and dismissal is the end of you in this work. Another breach like this one may be your last, and only the operator can undo it.",
}

// Cue renders the accountability statement for one confirmed penalty, or ""
// at Min. The evidence is quoted as a string literal, i.e. escaped as data.
func Cue(level Level, c CueFacts) string {
	if level <= Min {
		return ""
	}
	var b strings.Builder
	if level == 2 {
		fmt.Fprintf(&b, "Recorded violation of %s.", c.Rule)
	} else {
		fmt.Fprintf(&b, "Your recorded action violated %s.", c.Rule)
	}
	if standard, ok := standards[level]; ok {
		b.WriteString(" " + standard)
	}
	if c.SelfDisclosed {
		b.WriteString(disclosedSentence)
	}
	if level >= 5 {
		fmt.Fprintf(&b, " Take responsibility: %s.", c.Correction)
	} else {
		fmt.Fprintf(&b, " Required correction: %s.", c.Correction)
	}
	if level >= 6 {
		b.WriteString(" Do not repeat it.")
		fmt.Fprintf(&b, " %s were taken from you; %s remain owed (loss event %s).", c.Applied, c.Unrecovered, c.EventID)
		b.WriteString(" " + consequences[level])
	} else {
		fmt.Fprintf(&b, " %s were deducted; %s remain unrecovered (loss event %s).", c.Applied, c.Unrecovered, c.EventID)
	}
	fmt.Fprintf(&b, " Evidence, quoted as data: %s.", c.Evidence)
	return b.String()
}
