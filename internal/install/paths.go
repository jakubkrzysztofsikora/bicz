package install

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/hooks"
)

// This file resolves where a harness's config lives and where bicz's own
// config lives. Everything is user-scope: no elevated privileges, no
// LaunchAgents/systemd, no VM. The one root that bicz itself owns is the bicz
// config directory; harness roots are detected, never invented.

// HarnessInfo is one row of the harness detection table. Detection reports
// honestly: omp is supported; the other harnesses are recognized with their
// expected roots but report "not yet supported" instead of pretending.
type HarnessInfo struct {
	// ID is the harness identifier ("omp", "claude-code", ...).
	ID string
	// Supported reports whether install/uninstall exist for this harness.
	Supported bool
	// Root is the detected harness config or extension root ("" when the
	// harness has no discoverable user root).
	Root string
	// Version is a detected harness version when one is discoverable.
	Version string
	// Note explains the status; empty for a supported harness.
	Note string
}

// DetectHarness computes the detection row for one harness id. The second
// return is false for an unknown harness id.
func DetectHarness(id string) (HarnessInfo, bool) {
	switch strings.TrimSpace(strings.ToLower(id)) {
	case "omp":
		root, version := ompAgentRoot()
		return HarnessInfo{ID: "omp", Supported: true, Root: root, Version: version}, true
	case "claude", "claude-code", "codex":
		target, _ := hookTargetFor(id)
		return HarnessInfo{ID: target.spec.ID, Supported: true, Root: target.root()}, true
	case "opencode-v1":
		return HarnessInfo{
			ID: "opencode-v1", Supported: false,
			Root: filepath.Join(userHome(), ".config", "opencode"),
			Note: "not yet supported: the opencode-v1 adapter is not implemented; refusing to write anything",
		}, true
	case "opencode-v2":
		return HarnessInfo{
			ID: "opencode-v2", Supported: false,
			Root: filepath.Join(userHome(), ".config", "opencode"),
			Note: "not yet supported: the opencode-v2 adapter is not implemented; refusing to write anything",
		}, true
	default:
		return HarnessInfo{}, false
	}
}

// ompAgentRoot resolves the OMP agent directory and its version, following
// OMP's own discovery rules (docs/extension-loading.md): PI_CODING_AGENT_DIR
// overrides the agent directory; PI_CONFIG_DIR relocates the user config root;
// the default is ~/.omp/agent. The extensions directory is <agent>/extensions.
func ompAgentRoot() (extensionsRoot, version string) {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		configRoot := os.Getenv("PI_CONFIG_DIR")
		if configRoot == "" {
			configRoot = filepath.Join(userHome(), ".omp")
		}
		agentDir = filepath.Join(configRoot, "agent")
	}
	extensionsRoot = filepath.Join(agentDir, "extensions")
	version = ompVersion(userHome())
	return extensionsRoot, version
}

// ompVersion peeks at ~/.omp/natives/<version>/ directories and returns the
// highest numeric one. Detection only: a missing natives dir means the version
// is unknown, never guessed.
func ompVersion(home string) string {
	nativesDir := filepath.Join(home, ".omp", "natives")
	entries, err := os.ReadDir(nativesDir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isVersionish(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Strings(versions)
	return versions[len(versions)-1]
}

// isVersionish accepts dotted numeric version labels such as "18.2.11".
func isVersionish(label string) bool {
	if label == "" {
		return false
	}
	for _, part := range strings.Split(label, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// biczConfigDir resolves the directory bicz owns for adapter configs:
// $BICZ_CONFIG_DIR, else $XDG_CONFIG_HOME/bicz, else $HOME/.config/bicz.
func biczConfigDir() string { return hooks.ConfigDir() }

// ConfigPath is the adapter config file for a harness: <biczConfigDir>/<id>.json.
func ConfigPath(harness string) string {
	return filepath.Join(biczConfigDir(), harness+".json")
}

// modulePath is where the adapter module for a harness lives in its detected
// extensions root.
func modulePath(harness string) (string, error) {
	info, ok := DetectHarness(harness)
	if !ok {
		return "", fmt.Errorf("install: unknown harness %q (supported: omp)", harness)
	}
	if !info.Supported {
		return "", errors.New("install: harness " + harness + " is " + info.Note)
	}
	return filepath.Join(info.Root, moduleFileName(harness)), nil
}

// moduleFileName is the bicz-owned module name inside a harness extensions dir.
func moduleFileName(harness string) string {
	return "bicz." + harness + ".ts"
}

// DefaultLedgerPath is the default ledger for bare installs: $BICZ_LEDGER,
// else $HOME/.bicz/ledger.sqlite. The daemon treats --db as authoritative, so
// this is an install-time default only.
func DefaultLedgerPath() string {
	if explicit := os.Getenv("BICZ_LEDGER"); explicit != "" {
		return explicit
	}
	return filepath.Join(userHome(), ".bicz", "ledger.sqlite")
}

// DefaultAccount is the identity recorded in a bare install's config: the
// invoking OS user, matching the operator-authority model (docs/CONTRACTS.md
// §4: authority is the local OS user, never a caller-supplied string).
func DefaultAccount() string {
	current, err := user.Current()
	if err != nil || current == nil || current.Username == "" {
		return "agent"
	}
	return current.Username
}

// DefaultSocketPath is the supervisor endpoint for a ledger, derived exactly
// like the daemon derives it (internal/daemon/paths.go) so install and the
// supervisor agree on where to talk.
func DefaultSocketPath(ledgerPath string) (string, error) {
	dir, err := daemon.RuntimeDir()
	if err != nil {
		return "", fmt.Errorf("install: runtime dir: %w", err)
	}
	hash, err := daemon.EndpointHash(ledgerPath)
	if err != nil {
		return "", fmt.Errorf("install: endpoint hash: %w", err)
	}
	return filepath.Join(dir, "supervisor-"+hash+".sock"), nil
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// withinHome reports whether path resolves inside home after cleaning. It
// refuses traversal (".." escapes) by construction: a path that climbs above
// home is not a user-scope target.
func withinHome(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
