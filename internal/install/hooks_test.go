package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hookEnv is testEnv plus the harness relocation variables cleared, so every
// path stays inside the temp HOME.
func hookEnv(t *testing.T) (home, exe string) {
	t.Helper()
	home = testEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home, filepath.Join(home, "bin", "bicz")
}

func mustApply(t *testing.T, plan *Plan, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

// userSettings is a realistic settings.json with keys and hooks bicz must never
// disturb. It is deliberately 4-space indented with a non-alphabetical order.
const userSettings = `{
    "model": "opus",
    "permissions": {"allow": ["Bash(git status)"], "deny": ["Read(./.env)"]},
    "hooks": {
        "PreToolUse": [
            {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/audit-bash", "timeout": 10}]}
        ],
        "Notification": [
            {"hooks": [{"type": "command", "command": "notify-send done"}]}
        ]
    },
    "env": {"FOO": "bar"}
}
`

func TestClaudeInstallMergesPreservingEverythingElseAndUninstallRestoresExactly(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	mustApply(t, plan, err)

	after, _ := os.ReadFile(settings)
	doc := decode(t, after)
	original := decode(t, []byte(userSettings))
	for _, key := range []string{"model", "permissions", "env"} {
		if !reflect.DeepEqual(doc[key], original[key]) {
			t.Fatalf("unrelated key %q changed: %v", key, doc[key])
		}
	}
	events := doc["hooks"].(map[string]any)
	if !reflect.DeepEqual(events["Notification"], original["hooks"].(map[string]any)["Notification"]) {
		t.Fatalf("unrelated event Notification changed: %v", events["Notification"])
	}
	groups := events["PreToolUse"].([]any)
	if len(groups) != 2 {
		t.Fatalf("PreToolUse must keep the user's group and add one bicz group, got %d: %v", len(groups), groups)
	}
	if !reflect.DeepEqual(groups[0], original["hooks"].(map[string]any)["PreToolUse"].([]any)[0]) {
		t.Fatalf("the user's PreToolUse hook was disturbed: %v", groups[0])
	}
	for _, event := range []string{"SessionStart", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PreCompact", "UserPromptExpansion"} {
		want := exe + " hook --harness claude --event " + event
		if !strings.Contains(string(after), want) {
			t.Fatalf("settings missing the %s hook command %q:\n%s", event, want, after)
		}
	}
	if info, _ := os.Stat(settings); info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, want the original 0600 kept", info.Mode().Perm())
	}

	// Idempotent: a second plan has nothing to do, and no backup piles up.
	second, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if !second.Empty() {
		t.Fatalf("second install must be a no-op, got %+v", second.Changes)
	}
	if backups, _ := filepath.Glob(settings + ".bak-*"); len(backups) != 1 {
		t.Fatalf("want exactly the one pristine backup, got %v", backups)
	}

	// Uninstall restores the original bytes exactly (indentation included).
	un, err := PlanUninstall("claude")
	mustApply(t, un, err)
	restored, _ := os.ReadFile(settings)
	if string(restored) != userSettings {
		t.Fatalf("uninstall did not restore the file exactly:\n--- got\n%s\n--- want\n%s", restored, userSettings)
	}
	if backups, _ := filepath.Glob(settings + ".bak-*"); len(backups) != 0 {
		t.Fatalf("a consumed backup was left behind: %v", backups)
	}
	if _, err := os.Stat(ConfigPath("claude")); !os.IsNotExist(err) {
		t.Fatalf("the bicz config must be removed on uninstall, stat err = %v", err)
	}
	again, err := PlanUninstall("claude")
	if err != nil || !again.Empty() {
		t.Fatalf("a second uninstall must be a no-op: %v %+v", err, again)
	}
}

func TestClaudeInstallCreatesAndUninstallRemovesABiczCreatedFile(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	plan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	mustApply(t, plan, err)
	doc := decode(t, mustRead(t, settings))
	if len(doc["hooks"].(map[string]any)) != 6 {
		t.Fatalf("want 6 hook events, got %v", doc["hooks"])
	}
	if backups, _ := filepath.Glob(settings + ".bak-*"); len(backups) != 0 {
		t.Fatalf("creating a file needs no backup: %v", backups)
	}

	un, err := PlanUninstall("claude")
	mustApply(t, un, err)
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("a settings file bicz created must be removed again, stat err = %v", err)
	}
}

func TestUninstallAfterUserEditsKeepsTheEditsAndRemovesOnlyBicz(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o755)
	_ = os.WriteFile(settings, []byte(userSettings), 0o644)
	plan, err := PlanInstall(Options{Harness: "claude", Executable: exe})
	mustApply(t, plan, err)

	// The user edits the file after install.
	doc := decode(t, mustRead(t, settings))
	doc["model"] = "sonnet"
	edited, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(settings, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	un, err := PlanUninstall("claude")
	mustApply(t, un, err)
	got := decode(t, mustRead(t, settings))
	if got["model"] != "sonnet" {
		t.Fatalf("a user edit was lost on uninstall: %v", got["model"])
	}
	want := decode(t, []byte(userSettings))
	if !reflect.DeepEqual(got["hooks"], want["hooks"]) {
		t.Fatalf("hooks after uninstall = %v, want the user's own %v", got["hooks"], want["hooks"])
	}
	if strings.Contains(string(mustRead(t, settings)), "bicz hook") {
		t.Fatal("a bicz entry survived uninstall")
	}
}

func TestReinstallWithMovedExecutableUpdatesInPlace(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o755)
	_ = os.WriteFile(settings, []byte(userSettings), 0o644)
	plan, err := PlanInstall(Options{Harness: "claude", Executable: exe})
	mustApply(t, plan, err)

	moved := filepath.Join(home, "other dir", "bicz")
	plan, err = PlanInstall(Options{Harness: "claude", Executable: moved})
	mustApply(t, plan, err)
	body := string(mustRead(t, settings))
	if strings.Contains(body, exe+" hook") {
		t.Fatalf("the old executable path survived a re-install:\n%s", body)
	}
	if strings.Count(body, "bicz hook --harness claude --event PreToolUse") != 1 {
		t.Fatalf("PreToolUse must carry exactly one bicz entry:\n%s", body)
	}
	if !strings.Contains(body, `other\\ dir/bicz hook --harness claude`) {
		t.Fatalf("an executable path with a space must stay one shell word and keep the marker:\n%s", body)
	}

	un, err := PlanUninstall("claude")
	mustApply(t, un, err)
	if strings.Contains(string(mustRead(t, settings)), "bicz hook") {
		t.Fatal("uninstall missed an entry written for a path with a space")
	}
}

func TestInstallConfigKeepsOperatorKeys(t *testing.T) {
	_, exe := hookEnv(t)
	plan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	mustApply(t, plan, err)
	path := ConfigPath("claude")
	cfg := decode(t, mustRead(t, path))
	if cfg["kind"] != "bicz.claude-config" || cfg["bicz_owned"] != true || cfg["account_id"] != "alice" || cfg["unmanaged_mode"] != "notice" {
		t.Fatalf("config = %v", cfg)
	}
	cfg["schedule_path"] = "/somewhere/schedule.json"
	cfg["unmanaged_mode"] = "block"
	edited, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(path, append(edited, '\n'), 0o644)

	plan, err = PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("operator-edited config must not be rewritten when identity is unchanged: %+v", plan.Changes)
	}
}

func TestCodexInstallHonorsCodexHomeAndRoundTrips(t *testing.T) {
	home, exe := hookEnv(t)
	t.Setenv("CODEX_HOME", filepath.Join(home, "custom-codex"))
	hooksFile := filepath.Join(home, "custom-codex", "hooks.json")
	_ = os.MkdirAll(filepath.Dir(hooksFile), 0o755)
	original := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}` + "\n"
	_ = os.WriteFile(hooksFile, []byte(original), 0o644)

	plan, err := PlanInstall(Options{Harness: "codex", Account: "alice", Executable: exe})
	mustApply(t, plan, err)
	doc := decode(t, mustRead(t, hooksFile))
	events := doc["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop"} {
		if _, ok := events[event]; !ok {
			t.Fatalf("event %s missing: %v", event, events)
		}
	}
	if _, ok := events["PostToolUseFailure"]; ok {
		t.Fatal("Codex has no PostToolUseFailure event; it must not be installed")
	}
	if !strings.Contains(string(mustRead(t, hooksFile)), exe+" hook --harness codex --event PreToolUse") {
		t.Fatal("codex PreToolUse command missing")
	}
	if _, err := os.Stat(ConfigPath("codex")); err != nil {
		t.Fatalf("codex config not written: %v", err)
	}

	un, err := PlanUninstall("codex")
	mustApply(t, un, err)
	if string(mustRead(t, hooksFile)) != original {
		t.Fatalf("codex uninstall did not restore exactly: %s", mustRead(t, hooksFile))
	}
}

func TestDryRunWritesNothingAndShowsTheDiff(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o755)
	_ = os.WriteFile(settings, []byte(userSettings), 0o644)

	plan, err := PlanInstall(Options{Harness: "claude", Executable: exe, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	out := capturePrint(t, plan.Print)
	if !strings.Contains(out, "+ ") || !strings.Contains(out, "bicz hook --harness claude --event SessionStart") {
		t.Fatalf("dry-run diff must show the added hook lines:\n%s", out)
	}
	if string(mustRead(t, settings)) != userSettings {
		t.Fatal("planning modified the settings file")
	}
	if backups, _ := filepath.Glob(settings + ".bak-*"); len(backups) != 0 {
		t.Fatalf("planning wrote a backup: %v", backups)
	}
	if _, err := os.Stat(ConfigPath("claude")); !os.IsNotExist(err) {
		t.Fatalf("planning wrote the config: %v", err)
	}
}

func TestHookInstallRefusals(t *testing.T) {
	home, exe := hookEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o755)

	cases := map[string]struct {
		content string
		want    string
	}{
		"malformed JSON":        {`{"hooks": `, "malformed"},
		"top level not object":  {`["hooks"]`, "malformed"},
		"empty file":            {"", "malformed"},
		"hooks not an object":   {`{"hooks": []}`, "not a JSON object"},
		"event not an array":    {`{"hooks": {"PreToolUse": {}}}`, "not an array"},
		"group not an object":   {`{"hooks": {"PreToolUse": [1]}}`, "not a JSON object"},
		"group hooks not array": {`{"hooks": {"PreToolUse": [{"hooks": {}}]}}`, "non-array"},
		"duplicate key":         {`{"model":"a","model":"b"}`, "duplicate"},
	}
	for name, tc := range cases {
		if err := os.WriteFile(settings, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := PlanInstall(Options{Harness: "claude", Executable: exe})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: install must refuse with %q, got %v", name, tc.want, err)
		}
		if _, err := PlanUninstall("claude"); err == nil {
			t.Fatalf("%s: uninstall must refuse too", name)
		}
		if got := string(mustRead(t, settings)); got != tc.content {
			t.Fatalf("%s: a refused file was modified: %q", name, got)
		}
	}
}

func TestHookInstallRefusesSymlinkedTargets(t *testing.T) {
	home, exe := hookEnv(t)
	real := filepath.Join(home, "real-settings.json")
	_ = os.WriteFile(real, []byte("{}\n"), 0o644)
	dir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.Symlink(real, filepath.Join(dir, "settings.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked settings file must be refused, got %v", err)
	}
	if _, err := PlanUninstall("claude"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("uninstall must refuse a symlinked settings file, got %v", err)
	}

	// A symlinked parent directory is refused as well.
	_ = os.Remove(filepath.Join(dir, "settings.json"))
	_ = os.Remove(dir)
	elsewhere := filepath.Join(home, "elsewhere")
	_ = os.MkdirAll(elsewhere, 0o755)
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked config directory must be refused, got %v", err)
	}
}

func TestHookInstallRefusesPathsOutsideHome(t *testing.T) {
	home, exe := hookEnv(t)
	outside := t.TempDir() // a different temp tree than the fake HOME
	t.Setenv("CLAUDE_CONFIG_DIR", outside)
	// An env-relocated bicz root must not widen the allowed scope either.
	t.Setenv("XDG_CONFIG_HOME", outside)
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), "outside the user home") {
		t.Fatalf("CLAUDE_CONFIG_DIR outside HOME must be refused, got %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", home+"/../escape")
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), `".."`) {
		t.Fatalf("a root containing .. must be refused, got %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "relative/dir")
	if _, err := PlanInstall(Options{Harness: "codex", Executable: exe}); err == nil {
		t.Fatal("a relative CODEX_HOME must be refused")
	}
}

func TestHookInstallRefusesForeignBiczConfigAndBadExecutable(t *testing.T) {
	_, exe := hookEnv(t)
	cfg := ConfigPath("claude")
	_ = os.MkdirAll(filepath.Dir(cfg), 0o755)
	_ = os.WriteFile(cfg, []byte(`{"someone":"else"}`), 0o644)
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), "not a bicz-owned config") {
		t.Fatalf("a foreign config must be refused, got %v", err)
	}
	_ = os.Remove(cfg)
	for _, bad := range []string{"relative/bicz", filepath.Join(t.TempDir(), "not-bicz")} {
		if _, err := PlanInstall(Options{Harness: "claude", Executable: bad}); err == nil {
			t.Fatalf("executable %q must be refused", bad)
		}
	}
}

func TestClaudeCodeAliasInstallsTheClaudeAdapter(t *testing.T) {
	home, exe := hookEnv(t)
	plan, err := PlanInstall(Options{Harness: "claude-code", Executable: exe})
	mustApply(t, plan, err)
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatalf("claude-code alias did not install: %v", err)
	}
	if _, err := os.Stat(ConfigPath("claude")); err != nil {
		t.Fatalf("alias must use the claude config: %v", err)
	}
}

func TestInstallScheduleFlagsWriteConfigKeys(t *testing.T) {
	_, exe := hookEnv(t)
	plan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe,
		SchedulePath: "/somewhere/schedule.json", PolicyVersion: "1"})
	mustApply(t, plan, err)
	cfg := decode(t, mustRead(t, ConfigPath("claude")))
	if cfg["schedule_path"] != "/somewhere/schedule.json" || cfg["policy_version"] != "1" {
		t.Fatalf("schedule flags must land in the config, got %v", cfg)
	}

	// A flagless reinstall keeps the operator's schedule keys.
	plan, err = PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	mustApply(t, plan, err)
	cfg = decode(t, mustRead(t, ConfigPath("claude")))
	if cfg["schedule_path"] != "/somewhere/schedule.json" || cfg["policy_version"] != "1" {
		t.Fatalf("flagless reinstall must keep schedule keys, got %v", cfg)
	}

	// An explicit flag wins over the stored value.
	plan, err = PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe, PolicyVersion: "2"})
	mustApply(t, plan, err)
	cfg = decode(t, mustRead(t, ConfigPath("claude")))
	if cfg["policy_version"] != "2" {
		t.Fatalf("explicit flag must override stored policy_version, got %v", cfg)
	}
}

// A relative --schedule must be stored absolute: the adapter reads the file
// from whatever directory the harness later starts in, so a cwd-relative path
// silently unmeters every session started elsewhere.
func TestInstallScheduleRelativePathResolvedAgainstCwd(t *testing.T) {
	home := testEnv(t)
	schedule := filepath.Join(home, "schedule.json")
	_ = os.WriteFile(schedule, []byte(`{"initial_capital_mc":1}`), 0o644)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanInstall(Options{Harness: "omp", Account: "alice", SchedulePath: "schedule.json", PolicyVersion: "1"})
	mustApply(t, plan, err)
	cfg := decode(t, mustRead(t, ConfigPath("omp")))
	// macOS reports /tmp as /private/tmp; compare resolved paths, not strings.
	stored := cfg["schedule_path"].(string)
	want, err := filepath.EvalSymlinks(schedule)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(stored)
	if err != nil {
		t.Fatalf("stored schedule_path %q is not a readable file: %v", stored, err)
	}
	if got != want {
		t.Fatalf("relative --schedule must be stored absolute (%s), got %s", want, got)
	}
	if cfg["policy_version"] != "1" {
		t.Fatalf("policy_version missing: %v", cfg)
	}
}

func TestOpenCodeInstallScheduleFlagsWriteConfigKeys(t *testing.T) {
	testEnv(t)
	plan, err := PlanOpenCodeInstall(Options{SchedulePath: "/somewhere/schedule.json", PolicyVersion: "1"}, 2)
	mustApply(t, plan, err)
	cfg := decode(t, mustRead(t, OpenCodeConfigPath()))
	if cfg["schedule_path"] != "/somewhere/schedule.json" || cfg["policy_version"] != "1" {
		t.Fatalf("schedule flags must land in the OpenCode config, got %v", cfg)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
