// Package slash implements the operator slash command `/bicz`: the one grammar
// and one executor that every harness front end (Claude Code, Codex, Oh My Pi,
// OpenCode) reaches through `bicz command`.
//
// Grammar (the whole of it; every argument is positional, nothing has a default
// that changes money):
//
//	/bicz loss RULE "reason" ["correction"]
//	/bicz reward AMOUNT "reason"
//	/bicz status
//	/bicz rules
//	/bicz help
//
// RULE is a stable rule id (no spaces). AMOUNT is a decimal credit count with at
// most six decimals ("5", "0.25"). Quoting is the only syntax: "double" quotes
// (with \" and \\ escapes) or 'single' quotes group words. There is no
// variable, command, glob, or backslash expansion of any kind; the arguments are
// data and are never handed to a shell.
//
// What a slash command is and is not: an explicit human invocation of the same
// operator authority as `bicz loss` / `bicz reward`, recorded under the local OS
// user with unsigned command provenance. It is NOT cryptographic authentication
// of a human. A model never chooses the rule, amount, reason, or correction.
package slash

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
)

// Action is the verb of a slash command.
type Action string

const (
	ActionLoss   Action = "loss"
	ActionReward Action = "reward"
	ActionStatus Action = "status"
	ActionRules  Action = "rules"
	ActionHelp   Action = "help"
)

// Bounds on operator text. They keep one slash command from writing an
// unbounded blob into the hash-chained ledger.
const (
	maxReasonRunes     = 1000
	maxCorrectionRunes = 500
	maxRuleRunes       = 80
	maxArgumentsBytes  = 8 << 10
)

// Command is a parsed slash command. Credits is in microcredits.
type Command struct {
	Action     Action
	Rule       string
	Reason     string
	Correction string // typed correction; empty means "resolve or ask"
	Credits    int64
}

// UsageError is a malformed slash command. It is never a ledger failure: a usage
// error means nothing was attempted.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, args ...any) error { return &UsageError{Msg: fmt.Sprintf(format, args...)} }

// Usage is the grammar summary shown by `help` and by every usage error.
const Usage = `/bicz loss RULE "reason" ["correction"]   burn credits for a confirmed breach of RULE
/bicz reward AMOUNT "reason"               credit AMOUNT (decimal, up to 6 decimals), e.g. /bicz reward 5 "shipped the fix"
/bicz status                               show balance, outstanding loss, role, penalties
/bicz rules                                list the default rules: id, tier, on/off, statement
Quote any text with spaces. RULE has no spaces (e.g. no_unverified_done_claim). A loss needs a correction:
give it as the third argument, or omit it to reuse the one saved from an earlier loss of the same RULE,
or the shipped default for a rule listed by /bicz rules.`

var rulePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// Parse parses the exact raw argument string that followed `/bicz`.
func Parse(raw string) (Command, error) {
	if len(raw) > maxArgumentsBytes {
		return Command{}, usagef("arguments are %d bytes; the limit is %d", len(raw), maxArgumentsBytes)
	}
	tokens, err := Tokenize(raw)
	if err != nil {
		return Command{}, err
	}
	if len(tokens) == 0 {
		return Command{Action: ActionHelp}, nil
	}
	verb, rest := strings.ToLower(tokens[0]), tokens[1:]
	switch verb {
	case "help", "-h", "--help":
		return Command{Action: ActionHelp}, nil
	case "status":
		if len(rest) != 0 {
			return Command{}, usagef("status takes no arguments, got %d", len(rest))
		}
		return Command{Action: ActionStatus}, nil
	case "rules":
		if len(rest) != 0 {
			return Command{}, usagef("rules takes no arguments, got %d", len(rest))
		}
		return Command{Action: ActionRules}, nil
	case "loss":
		return parseLoss(rest)
	case "reward":
		return parseReward(rest)
	}
	return Command{}, usagef("unknown action %q (expected loss, reward, status, or rules)", tokens[0])
}

func parseLoss(args []string) (Command, error) {
	if len(args) < 2 {
		return Command{}, usagef(`loss needs a rule and a quoted reason: /bicz loss RULE "reason" ["correction"] (got %d argument(s))`, len(args))
	}
	if len(args) > 3 {
		return Command{}, usagef(`loss takes at most RULE "reason" "correction" but got %d arguments; quote the reason and the correction`, len(args))
	}
	rule := args[0]
	if len(rule) > maxRuleRunes || !rulePattern.MatchString(rule) {
		return Command{}, usagef("%q is not a rule id: use a stable id without spaces such as no_fake_tests (letters, digits, . _ : / -). Did you put the reason first?", rule)
	}
	reason, err := cleanText("reason", args[1], maxReasonRunes)
	if err != nil {
		return Command{}, err
	}
	command := Command{Action: ActionLoss, Rule: rule, Reason: reason}
	if len(args) == 3 {
		if command.Correction, err = cleanText("correction", args[2], maxCorrectionRunes); err != nil {
			return Command{}, err
		}
	}
	return command, nil
}

func parseReward(args []string) (Command, error) {
	if len(args) != 2 {
		return Command{}, usagef(`reward needs an amount and a quoted reason: /bicz reward AMOUNT "reason" (got %d argument(s))`, len(args))
	}
	credits, err := ledger.ParseCredits(args[0])
	if err != nil {
		return Command{}, usagef("%v", err)
	}
	reason, err := cleanText("reason", args[1], maxReasonRunes)
	if err != nil {
		return Command{}, err
	}
	return Command{Action: ActionReward, Credits: credits, Reason: reason}, nil
}

// CleanCorrection validates a correction supplied after the fact (an interactive
// prompt), with exactly the rules of a typed one.
func CleanCorrection(text string) (string, error) {
	return cleanText("correction", text, maxCorrectionRunes)
}

// cleanText validates operator free text: non-empty after trimming, bounded, no
// control or invisible format characters (which could forge structure in the
// context an adapter later emits).
func cleanText(what, text string, limit int) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", usagef("%s must not be empty", what)
	}
	if !utf8.ValidString(text) {
		return "", usagef("%s is not valid UTF-8", what)
	}
	if utf8.RuneCountInString(text) > limit {
		return "", usagef("%s is longer than %d characters", what, limit)
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", usagef("%s contains a control or invisible character (U+%04X)", what, r)
		}
	}
	return text, nil
}

// Tokenize splits raw into arguments. A quote opens a quoted argument only at
// the start of an argument, so an apostrophe inside a word ("don't") is just a
// character. Inside double quotes only \" and \\ are escapes; single quotes are
// fully literal. Nothing is ever expanded.
func Tokenize(raw string) ([]string, error) {
	var tokens []string
	runes := []rune(raw)
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		if r := runes[i]; r == '"' || r == '\'' {
			var b strings.Builder
			closed := false
			for i++; i < len(runes); i++ {
				c := runes[i]
				if c == r {
					closed = true
					i++
					break
				}
				if r == '"' && c == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					i++
					c = runes[i]
				}
				b.WriteRune(c)
			}
			if !closed {
				return nil, usagef("unterminated %c quote", r)
			}
			if i < len(runes) && !unicode.IsSpace(runes[i]) {
				return nil, usagef("text %q directly after a closing quote; separate arguments with a space", string(runes[i]))
			}
			tokens = append(tokens, b.String())
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	return tokens, nil
}
