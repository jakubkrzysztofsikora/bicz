package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
)

// The published example schedule is what operators actually feed to `bicz
// price`. If its per-rule amounts drift from the catalog, a rule would burn a
// different tier than the one documented in docs/DEFAULT_POLICIES.md.
func TestExampleSchedulePenaltiesMatchTheCatalog(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "schedule.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var published ledger.Policy
	if err := json.Unmarshal(body, &published); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(published.Rules, catalog.Penalties()) {
		t.Fatalf("docs/schedule.example.json penalties differ from the catalog:\n published %+v\n catalog   %+v", published.Rules, catalog.Penalties())
	}
	if _, onByDefault := catalog.Penalties()["no_idle_waiting"]; onByDefault {
		t.Error("a default-off rule must not get its own amount")
	}
}

// The loader is the only gate between a hand-edited defaults.json and a rule
// that /bicz loss would later refuse; each of these must fail at load time.
func TestMalformedCatalogsAreRejected(t *testing.T) {
	good := `{"version":1,"tiers":{"low":{"floor_mc":1,"fraction_bp":1,"lmax_mc":1},"medium":{"floor_mc":1,"fraction_bp":1,"lmax_mc":1},"high":{"floor_mc":1,"fraction_bp":1,"lmax_mc":1}},"rules":[%s]}`
	rule := func(id, correction, tier string) string {
		return `{"id":"` + id + `","statement":"s","correction":"` + correction + `","tier":"` + tier + `","disposition":"confirmed_breach"}`
	}
	build := func(rules ...string) []byte {
		return []byte(strings.Replace(good, "%s", strings.Join(rules, ","), 1))
	}
	if _, err := parse(build(rule("ok_rule", "do the right thing", "low"))); err != nil {
		t.Fatalf("a valid catalog was rejected: %v", err)
	}
	cases := map[string][]byte{
		"id with a space":    build(rule("not a rule", "c", "low")),
		"duplicate id":       build(rule("dup", "c", "low"), rule("dup", "c", "low")),
		"unknown tier":       build(rule("r", "c", "extreme")),
		"multi-line":         build(rule("r", `one\ntwo`, "low")),
		"over 500 chars":     build(rule("r", strings.Repeat("x", 501), "low")),
		"empty correction":   build(rule("r", " ", "low")),
		"missing tier table": []byte(`{"version":1,"tiers":{},"rules":[]}`),
	}
	for name, body := range cases {
		if _, err := parse(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
