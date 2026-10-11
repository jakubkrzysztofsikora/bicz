// Package install implements the user-scope harness installer behind the
// `bicz install` / `bicz uninstall` commands.
//
// Rules this package enforces (all user-scope; no elevated privileges, no
// LaunchAgents/systemd, no VM):
//
//   - Only bicz-owned files are ever written, backed up, or removed. Ownership
//     is proven by a marker inside the file, never by the path.
//   - Writes are atomic (temp file + rename) and every replacement of a
//     bicz-owned file first takes a timestamped backup.
//   - A second install of the same state is a no-op (idempotent).
//   - A foreign, malformed, unreadable, or symlinked target is refused, never
//     clobbered.
//   - Uninstall restores the newest bicz backup when safe and removes only
//     bicz-owned entries.
//   - --dry-run prints the exact diff and changes nothing.
package install

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed embed/omp_extension.ts
var ompExtensionModule []byte

// moduleMarker is the first line of the deployed adapter module. Ownership of
// an existing module is proven by this prefix plus the protocol id inside the
// body: a file with neither is someone else's and is never touched.
const moduleMarker = "// bicz — Oh My Pi harness adapter."

// configKind is the JSON kind marker of a bicz-owned adapter config.
const configKind = "bicz.omp-config"

// Options are the resolved inputs of an install or uninstall.
type Options struct {
	// Harness is the harness id ("omp").
	Harness string
	// Account is the identity recorded in the adapter config.
	Account string
	// LedgerPath is the ledger the supervisor meters into; its endpoint hash
	// names the socket.
	LedgerPath string
	// DryRun prints the diff and makes no changes.
	DryRun bool
	// Executable overrides the bicz executable path written into hook
	// commands (claude, codex). Empty means the running executable.
	Executable string
	// Generation selects the OpenCode plugin generation ("1" or "2"); empty
	// detects the installed OpenCode binary.
	Generation string
}

// ChangeKind is the kind of one planned change.
type ChangeKind string

const (
	ChangeWriteModule   ChangeKind = "write_module"
	ChangeWriteConfig   ChangeKind = "write_config"
	ChangeRemoveModule  ChangeKind = "remove_module"
	ChangeRemoveConfig  ChangeKind = "remove_config"
	ChangeRestoreModule ChangeKind = "restore_module"
	ChangeRestoreConfig ChangeKind = "restore_config"
	ChangeWriteHooks    ChangeKind = "write_hooks"
	ChangeRestoreHooks  ChangeKind = "restore_hooks"
	ChangeRemoveHooks   ChangeKind = "remove_hooks"
	ChangeWriteAsset    ChangeKind = "write_asset"
	ChangeRemoveAsset   ChangeKind = "remove_asset"
)

// Change is one planned filesystem change.
type Change struct {
	Kind   ChangeKind
	Path   string
	Backup string // backup consumed by this change ("" when none)
	Detail string
	// Hook-file changes only (claude, codex): the merged document to write, the
	// exact bytes of the file it replaces (written to Backup by Apply), the file
	// mode to keep, and the line diff shown by a dry run.
	Body  []byte
	Prior []byte
	Mode  os.FileMode
	Diff  string
}

// Plan is the exact diff of an install or uninstall.
type Plan struct {
	// Changes in application order. Empty means "nothing to do" (idempotent
	// second run, or uninstall of a clean state).
	Changes []Change
	// ModulePath and ConfigPath name the targets.
	ModulePath string
	ConfigPath string
	// ConfigBody is the config written (or removed) by this plan.
	ConfigBody []byte
	// hookMarker is the command substring that identifies this harness's bicz
	// hook entries; uninstall marks the plan so Apply can drop redundant backups.
	hookMarker string
	uninstall  bool
	// Account and Ledger are the resolved identity and ledger of an install,
	// echoed by the CLI.
	Account string
	Ledger  string
}

// Empty reports whether the plan would change nothing.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// Print renders the plan as an exact diff, one line per change.
func (p *Plan) Print() {
	for _, change := range p.Changes {
		switch change.Kind {
		case ChangeWriteModule, ChangeWriteConfig:
			fmt.Printf("  write %s", change.Path)
			if change.Backup != "" {
				fmt.Printf(" (backup %s)", change.Backup)
			}
			fmt.Println()
		case ChangeRemoveModule, ChangeRemoveConfig:
			fmt.Printf("  remove %s\n", change.Path)
		case ChangeWriteHooks, ChangeWriteAsset:
			fmt.Printf("  write %s", change.Path)
			if change.Backup != "" {
				fmt.Printf(" (backup %s)", change.Backup)
			}
			fmt.Println()
		case ChangeRemoveHooks, ChangeRemoveAsset:
			fmt.Printf("  remove %s\n", change.Path)
		case ChangeRestoreHooks:
			fmt.Printf("  restore %s <- %s\n", change.Path, change.Backup)
		case ChangeRestoreModule, ChangeRestoreConfig:
			fmt.Printf("  restore %s <- %s\n", change.Path, change.Backup)
		}
		if change.Detail != "" {
			fmt.Printf("    %s\n", change.Detail)
		}
		if change.Diff != "" {
			for _, line := range strings.Split(change.Diff, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

// PlanInstall computes the exact diff of an install. It returns an error for a
// refusal (unknown harness, unsupported harness, foreign target, symlink,
// unreadable target) before anything is touched.
func PlanInstall(opts Options) (*Plan, error) {
	if opts.Harness == "" {
		return nil, errors.New("install: --harness is required (supported: omp, claude, codex, opencode)")
	}
	if opts.Harness == "opencode" {
		gen, err := OpenCodeGeneration(opts.Generation)
		if err != nil {
			return nil, err
		}
		return PlanOpenCodeInstall(opts, gen)
	}
	if target, ok := hookTargetFor(opts.Harness); ok {
		return planHooksInstall(opts, target)
	}
	info, ok := DetectHarness(opts.Harness)
	if !ok {
		return nil, fmt.Errorf("install: unknown harness %q (supported: omp, claude, codex, opencode)", opts.Harness)
	}
	if !info.Supported {
		return nil, fmt.Errorf("install: harness %s is %s", opts.Harness, info.Note)
	}
	account := strings.TrimSpace(opts.Account)
	if account == "" {
		account = DefaultAccount()
	}
	if account == "" {
		return nil, errors.New("install: --account is required when the OS user cannot be resolved")
	}
	ledger := strings.TrimSpace(opts.LedgerPath)
	if ledger == "" {
		ledger = DefaultLedgerPath()
	}
	socketPath, err := DefaultSocketPath(ledger)
	if err != nil {
		return nil, err
	}

	modulePath, err := modulePath(opts.Harness)
	if err != nil {
		return nil, err
	}
	configPath := ConfigPath(opts.Harness)
	plan := &Plan{ModulePath: modulePath, ConfigPath: configPath, Account: account, Ledger: ledger}

	plan.Changes, err = planModuleWrite(plan.Changes, modulePath)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{
		"bicz_owned":       true,
		"kind":             configKind,
		"version":          1,
		"unmanaged_mode":   "notice",
		"no_schedule_mode": "notice",
	}
	if err := carryOperatorKeys(configPath, fields); err != nil {
		return nil, err
	}
	fields["account_id"], fields["ledger"], fields["socket"] = account, ledger, socketPath
	if err := addSlashExecutable(fields, opts); err != nil {
		return nil, err
	}
	configBody, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("install: encode config: %w", err)
	}
	configBody = append(configBody, '\n')
	plan.ConfigBody = configBody
	plan.Changes, err = planConfigWrite(plan.Changes, configPath, configBody)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// PlanUninstall computes the exact diff of an uninstall.
func PlanUninstall(harness string) (*Plan, error) {
	if harness == "" {
		return nil, errors.New("uninstall: --harness is required (supported: omp, claude, codex, opencode)")
	}
	if harness == "opencode" {
		return PlanOpenCodeUninstall()
	}
	if target, ok := hookTargetFor(harness); ok {
		return planHooksUninstall(target)
	}
	info, ok := DetectHarness(harness)
	if !ok {
		return nil, fmt.Errorf("uninstall: unknown harness %q (supported: omp, claude, codex, opencode)", harness)
	}
	if info.Supported {
		modulePath, err := modulePath(harness)
		if err != nil {
			return nil, err
		}
		configPath := ConfigPath(harness)
		plan := &Plan{ModulePath: modulePath, ConfigPath: configPath}
		plan.Changes, err = planModuleRemoval(plan.Changes, modulePath)
		if err != nil {
			return nil, err
		}
		plan.Changes, err = planConfigRemoval(plan.Changes, configPath)
		if err != nil {
			return nil, err
		}
		return plan, nil
	}
	// Recognized but unsupported: report honestly, remove nothing.
	return nil, fmt.Errorf("uninstall: harness %s is %s", harness, info.Note)
}

// Apply executes every change in order. It is only called for a non-empty,
// already-validated plan; every write is atomic (temp + rename) and every
// replacement has its backup taken first.
func (p *Plan) Apply() error {
	for _, change := range p.Changes {
		switch change.Kind {
		case ChangeWriteModule:
			if err := writeModule(change.Path, change.Backup); err != nil {
				return err
			}
		case ChangeWriteConfig:
			if err := writeFileAtomic(change.Path, p.ConfigBody, change.Backup); err != nil {
				return err
			}
		case ChangeRemoveModule, ChangeRemoveConfig:
			if err := os.Remove(change.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("install: remove %s: %w", change.Path, err)
			}
		case ChangeWriteHooks:
			if change.Prior != nil {
				if err := os.WriteFile(change.Backup, change.Prior, change.Mode); err != nil {
					return fmt.Errorf("install: write backup %s: %w", change.Backup, err)
				}
			}
			if err := writeFileAtomicMode(change.Path, change.Body, change.Backup, change.Mode); err != nil {
				return err
			}
		case ChangeRestoreHooks:
			if err := restoreBackup(change.Backup, change.Path); err != nil {
				return err
			}
		case ChangeWriteAsset:
			if err := writeAsset(change); err != nil {
				return err
			}
		case ChangeRemoveAsset:
			if err := removeAsset(change.Path); err != nil {
				return err
			}
		case ChangeRemoveHooks:
			if err := os.Remove(change.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("install: remove %s: %w", change.Path, err)
			}
		case ChangeRestoreModule, ChangeRestoreConfig:
			if err := restoreBackup(change.Backup, change.Path); err != nil {
				return err
			}
		}
	}
	// Remove the config directory when we emptied it (it is bicz-owned and
	// should not linger), and drop stale backups that were not restored.
	p.removeEmptyConfigDir()
	p.removeStaleBackups()
	if p.uninstall {
		p.cleanHookBackups()
	}
	return nil
}

// --- module plan ------------------------------------------------------------

func planModuleWrite(changes []Change, path string) ([]Change, error) {
	existing, state, err := inspectTarget("install: module", path, moduleMarker)
	if err != nil {
		return nil, err
	}
	if state == targetAbsent {
		parent := filepath.Dir(path)
		if err := checkPathRoots(parent); err != nil {
			return nil, fmt.Errorf("install: module directory %s: %w", parent, err)
		}
		return append(changes, Change{Kind: ChangeWriteModule, Path: path,
			Detail: "adapter module does not exist yet"}), nil
	}
	if state == targetForeign {
		return nil, fmt.Errorf("install: refusing to clobber %s: it exists but is not a bicz-owned adapter module", path)
	}
	if bytesEqual(existing, ompExtensionModule) {
		return changes, nil // idempotent second install: no change
	}
	backup, err := stageBackup(path, existing)
	if err != nil {
		return nil, err
	}
	return append(changes, Change{Kind: ChangeWriteModule, Path: path, Backup: backup,
		Detail: "existing bicz-owned module differs from the deployed version; backed up before rewriting"}), nil
}

func planConfigWrite(changes []Change, path string, body []byte) ([]Change, error) {
	existing, state, err := inspectTarget("install: config", path, configMarker)
	if err != nil {
		return nil, err
	}
	if state == targetAbsent {
		parent := filepath.Dir(path)
		if err := checkPathRoots(parent); err != nil {
			return nil, fmt.Errorf("install: config directory %s: %w", parent, err)
		}
		return append(changes, Change{Kind: ChangeWriteConfig, Path: path,
			Detail: "adapter config does not exist yet"}), nil
	}
	if state == targetForeign {
		return nil, fmt.Errorf("install: refusing to clobber %s: it exists but is not a bicz-owned config", path)
	}
	if bytesEqual(existing, body) {
		return changes, nil
	}
	backup, err := stageBackup(path, existing)
	if err != nil {
		return nil, err
	}
	return append(changes, Change{Kind: ChangeWriteConfig, Path: path, Backup: backup,
		Detail: "existing bicz-owned config differs from the requested one; backed up before rewriting"}), nil
}

// --- uninstall plan ----------------------------------------------------------

func planModuleRemoval(changes []Change, path string) ([]Change, error) {
	_, state, err := inspectTarget("uninstall: module", path, moduleMarker)
	if err != nil {
		return nil, err
	}
	switch state {
	case targetAbsent:
		return changes, nil
	case targetForeign:
		return changes, nil // not ours: never remove it
	}
	backup := newestBackup(path)
	if backup != "" {
		if err := checkPathRoots(backup); err != nil {
			return nil, fmt.Errorf("uninstall: cannot safely restore %s: %w", backup, err)
		}
		return append(changes, Change{Kind: ChangeRestoreModule, Path: path, Backup: backup,
			Detail: "restoring the newest bicz-owned module backup"}), nil
	}
	return append(changes, Change{Kind: ChangeRemoveModule, Path: path,
		Detail: "bicz-owned module with no backup to restore"}), nil
}

func planConfigRemoval(changes []Change, path string) ([]Change, error) {
	_, state, err := inspectTarget("uninstall: config", path, configMarker)
	if err != nil {
		return nil, err
	}
	switch state {
	case targetAbsent:
		return changes, nil
	case targetForeign:
		return changes, nil
	}
	backup := newestBackup(path)
	if backup != "" {
		if err := checkPathRoots(backup); err != nil {
			return nil, fmt.Errorf("uninstall: cannot safely restore %s: %w", backup, err)
		}
		return append(changes, Change{Kind: ChangeRestoreConfig, Path: path, Backup: backup,
			Detail: "restoring the newest bicz-owned config backup"}), nil
	}
	return append(changes, Change{Kind: ChangeRemoveConfig, Path: path,
		Detail: "bicz-owned config with no backup to restore"}), nil
}

// --- target inspection -------------------------------------------------------

type targetState int

const (
	targetAbsent targetState = iota
	targetOwned
	targetForeign
)

// inspectTarget reads a target and classifies it. It refuses symlinks anywhere
// on the path (including the target), unreadable targets, and it refuses to
// classify a file whose ownership cannot be proven.
func inspectTarget(what, path, marker string) ([]byte, targetState, error) {
	if err := checkPathRoots(path); err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", what, path, err)
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, targetAbsent, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: unreadable; refusing to touch it: %w", what, path, err)
	}
	if isOwned(content, marker) {
		return content, targetOwned, nil
	}
	return content, targetForeign, nil
}

// isOwned proves bicz ownership: the marker prefix for modules, the JSON kind
// for configs.
func isOwned(content []byte, marker string) bool {
	if marker == configMarker {
		var header struct {
			BiczOwned bool   `json:"bicz_owned"`
			Kind      string `json:"kind"`
		}
		if err := json.Unmarshal(content, &header); err != nil {
			return false
		}
		return header.BiczOwned && knownConfigKind(header.Kind)
	}
	return strings.HasPrefix(string(content), marker) && strings.Contains(string(content), "bicz.adapter/1")
}

// configMarker routes ownership checks to the JSON form.
const configMarker = "<json-config>"

// checkPathRoots refuses symlinks and traversal on the path, walking only the
// components at-or-below the anchoring root (HOME, or an env-relocated root).
// Components above the anchor are the OS's business — /var is a symlink to
// /private/var on macOS and is not a traversal we police.
func checkPathRoots(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing a relative path %q", path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return fmt.Errorf("refusing a non-canonical path %q (cleans to %q)", path, clean)
	}
	home := userHome()
	if home != "." && !withinHome(home, clean) && !envRelocates(clean) {
		return fmt.Errorf("refusing %q: it escapes the user home %q; user-scope only", clean, home)
	}
	anchor := pathAnchor(clean)
	if anchor == "" {
		return fmt.Errorf("refusing %q: no user-scope root contains it", clean)
	}
	rel, err := filepath.Rel(anchor, clean)
	if err != nil {
		return fmt.Errorf("refusing %q: cannot relate it to %s: %w", clean, anchor, err)
	}
	component := anchor
	if component == "." {
		component = filepath.Clean("/")
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		component = filepath.Join(component, part)
		info, lerr := os.Lstat(component)
		if lerr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing %q: %s is a symlink", clean, component)
			}
		} else if !errors.Is(lerr, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", component, lerr)
		}
	}
	return nil
}

// pathAnchor returns the deepest anchoring root (user home or an env-relocated
// root) that is a path prefix of clean.
func pathAnchor(clean string) string {
	home := userHome()
	var candidates []string
	if home != "." {
		candidates = append(candidates, filepath.Clean(home))
	}
	candidates = append(candidates, envRoots()...)
	var deepest string
	for _, candidate := range candidates {
		if candidate == "." || !filepath.IsAbs(candidate) {
			continue
		}
		if clean == candidate || strings.HasPrefix(clean, candidate+string(filepath.Separator)) {
			if len(candidate) > len(deepest) {
				deepest = candidate
			}
		}
	}
	return deepest
}

// envRoots returns the absolute, cleaned, traversal-free roots an env var
// explicitly relocated. A root containing ".." is NOT a legitimate relocation:
// it would let an env value climb out of the user scope, so it is ignored here
// (and the target is then refused by the within-home check).
func envRoots() []string {
	var roots []string
	for _, root := range []string{
		os.Getenv("BICZ_CONFIG_DIR"),
		os.Getenv("PI_CONFIG_DIR"),
		os.Getenv("PI_CODING_AGENT_DIR"),
		os.Getenv("BICZ_LEDGER"),
		os.Getenv("BICZ_RUNTIME_DIR"),
		os.Getenv("XDG_CONFIG_HOME"),
	} {
		if root == "" {
			continue
		}
		// The RAW value is checked before any cleaning: a ".." component in the
		// operator's env must refuse relocation, not be resolved away.
		if hasDotDot(root) {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		roots = append(roots, filepath.Clean(absolute))
	}
	return roots
}

// hasDotDot reports whether any path component is "..".
func hasDotDot(path string) bool {
	if path == ".." {
		return true
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

// envRelocates reports whether an env var explicitly set a root that
// legitimately contains the path. Only traversal-free roots (see envRoots)
// count: a root with ".." never legitimizes an escape from the user scope.
func envRelocates(path string) bool {
	clean := filepath.Clean(path)
	for _, root := range envRoots() {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// stageBackup copies existing bicz-owned bytes to a timestamped sibling. The
// timestamp is sortable so the newest backup is the one uninstall restores.
func stageBackup(path string, content []byte) (string, error) {
	backup := path + ".bak-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.WriteFile(backup, content, 0o644); err != nil {
		return "", fmt.Errorf("install: write backup %s: %w", backup, err)
	}
	return backup, nil
}

// newestBackup finds the lexicographically newest sibling backup of a target,
// which is also the newest by time because of the sortable timestamp format.
func newestBackup(path string) string {
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// writeModule writes the embedded module atomically, taking the backup that was
// staged during planning.
func writeModule(path, backup string) error {
	return writeFileAtomic(path, ompExtensionModule, backup)
}

// writeFileAtomic writes content to a temp file in the same directory and
// renames it over the target. The backup (already on disk) is renamed into
// place only by restore; this writer never clobbers without a staged backup of
// a bicz-owned predecessor.
func writeFileAtomic(path string, content []byte, backup string) error {
	return writeFileAtomicMode(path, content, backup, 0o644)
}

// writeFileAtomicMode is writeFileAtomic with an explicit file mode (a user's
// settings file keeps the mode it already had).
func writeFileAtomicMode(path string, content []byte, backup string, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("install: create %s: %w", parent, err)
	}
	temp, err := os.CreateTemp(parent, ".bicz-*.tmp")
	if err != nil {
		return fmt.Errorf("install: temp file in %s: %w", parent, err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return fmt.Errorf("install: write %s: %w", tempName, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("install: sync %s: %w", tempName, err)
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("install: chmod %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("install: close %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("install: replace %s: %w", path, err)
	}
	tempName = ""
	return nil
}

// restoreBackup moves a staged backup back over its target.
func restoreBackup(backup, path string) error {
	if err := checkPathRoots(backup); err != nil {
		return fmt.Errorf("uninstall: cannot restore %s: %w", backup, err)
	}
	if err := os.Rename(backup, path); err != nil {
		return fmt.Errorf("uninstall: restore %s -> %s: %w", backup, path, err)
	}
	return nil
}

// removeEmptyConfigDir removes the bicz config directory after uninstall when
// it is empty (it is a bicz-owned directory).
func (p *Plan) removeEmptyConfigDir() {
	dir := filepath.Dir(p.ConfigPath)
	if !strings.HasSuffix(dir, string(filepath.Separator)+"bicz") && filepath.Base(dir) != "bicz" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// removeStaleBackups deletes bicz-owned backup files that uninstall did not
// restore: every sibling *.bak-* of a removed/restored target.
func (p *Plan) removeStaleBackups() {
	remove := func(target string) {
		matches, err := filepath.Glob(target + ".bak-*")
		if err != nil {
			return
		}
		for _, match := range matches {
			_ = os.Remove(match)
		}
	}
	for _, change := range p.Changes {
		switch change.Kind {
		case ChangeRemoveModule, ChangeRemoveConfig, ChangeRestoreModule, ChangeRestoreConfig, ChangeRemoveAsset:
			remove(change.Path)
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	_ = a[len(b)-1]
	_ = b[len(a)-1]
	return string(a) == string(b)
}
