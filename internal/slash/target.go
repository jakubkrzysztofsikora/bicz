package slash

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jakubkrzysztofsikora/bicz/internal/hooks"
)

// Harnesses is every harness a slash command can come from. The ids are the
// bicz adapter config names (<config dir>/<id>.json).
var Harnesses = []string{"claude", "codex", "omp", "opencode"}

// NormalizeHarness resolves a --harness value ("claude-code" is the Claude Code
// coverage-table spelling of "claude").
func NormalizeHarness(harness string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(harness))
	if id == "claude-code" {
		id = "claude"
	}
	for _, known := range Harnesses {
		if id == known {
			return id, nil
		}
	}
	return "", fmt.Errorf("unknown harness %q (supported: %s)", harness, strings.Join(Harnesses, ", "))
}

// Target is where a slash command acts: the identity and ledger the harness's
// installed bicz config already names. A slash command has no flag that selects
// either.
type Target struct {
	Harness       string
	Identity      string
	Ledger        string
	PolicyVersion string
	ConfigPath    string
}

type adapterConfig struct {
	BiczOwned     bool   `json:"bicz_owned"`
	Kind          string `json:"kind"`
	AccountID     string `json:"account_id"`
	Ledger        string `json:"ledger"`
	PolicyVersion string `json:"policy_version"`
}

// ConfigPath is the config of one harness: $BICZ_CONFIG (the same one-process
// override the hook adapters honor), else <config dir>/<harness>.json.
func ConfigPath(harness string) string {
	if explicit := os.Getenv("BICZ_CONFIG"); explicit != "" {
		return explicit
	}
	return filepath.Join(hooks.ConfigDir(), harness+".json")
}

// ResolveTarget reads and validates the installed adapter config of harness.
// Each harness has its own config; one harness's file is never consulted for
// another, and a config that is not bicz-owned, names a different harness, lacks
// an identity, or lacks an absolute ledger path is refused rather than guessed.
func ResolveTarget(harness string) (Target, error) {
	id, err := NormalizeHarness(harness)
	if err != nil {
		return Target{}, err
	}
	path := ConfigPath(id)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Target{}, fmt.Errorf("no bicz config for %s at %s (run `bicz install --harness %s`): %w", id, path, id, err)
	}
	var cfg adapterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Target{}, fmt.Errorf("bicz config %s is malformed: %w", path, err)
	}
	if !cfg.BiczOwned || cfg.Kind != "bicz."+id+"-config" {
		return Target{}, fmt.Errorf("bicz config %s is not a bicz-owned %s config (kind %q)", path, id, cfg.Kind)
	}
	identity := strings.TrimSpace(cfg.AccountID)
	if identity == "" {
		return Target{}, fmt.Errorf("bicz config %s has no account_id", path)
	}
	if cfg.Ledger == "" || !filepath.IsAbs(cfg.Ledger) {
		return Target{}, fmt.Errorf("bicz config %s has no absolute ledger path (re-run `bicz install --harness %s --ledger PATH`)", path, id)
	}
	policy := strings.TrimSpace(cfg.PolicyVersion)
	if policy == "" {
		policy = "1"
	}
	return Target{Harness: id, Identity: identity, Ledger: filepath.Clean(cfg.Ledger), PolicyVersion: policy, ConfigPath: path}, nil
}
