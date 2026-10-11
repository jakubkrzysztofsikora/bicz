package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/hooks"
)

// This file installs the Claude Code and Codex hook adapters. Unlike the OMP
// module (a bicz-owned file), the target is the USER'S OWN settings file:
//
//   - Claude Code: $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json
//   - Codex:       $CODEX_HOME/hooks.json,            else ~/.codex/hooks.json
//
// so the rules are stricter than for a bicz-owned file. Only entries whose
// command contains `bicz hook --harness <id>` are ever added, changed, or
// removed; every other key and hook is carried through byte-for-byte (values
// are kept as raw JSON; only the whole-file indentation is normalized). Every
// rewrite takes a timestamped backup of the previous bytes, preserving the file
// mode, and uninstall restores the original bytes exactly when a pristine
// backup still matches.

// hookTarget names where a harness keeps its hooks file.
type hookTarget struct {
	spec   *hooks.Spec
	envDir string // env var that relocates the harness root
	dir    string // default root under $HOME
	file   string
}

func hookTargetFor(id string) (hookTarget, bool) {
	spec, ok := hooks.SpecFor(id)
	if !ok {
		return hookTarget{}, false
	}
	switch spec.ID {
	case "claude":
		return hookTarget{spec: spec, envDir: "CLAUDE_CONFIG_DIR", dir: ".claude", file: "settings.json"}, true
	case "codex":
		return hookTarget{spec: spec, envDir: "CODEX_HOME", dir: ".codex", file: "hooks.json"}, true
	}
	return hookTarget{}, false
}

// root is the harness's user root.
func (t hookTarget) root() string {
	if explicit := os.Getenv(t.envDir); explicit != "" {
		return explicit
	}
	return filepath.Join(userHome(), t.dir)
}

// path is the hooks file, validated to be inside $HOME. A relocation env var
// is NOT an escape hatch from the user scope: a root outside $HOME (or one
// containing "..") is refused.
func (t hookTarget) path() (string, error) {
	if explicit := os.Getenv(t.envDir); explicit != "" && hasDotDot(explicit) {
		return "", fmt.Errorf("refusing %s=%q: it contains \"..\"", t.envDir, explicit)
	}
	path := filepath.Join(t.root(), t.file)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("refusing relative hooks path %q", path)
	}
	home := userHome()
	if home == "." || !withinHome(home, path) {
		return "", fmt.Errorf("refusing %q: it is outside the user home %q; user-scope only", path, home)
	}
	return path, nil
}

// marker identifies this harness's bicz hook entries.
func hookMarker(spec *hooks.Spec) string { return "bicz hook --harness " + spec.ID }

// hookCommand is the command written for one event. The bicz executable is an
// absolute path, backslash-escaped (never quoted) so the literal marker
// `bicz hook --harness <id>` survives and identifies the entry.
func hookCommand(exe string, spec *hooks.Spec, event string) string {
	return shellEscape(exe) + " hook --harness " + spec.ID + " --event " + event
}

func shellEscape(path string) string {
	if runtime.GOOS == "windows" {
		return path
	}
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			strings.ContainsRune("_@%+=:,./-", r), r > 127:
			b.WriteRune(r)
		default:
			b.WriteByte('\\')
			b.WriteRune(r)
		}
	}
	return b.String()
}

func resolveExecutable(opts Options) (string, error) {
	exe := opts.Executable
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("install: cannot resolve the bicz executable: %w", err)
		}
	}
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("install: the bicz executable path %q is not absolute", exe)
	}
	exe = filepath.Clean(exe)
	base := strings.TrimSuffix(filepath.Base(exe), ".exe")
	if base != "bicz" {
		return "", fmt.Errorf("install: the executable %q is not named bicz; hook entries are identified by `bicz hook --harness ...`", exe)
	}
	return exe, nil
}

// --- ordered JSON -----------------------------------------------------------

// object is a JSON object that keeps member order and carries each value as raw
// JSON, so anything not deliberately edited is preserved exactly.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

func parseObject(raw []byte) (*object, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("not a JSON object")
	}
	obj := newObject()
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		if _, dup := obj.vals[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, key)
		obj.vals[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON object")
	}
	return obj, nil
}

func (o *object) set(key string, value []byte) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func (o *object) delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) bytes() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		b.Write(encoded)
		b.WriteByte(':')
		b.Write(o.vals[key])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// render indents a compact document with a trailing newline.
func render(compact []byte) ([]byte, error) {
	var squeezed, indented bytes.Buffer
	if err := json.Compact(&squeezed, compact); err != nil {
		return nil, err
	}
	if err := json.Indent(&indented, squeezed.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

// --- merge / unmerge ---------------------------------------------------------

// hookEntryCommand extracts the command of a hook entry, and whether it is one
// of this harness's bicz entries.
func isBiczEntry(entry json.RawMessage, marker string) bool {
	var probe struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(entry, &probe); err != nil {
		return false
	}
	return strings.Contains(probe.Command, marker)
}

type groupParts struct {
	obj     *object
	entries []json.RawMessage
}

// parseGroup reads one matcher group. A group with no "hooks" array is foreign
// and left alone (nil, nil).
func parseGroup(raw json.RawMessage) (*groupParts, error) {
	obj, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("a hook matcher group is not a JSON object: %w", err)
	}
	inner, ok := obj.vals["hooks"]
	if !ok {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(inner, &entries); err != nil {
		return nil, fmt.Errorf("a hook matcher group has a non-array \"hooks\": %w", err)
	}
	return &groupParts{obj: obj, entries: entries}, nil
}

func (g *groupParts) bytes() []byte {
	encoded, _ := json.Marshal(g.entries)
	g.obj.set("hooks", encoded)
	return g.obj.bytes()
}

// hooksDoc is the parsed settings document.
type hooksDoc struct {
	root   *object
	events *object // nil when the document has no "hooks" key
}

func parseHooksDoc(content []byte) (*hooksDoc, error) {
	root, err := parseObject(content)
	if err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	doc := &hooksDoc{root: root}
	if raw, ok := root.vals["hooks"]; ok {
		events, err := parseObject(raw)
		if err != nil {
			return nil, fmt.Errorf("\"hooks\" is not a JSON object: %w", err)
		}
		doc.events = events
	}
	return doc, nil
}

func (d *hooksDoc) groups(event string) ([]json.RawMessage, error) {
	if d.events == nil {
		return nil, nil
	}
	raw, ok := d.events.vals[event]
	if !ok {
		return nil, nil
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("hooks.%s is not an array: %w", event, err)
	}
	return groups, nil
}

func (d *hooksDoc) setGroups(event string, groups []json.RawMessage) {
	if d.events == nil {
		d.events = newObject()
	}
	if len(groups) == 0 {
		d.events.delete(event)
		return
	}
	encoded, _ := json.Marshal(groups)
	d.events.set(event, encoded)
}

// finish writes the events object back into the root, dropping an emptied
// "hooks" key.
func (d *hooksDoc) finish() ([]byte, error) {
	if d.events != nil {
		if len(d.events.keys) == 0 {
			d.root.delete("hooks")
		} else {
			d.root.set("hooks", d.events.bytes())
		}
	}
	return render(d.root.bytes())
}

// ensureEvent makes the event carry exactly one bicz entry with the desired
// command. An existing entry is updated in place (position and every other key
// kept); a missing one is appended as its own matcher group.
func ensureEvent(groups []json.RawMessage, event hooks.EventSpec, command, marker string) ([]json.RawMessage, bool, error) {
	changed := false
	found := false
	out := make([]json.RawMessage, 0, len(groups)+1)
	for _, raw := range groups {
		group, err := parseGroup(raw)
		if err != nil {
			return nil, false, err
		}
		if group == nil {
			out = append(out, raw)
			continue
		}
		kept := make([]json.RawMessage, 0, len(group.entries))
		groupChanged := false
		for _, entry := range group.entries {
			if !isBiczEntry(entry, marker) {
				kept = append(kept, entry)
				continue
			}
			if found { // duplicate bicz entry: drop it
				groupChanged = true
				continue
			}
			found = true
			updated, same, err := withCommand(entry, command)
			if err != nil {
				return nil, false, err
			}
			if !same {
				groupChanged = true
			}
			kept = append(kept, updated)
		}
		if !groupChanged {
			out = append(out, raw)
			continue
		}
		changed = true
		if len(kept) == 0 {
			continue
		}
		group.entries = kept
		out = append(out, group.bytes())
	}
	if !found {
		fresh := newObject()
		if event.Matcher != "" {
			matcher, _ := json.Marshal(event.Matcher)
			fresh.set("matcher", matcher)
		}
		timeout := event.Timeout
		if timeout == 0 {
			timeout = 5
		}
		entry, _ := json.Marshal(map[string]any{"type": "command", "command": command, "timeout": timeout})
		fresh.set("hooks", append(append([]byte{'['}, entry...), ']'))
		out = append(out, fresh.bytes())
		changed = true
	}
	return out, changed, nil
}

// withCommand sets an entry's command, reporting whether it already matched.
func withCommand(entry json.RawMessage, command string) (json.RawMessage, bool, error) {
	obj, err := parseObject(entry)
	if err != nil {
		return nil, false, fmt.Errorf("a bicz hook entry is not a JSON object: %w", err)
	}
	var current string
	_ = json.Unmarshal(obj.vals["command"], &current)
	if current == command {
		return entry, true, nil
	}
	encoded, _ := json.Marshal(command)
	obj.set("command", encoded)
	return obj.bytes(), false, nil
}

// removeBiczEntries strips every bicz entry from one event, dropping matcher
// groups it empties. It returns the remaining groups and how many it removed.
func removeBiczEntries(groups []json.RawMessage, marker string) ([]json.RawMessage, int, error) {
	removed := 0
	out := make([]json.RawMessage, 0, len(groups))
	for _, raw := range groups {
		group, err := parseGroup(raw)
		if err != nil {
			return nil, 0, err
		}
		if group == nil {
			out = append(out, raw)
			continue
		}
		kept := make([]json.RawMessage, 0, len(group.entries))
		for _, entry := range group.entries {
			if isBiczEntry(entry, marker) {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == len(group.entries) {
			out = append(out, raw)
			continue
		}
		if len(kept) == 0 {
			continue
		}
		group.entries = kept
		out = append(out, group.bytes())
	}
	return out, removed, nil
}

// --- planning ----------------------------------------------------------------

// readHooksFile inspects the target: symlinks anywhere on the path are refused,
// a missing file is fine, anything unreadable or malformed is refused.
func readHooksFile(what, path string) ([]byte, os.FileMode, bool, error) {
	if err := checkPathRoots(path); err != nil {
		return nil, 0, false, fmt.Errorf("%s %s: %w", what, path, err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o644, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("%s %s: unreadable; refusing to touch it: %w", what, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s %s: not a regular file; refusing to touch it", what, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("%s %s: unreadable; refusing to touch it: %w", what, path, err)
	}
	return content, info.Mode().Perm(), true, nil
}

func planHooksInstall(opts Options, target hookTarget) (*Plan, error) {
	spec := target.spec
	exe, err := resolveExecutable(opts)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(opts.Account)
	if account == "" {
		account = DefaultAccount()
	}
	ledger := strings.TrimSpace(opts.LedgerPath)
	if ledger == "" {
		ledger = DefaultLedgerPath()
	}
	socketPath, err := DefaultSocketPath(ledger)
	if err != nil {
		return nil, err
	}
	path, err := target.path()
	if err != nil {
		return nil, fmt.Errorf("install: %w", err)
	}
	configPath := ConfigPath(spec.ID)
	plan := &Plan{ModulePath: path, ConfigPath: configPath, Account: account, Ledger: ledger, hookMarker: hookMarker(spec)}

	content, mode, exists, err := readHooksFile("install: hooks file", path)
	if err != nil {
		return nil, err
	}
	base := []byte("{}")
	if exists {
		base = content
	}
	doc, err := parseHooksDoc(base)
	if err != nil {
		return nil, fmt.Errorf("install: %s is malformed (%v); refusing to touch it", path, err)
	}
	if exists && len(bytes.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("install: %s is empty; refusing to touch it", path)
	}
	marker := hookMarker(spec)
	changed := false
	for _, event := range spec.Events {
		groups, err := doc.groups(event.Name)
		if err != nil {
			return nil, fmt.Errorf("install: %s: %v; refusing to touch it", path, err)
		}
		next, did, err := ensureEvent(groups, event, hookCommand(exe, spec, event.Name), marker)
		if err != nil {
			return nil, fmt.Errorf("install: %s: %v; refusing to touch it", path, err)
		}
		if did {
			changed = true
			doc.setGroups(event.Name, next)
		}
	}
	// bicz entries under events this harness no longer installs are stale too.
	if doc.events != nil {
		for _, key := range append([]string(nil), doc.events.keys...) {
			if _, ours := spec.Event(key); ours {
				continue
			}
			groups, err := doc.groups(key)
			if err != nil {
				return nil, fmt.Errorf("install: %s: %v; refusing to touch it", path, err)
			}
			next, removed, err := removeBiczEntries(groups, marker)
			if err != nil {
				return nil, fmt.Errorf("install: %s: %v; refusing to touch it", path, err)
			}
			if removed > 0 {
				changed = true
				doc.setGroups(key, next)
			}
		}
	}
	if changed {
		body, err := doc.finish()
		if err != nil {
			return nil, fmt.Errorf("install: render %s: %w", path, err)
		}
		change := Change{Kind: ChangeWriteHooks, Path: path, Body: body, Mode: mode,
			Diff: lineDiff(string(content), string(body))}
		if exists {
			// The backup is named now but written by Apply, so a dry run
			// leaves nothing on disk.
			change.Backup = backupName(path)
			change.Prior = content
			change.Detail = "merging bicz hook entries; every other key and hook is preserved; previous file backed up"
		} else {
			if err := checkPathRoots(filepath.Dir(path)); err != nil {
				return nil, fmt.Errorf("install: hooks directory %s: %w", filepath.Dir(path), err)
			}
			change.Detail = "hooks file does not exist yet"
		}
		plan.Changes = append(plan.Changes, change)
	}

	body, err := hookConfigBody(configPath, spec, account, ledger, socketPath)
	if err != nil {
		return nil, err
	}
	plan.ConfigBody = body
	plan.Changes, err = planConfigWrite(plan.Changes, configPath, body)
	if err != nil {
		return nil, err
	}
	plan.Changes, err = planSlashInstall(plan.Changes, spec.ID, target, exe)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// carryOperatorKeys copies every key of an existing bicz-owned adapter config
// into config, so a reinstall keeps operator-set keys (schedule_path,
// policy_version, modes); callers then refresh identity, ledger and socket.
// An absent or foreign file contributes nothing (the plan write refuses a
// foreign one).
func carryOperatorKeys(path string, config map[string]any) error {
	existing, state, err := inspectTarget("install: config", path, configMarker)
	if err != nil {
		return err
	}
	if state != targetOwned {
		return nil
	}
	var previous map[string]any
	if err := json.Unmarshal(existing, &previous); err == nil {
		for key, value := range previous {
			config[key] = value
		}
	}
	return nil
}

// hookConfigBody builds the adapter config. An existing bicz-owned config keeps
// every operator-set key; only the identity, ledger, and socket are refreshed.
func hookConfigBody(path string, spec *hooks.Spec, account, ledger, socket string) ([]byte, error) {
	config := map[string]any{
		"bicz_owned":       true,
		"kind":             spec.ConfigKind,
		"version":          1,
		"unmanaged_mode":   "notice",
		"no_schedule_mode": "notice",
	}
	if err := carryOperatorKeys(path, config); err != nil {
		return nil, err
	}
	config["account_id"] = account
	config["ledger"] = ledger
	config["socket"] = socket
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("install: encode config: %w", err)
	}
	return append(body, '\n'), nil
}

func planHooksUninstall(target hookTarget) (*Plan, error) {
	spec := target.spec
	path, err := target.path()
	if err != nil {
		return nil, fmt.Errorf("uninstall: %w", err)
	}
	marker := hookMarker(spec)
	configPath := ConfigPath(spec.ID)
	plan := &Plan{ModulePath: path, ConfigPath: configPath, hookMarker: marker, uninstall: true}

	content, _, exists, err := readHooksFile("uninstall: hooks file", path)
	if err != nil {
		return nil, err
	}
	if exists {
		doc, err := parseHooksDoc(content)
		if err != nil {
			return nil, fmt.Errorf("uninstall: %s is malformed (%v); refusing to touch it", path, err)
		}
		removedTotal := 0
		if doc.events != nil {
			for _, key := range append([]string(nil), doc.events.keys...) {
				groups, err := doc.groups(key)
				if err != nil {
					return nil, fmt.Errorf("uninstall: %s: %v; refusing to touch it", path, err)
				}
				next, removed, err := removeBiczEntries(groups, marker)
				if err != nil {
					return nil, fmt.Errorf("uninstall: %s: %v; refusing to touch it", path, err)
				}
				if removed > 0 {
					removedTotal += removed
					doc.setGroups(key, next)
				}
			}
		}
		if removedTotal > 0 {
			body, err := doc.finish()
			if err != nil {
				return nil, fmt.Errorf("uninstall: render %s: %w", path, err)
			}
			change, err := planHooksRemoval(path, content, body, doc, marker)
			if err != nil {
				return nil, err
			}
			plan.Changes = append(plan.Changes, change)
		}
	}
	plan.Changes, err = planConfigRemoval(plan.Changes, configPath)
	if err != nil {
		return nil, err
	}
	plan.Changes, err = planSlashUninstall(plan.Changes, spec.ID, target)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// planHooksRemoval picks how the hooks file returns to its pre-bicz state:
// restore a pristine backup byte-for-byte when it still matches, delete a file
// bicz created, or write the filtered document.
func planHooksRemoval(path string, current, filtered []byte, doc *hooksDoc, marker string) (Change, error) {
	backups := allBackups(path)
	want := normalizeDoc(filtered)
	for i := len(backups) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(backups[i])
		if err != nil || bytes.Contains(raw, []byte(marker)) {
			continue
		}
		if err := checkPathRoots(backups[i]); err != nil {
			continue
		}
		if reflect.DeepEqual(normalizeDoc(raw), want) && want != nil {
			return Change{Kind: ChangeRestoreHooks, Path: path, Backup: backups[i],
				Diff:   lineDiff(string(current), string(raw)),
				Detail: "removing bicz entries by restoring the pristine backup byte-for-byte"}, nil
		}
	}
	if len(doc.root.keys) == 0 && len(backups) == 0 {
		return Change{Kind: ChangeRemoveHooks, Path: path,
			Diff:   lineDiff(string(current), ""),
			Detail: "the file holds only bicz entries and no backup exists, so bicz created it; removing it"}, nil
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	return Change{Kind: ChangeWriteHooks, Path: path, Body: filtered, Mode: mode,
		Diff:   lineDiff(string(current), string(filtered)),
		Detail: "removing only bicz hook entries; every other key and hook is preserved"}, nil
}

// normalizeDoc parses a settings document for semantic comparison, ignoring
// formatting and any empty hooks containers. It returns nil for malformed
// input.
func normalizeDoc(raw []byte) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if events, ok := root["hooks"].(map[string]any); ok {
		for name, groups := range events {
			if list, ok := groups.([]any); ok && len(list) == 0 {
				delete(events, name)
			}
		}
		if len(events) == 0 {
			delete(root, "hooks")
		}
	}
	return root
}

func allBackups(path string) []string {
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

// cleanHookBackups removes the backups a finished uninstall made redundant: any
// backup that still carries bicz entries (an intermediate state). A restored
// backup is already consumed; pristine backups that were not restored (the user
// changed the file since) are kept.
func (p *Plan) cleanHookBackups() {
	for _, backup := range allBackups(p.ModulePath) {
		raw, err := os.ReadFile(backup)
		if err == nil && bytes.Contains(raw, []byte(p.hookMarker)) {
			_ = os.Remove(backup)
		}
	}
}

// backupName is a sortable timestamped sibling of path, the same shape
// stageBackup uses so newestBackup and allBackups agree.
func backupName(path string) string {
	return path + ".bak-" + time.Now().UTC().Format("20060102T150405.000000000Z")
}

// --- diff --------------------------------------------------------------------

// lineDiff renders a line diff with two lines of context.
func lineDiff(oldText, newText string) string {
	a := splitLines(oldText)
	b := splitLines(newText)
	// Longest common subsequence table.
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	type op struct {
		tag  byte
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j]})
	}
	const context = 2
	show := make([]bool, len(ops))
	for k, o := range ops {
		if o.tag == ' ' {
			continue
		}
		for d := -context; d <= context; d++ {
			if k+d >= 0 && k+d < len(ops) {
				show[k+d] = true
			}
		}
	}
	var out strings.Builder
	skipped := false
	for k, o := range ops {
		if !show[k] {
			skipped = true
			continue
		}
		if skipped {
			out.WriteString("  ...\n")
			skipped = false
		}
		out.WriteByte(o.tag)
		out.WriteByte(' ')
		out.WriteString(o.text)
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

// knownConfigKind accepts the kind marker of every bicz-owned adapter config.
func knownConfigKind(kind string) bool {
	switch kind {
	case configKind, hooks.ClaudeSpec.ConfigKind, hooks.CodexSpec.ConfigKind, "bicz.opencode-config":
		return true
	}
	return false
}
