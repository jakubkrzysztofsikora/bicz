package install

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed embed/opencode_v1.ts
var opencodeV1 []byte

//go:embed embed/opencode_v2.ts
var opencodeV2 []byte

const opencodePluginMarker = "// bicz OpenCode "

// OpenCodeGeneration resolves a requested generation or detects the installed binary's major.
func OpenCodeGeneration(override string) (int, error) {
	if override != "" {
		v, err := strconv.Atoi(override)
		if err != nil || (v != 1 && v != 2) {
			return 0, errors.New("install: OpenCode generation must be 1 or 2")
		}
		return v, nil
	}
	out, err := exec.Command("opencode", "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("install: detect OpenCode generation with opencode --version: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, errors.New("install: empty output from opencode --version")
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(fields[len(fields)-1], "v"), ".", 2)[0])
	if err != nil || (major != 1 && major != 2) {
		return 0, fmt.Errorf("install: unsupported OpenCode version %q", strings.TrimSpace(string(out)))
	}
	return major, nil
}

// PlanOpenCodeInstall plans an atomic user-scope plugin and bicz config install.
func PlanOpenCodeInstall(opts Options, generation int) (*Plan, error) {
	if generation != 1 && generation != 2 {
		return nil, errors.New("install: OpenCode generation must be 1 or 2")
	}
	account := strings.TrimSpace(opts.Account)
	if account == "" {
		account = DefaultAccount()
	}
	if account == "" {
		return nil, errors.New("install: --account is required")
	}
	ledger := strings.TrimSpace(opts.LedgerPath)
	if ledger == "" {
		ledger = DefaultLedgerPath()
	}
	socket, err := DefaultSocketPath(ledger)
	if err != nil {
		return nil, err
	}
	root := os.Getenv("OPENCODE_CONFIG_DIR")
	if root == "" {
		root = os.Getenv("XDG_CONFIG_HOME")
	}
	if root == "" {
		root = filepath.Join(userHome(), ".config")
	}
	plugin := filepath.Join(root, "opencode", "plugins", "bicz.ts")
	// OPENCODE_CONFIG_DIR names OpenCode's config directory itself.
	if os.Getenv("OPENCODE_CONFIG_DIR") != "" {
		plugin = filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "plugins", "bicz.ts")
	}
	if root == os.Getenv("XDG_CONFIG_HOME") && os.Getenv("OPENCODE_CONFIG_DIR") == "" {
		plugin = filepath.Join(root, "opencode", "plugins", "bicz.ts")
	}
	cfg := OpenCodeConfigPath()
	plan := &Plan{ModulePath: plugin, ConfigPath: cfg, Account: account, Ledger: ledger}
	body := opencodeV1
	if generation == 2 {
		body = opencodeV2
	}
	plan.Changes, err = planOpenCodeFile(plan.Changes, plugin, body, opencodePluginMarker)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{"bicz_owned": true, "kind": "bicz.opencode-config", "version": 1, "unmanaged_mode": "notice", "no_schedule_mode": "notice"}
	if err := carryOperatorKeys(cfg, fields); err != nil {
		return nil, err
	}
	fields["account_id"], fields["ledger"], fields["socket"] = account, ledger, socket
	if err := addSlashExecutable(fields, opts); err != nil {
		return nil, err
	}
	configBody, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	configBody = append(configBody, '\n')
	plan.ConfigBody = configBody
	plan.Changes, err = planOpenCodeConfigWrite(plan.Changes, cfg, configBody)
	return plan, err
}

func planOpenCodeFile(changes []Change, path string, content []byte, marker string) ([]Change, error) {
	existing, state, err := inspectTarget("install: OpenCode plugin", path, marker)
	if err != nil {
		return nil, err
	}
	if state == targetForeign {
		return nil, fmt.Errorf("install: refusing to clobber %s: not a bicz-owned OpenCode plugin", path)
	}
	if state == targetAbsent {
		return append(changes, Change{Kind: ChangeWriteHooks, Path: path, Body: content, Mode: 0644, Detail: "OpenCode plugin does not exist yet"}), nil
	}
	if bytesEqual(existing, content) {
		return changes, nil
	}
	backup := path + ".bak-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	return append(changes, Change{Kind: ChangeWriteHooks, Path: path, Body: content, Prior: existing, Backup: backup, Mode: 0644, Detail: "replacing bicz-owned OpenCode plugin; prior version backed up"}), nil
}

// planOpenCodeConfigWrite plans a config update without creating backups during dry-run.

func planOpenCodeConfigWrite(changes []Change, path string, content []byte) ([]Change, error) {
	existing, state, err := inspectTarget("install: OpenCode config", path, configMarker)
	if err != nil {
		return nil, err
	}
	if state == targetForeign {
		return nil, fmt.Errorf("install: refusing to clobber %s: not a bicz-owned OpenCode config", path)
	}
	if state == targetAbsent {
		return append(changes, Change{Kind: ChangeWriteHooks, Path: path, Body: content, Mode: 0644, Detail: "OpenCode adapter config does not exist yet"}), nil
	}
	if bytesEqual(existing, content) {
		return changes, nil
	}
	backup := path + ".bak-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	return append(changes, Change{Kind: ChangeWriteHooks, Path: path, Body: content, Prior: existing, Backup: backup, Mode: 0644, Detail: "replacing bicz-owned OpenCode config; prior version backed up"}), nil
}

// PlanOpenCodeUninstall plans removal of only bicz-owned OpenCode files.
func PlanOpenCodeUninstall() (*Plan, error) {
	root := os.Getenv("OPENCODE_CONFIG_DIR")
	if root == "" {
		root = os.Getenv("XDG_CONFIG_HOME")
	}
	if root == "" {
		root = filepath.Join(userHome(), ".config")
	}
	plugin := filepath.Join(root, "opencode", "plugins", "bicz.ts")
	if os.Getenv("OPENCODE_CONFIG_DIR") != "" {
		plugin = filepath.Join(root, "plugins", "bicz.ts")
	}
	plan := &Plan{ModulePath: plugin, ConfigPath: OpenCodeConfigPath(), uninstall: true}
	var err error
	plan.Changes, err = planOpenCodeRemoval(plan.Changes, plugin)
	if err != nil {
		return nil, err
	}
	plan.Changes, err = planConfigRemoval(plan.Changes, plan.ConfigPath)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func planOpenCodeRemoval(changes []Change, path string) ([]Change, error) {
	_, state, err := inspectTarget("uninstall: OpenCode plugin", path, opencodePluginMarker)
	if err != nil {
		return nil, err
	}
	if state == targetAbsent {
		return changes, nil
	}
	if state == targetForeign {
		return nil, fmt.Errorf("uninstall: refusing to remove foreign file %s", path)
	}
	backup := newestBackup(path)
	if backup != "" {
		return append(changes, Change{Kind: ChangeRestoreHooks, Path: path, Backup: backup, Detail: "restore newest bicz backup"}), nil
	}
	return append(changes, Change{Kind: ChangeRemoveHooks, Path: path, Detail: "remove bicz-owned OpenCode plugin"}), nil
}

// OpenCodeConfigPath locates the adapter config alongside the OpenCode user config root.
func OpenCodeConfigPath() string {
	if root := os.Getenv("OPENCODE_CONFIG_DIR"); root != "" {
		return filepath.Join(root, "bicz", "opencode.json")
	}
	return ConfigPath("opencode")
}
