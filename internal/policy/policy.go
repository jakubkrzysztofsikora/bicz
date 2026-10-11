// Package policy is the catalog of default rules shipped with bicz: for each
// rule a stable id, a one-line statement, the correction an agent is shown, the
// lesson scope and exceptions, a burn tier and whether the rule is on by default.
//
// The catalog is a convenience for the operator, never a verdict. It does not
// detect anything and it never burns credit by itself: an operator still types
// `/bicz loss RULE "reason"`. The catalog only supplies the correction (when the
// operator typed none and no earlier loss of the rule saved one), the lesson
// scope and exceptions, and, through Penalties, the per-rule amounts that
// docs/schedule.example.json publishes.
//
// Tiers carry the ledger's own PenaltyRule parameters (floor_mc, fraction_bp,
// lmax_mc; docs/CONTRACTS.md 6.1), so a tier is nothing but a named set of the
// numbers the ledger already understands.
package policy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
)

//go:embed defaults.json
var defaultsJSON []byte

// Dispositions a rule may suggest. They are advisory: /bicz loss always records
// confirmed_breach (docs/SLASH_COMMAND.md); `bicz loss --disposition` can choose.
const (
	DispositionConfirmedBreach = "confirmed_breach"
	DispositionIntegrityBreach = "integrity_breach"
)

// Rule is one default rule.
type Rule struct {
	ID          string `json:"id"`
	Statement   string `json:"statement"`
	Correction  string `json:"correction"`
	Scope       string `json:"scope"`
	Exceptions  string `json:"exceptions"`
	Tier        string `json:"tier"`
	DefaultOn   bool   `json:"default_on"`
	Disposition string `json:"disposition"`
}

// Catalog is the decoded defaults document.
type Catalog struct {
	Version int                           `json:"version"`
	Name    string                        `json:"name"`
	Tiers   map[string]ledger.PenaltyRule `json:"tiers"`
	Rules   []Rule                        `json:"rules"`

	byID map[string]int
}

var (
	loadOnce sync.Once
	loaded   *Catalog
	loadErr  error
)

// Load returns the embedded catalog, validated. The result is shared and must
// not be modified.
func Load() (*Catalog, error) {
	loadOnce.Do(func() { loaded, loadErr = parse(defaultsJSON) })
	return loaded, loadErr
}

// Bounds and syntax the slash grammar enforces on a rule id and a correction. A
// catalog entry that broke them could never be used by `/bicz loss`.
const (
	maxRuleRunes       = 80
	maxCorrectionRunes = 500
)

var rulePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

var tierNames = []string{"low", "medium", "high"}

func parse(body []byte) (*Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("policy: decode defaults: %w", err)
	}
	if catalog.Version != 1 {
		return nil, fmt.Errorf("policy: unsupported defaults version %d", catalog.Version)
	}
	for _, name := range tierNames {
		tier, ok := catalog.Tiers[name]
		if !ok {
			return nil, fmt.Errorf("policy: tier %q is missing", name)
		}
		if tier.Floor <= 0 || tier.FractionBP <= 0 || tier.Max <= 0 {
			return nil, fmt.Errorf("policy: tier %q needs positive floor_mc, fraction_bp and lmax_mc", name)
		}
	}
	catalog.byID = make(map[string]int, len(catalog.Rules))
	for i, rule := range catalog.Rules {
		if len(rule.ID) > maxRuleRunes || !rulePattern.MatchString(rule.ID) {
			return nil, fmt.Errorf("policy: %q is not a valid rule id", rule.ID)
		}
		if _, dup := catalog.byID[rule.ID]; dup {
			return nil, fmt.Errorf("policy: duplicate rule id %q", rule.ID)
		}
		if _, ok := catalog.Tiers[rule.Tier]; !ok {
			return nil, fmt.Errorf("policy: rule %q names unknown tier %q", rule.ID, rule.Tier)
		}
		if rule.Disposition != DispositionConfirmedBreach && rule.Disposition != DispositionIntegrityBreach {
			return nil, fmt.Errorf("policy: rule %q has disposition %q", rule.ID, rule.Disposition)
		}
		if strings.TrimSpace(rule.Statement) == "" {
			return nil, fmt.Errorf("policy: rule %q has no statement", rule.ID)
		}
		if err := validCorrection(rule.Correction); err != nil {
			return nil, fmt.Errorf("policy: rule %q: %w", rule.ID, err)
		}
		catalog.byID[rule.ID] = i
	}
	return &catalog, nil
}

// validCorrection applies the rules the slash grammar applies to a typed
// correction: non-empty, one line, bounded, no control or invisible characters.
func validCorrection(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("correction is empty")
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("correction is not valid UTF-8")
	}
	if utf8.RuneCountInString(text) > maxCorrectionRunes {
		return fmt.Errorf("correction is longer than %d characters", maxCorrectionRunes)
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("correction contains a control or invisible character (U+%04X)", r)
		}
	}
	return nil
}

// Rule returns the default rule with this id.
func (c *Catalog) Rule(id string) (Rule, bool) {
	if c == nil {
		return Rule{}, false
	}
	i, ok := c.byID[id]
	if !ok {
		return Rule{}, false
	}
	return c.Rules[i], true
}

// Correction returns the shipped correction for a rule id, or "" when the id is
// not a default rule.
func (c *Catalog) Correction(id string) string {
	rule, _ := c.Rule(id)
	return rule.Correction
}

// Penalties maps each default-on rule to its tier's ledger parameters. It is the
// body of the "penalties" key of a price schedule; rules that are off, and rules
// the catalog does not know, fall back to the schedule's default_penalty.
func (c *Catalog) Penalties() map[string]ledger.PenaltyRule {
	out := make(map[string]ledger.PenaltyRule)
	if c == nil {
		return out
	}
	for _, rule := range c.Rules {
		if rule.DefaultOn {
			out[rule.ID] = c.Tiers[rule.Tier]
		}
	}
	return out
}
