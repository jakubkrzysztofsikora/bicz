package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakubkrzysztofsikora/bicz/internal/hooks"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			body, _ := os.ReadFile(path)
			out[rel] = string(body)
		}
		return nil
	})
	return out
}

func TestClaudeSkillInstallCoexistsWithOtherSkillsAndUninstallsCleanly(t *testing.T) {
	home, exe := hookEnv(t)
	root := filepath.Join(home, ".claude")
	writeFile(t, filepath.Join(root, "settings.json"), userSettings)
	writeFile(t, filepath.Join(root, "skills", "deploy", "SKILL.md"), "---\nname: deploy\n---\nship it\n")
	writeFile(t, filepath.Join(root, "skills", "bicz-notes", "SKILL.md"), "my own notes skill\n")
	writeFile(t, filepath.Join(root, "commands", "bicz.md"), "a user command that is not ours\n")
	before := treeOf(t, root)

	// Dry run: the plan names the skill and writes nothing.
	plan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	if err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, "skills", "bicz", "SKILL.md")
	var planned bool
	for _, change := range plan.Changes {
		if change.Path == skill && change.Kind == ChangeWriteAsset && strings.Contains(change.Diff, "disable-model-invocation: true") {
			planned = true
		}
	}
	if !planned {
		t.Fatalf("the plan does not show the skill write: %+v", plan.Changes)
	}
	if !reflectEqualTrees(before, treeOf(t, root)) {
		t.Fatal("planning (a dry run) changed the harness directory")
	}

	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	body := string(mustRead(t, skill))
	for _, want := range []string{"name: bicz", "disable-model-invocation: true", assetMarker, "NOTHING was recorded"} {
		if !strings.Contains(body, want) {
			t.Errorf("skill lacks %q:\n%s", want, body)
		}
	}
	// The skill is inert: it carries no shell, no here-document, no placeholder
	// for typed text, no tool grant, and no path to the bicz binary.
	for _, banned := range []string{"```", "$ARGUMENTS", "!`", "<<", "allowed-tools", exe, "--arguments"} {
		if strings.Contains(strings.ReplaceAll(body, "`bicz command --harness claude --arguments '...'`", ""), banned) {
			t.Errorf("an inert skill must not contain %q:\n%s", banned, body)
		}
	}
	settingsBody := string(mustRead(t, filepath.Join(root, "settings.json")))
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settingsBody), &settings); err != nil {
		t.Fatal(err)
	}
	var slashHook bool
	slashSpec := hooks.EventSpec{}
	for _, event := range hooks.ClaudeSpec.Events {
		if event.Name == "UserPromptExpansion" {
			slashSpec = event
		}
	}
	for _, group := range settings.Hooks["UserPromptExpansion"] {
		for _, hook := range group.Hooks {
			if group.Matcher == "bicz" && hook.Command == shellEscape(exe)+" hook --harness claude --event UserPromptExpansion" && hook.Timeout == slashSpec.Timeout {
				slashHook = true
			}
		}
	}
	if !slashHook {
		t.Errorf("the UserPromptExpansion hook for /bicz is not installed with matcher bicz:\n%s", settingsBody)
	}
	for path, want := range before {
		if path == "settings.json" {
			continue
		}
		if got := string(mustRead(t, filepath.Join(root, path))); got != want {
			t.Errorf("%s was touched", path)
		}
	}

	// Idempotent.
	again, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	if err != nil || !again.Empty() {
		t.Fatalf("second install must be a no-op: %v %+v", err, again)
	}

	// A bicz-owned skill from an older version is rewritten with a backup.
	if err := os.WriteFile(skill, []byte("---\nname: bicz\n---\n<!-- "+assetMarker+" old -->\nold body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replan, err := PlanInstall(Options{Harness: "claude", Account: "alice", Executable: exe})
	if err != nil || replan.Empty() {
		t.Fatalf("an outdated skill must be replanned: %v", err)
	}
	if err := replan.Apply(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, skill)), "NOTHING was recorded") {
		t.Fatal("the skill was not updated")
	}
	if backups, _ := filepath.Glob(skill + ".bak-*"); len(backups) != 1 {
		t.Fatalf("expected one backup of the previous skill, got %v", backups)
	}

	// Uninstall removes only what bicz owns and restores settings.json exactly.
	un, err := PlanUninstall("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := un.Apply(); err != nil {
		t.Fatal(err)
	}
	after := treeOf(t, root)
	if !reflectEqualTrees(before, after) {
		t.Fatalf("uninstall did not restore the pre-install tree\nbefore=%v\nafter=%v", keys(before), keys(after))
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "bicz")); !os.IsNotExist(err) {
		t.Fatal("the emptied bicz skill directory was left behind")
	}
	if again, err := PlanUninstall("claude"); err != nil || !again.Empty() {
		t.Fatalf("a second uninstall must be a no-op: %v", err)
	}
}

func reflectEqualTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCodexSkillIsInstalledUnderAgentsSkillsWithImplicitInvocationOff(t *testing.T) {
	home, exe := hookEnv(t)
	other := filepath.Join(home, ".agents", "skills", "linear", "SKILL.md")
	writeFile(t, other, "linear skill\n")
	plan, err := PlanInstall(Options{Harness: "codex", Account: "alice", Executable: exe})
	mustApply(t, plan, err)

	dir := filepath.Join(home, ".agents", "skills", "bicz")
	skill := string(mustRead(t, filepath.Join(dir, "SKILL.md")))
	metadata := string(mustRead(t, filepath.Join(dir, "agents", "openai.yaml")))
	if !strings.Contains(metadata, "allow_implicit_invocation: false") {
		t.Errorf("the model must not be able to invoke the skill implicitly:\n%s", metadata)
	}
	for _, want := range []string{"name: bicz", "fallback", "BICZ_ARGS_", "16 random hex", "If they contain a line break, STOP",
		"Never run this skill unless", "Never choose, infer, change, or add"} {
		if !strings.Contains(skill, want) {
			t.Errorf("skill lacks %q:\n%s", want, skill)
		}
	}
	// No fixed, guessable delimiter and no per-command db/identity flags.
	for _, banned := range []string{"BICZ_SLASH_ARGUMENTS_END", "--db", "--identity", "$ARGUMENTS"} {
		if strings.Contains(skill, banned) {
			t.Errorf("the Codex skill must not contain %q", banned)
		}
	}
	hooksDoc := string(mustRead(t, filepath.Join(home, ".codex", "hooks.json")))
	if !strings.Contains(hooksDoc, exe+" hook --harness codex --event UserPromptSubmit") {
		t.Errorf("the UserPromptSubmit hook for $bicz is not installed:\n%s", hooksDoc)
	}
	// No deprecated custom prompt is created.
	if _, err := os.Stat(filepath.Join(home, ".codex", "prompts")); !os.IsNotExist(err) {
		t.Error("a deprecated ~/.codex/prompts entry was created")
	}

	un, err := PlanUninstall("codex")
	mustApply(t, un, err)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the bicz skill directory survived uninstall")
	}
	if string(mustRead(t, other)) != "linear skill\n" {
		t.Fatal("another skill was disturbed")
	}
}

func TestSlashAssetRefusesForeignFilesAndSymlinks(t *testing.T) {
	home, exe := hookEnv(t)
	skill := filepath.Join(home, ".claude", "skills", "bicz", "SKILL.md")
	writeFile(t, skill, "---\nname: bicz\n---\nsomeone else's skill\n")
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe}); err == nil || !strings.Contains(err.Error(), "refusing to clobber") {
		t.Fatalf("a foreign skill was not refused: %v", err)
	}
	un, err := PlanUninstall("claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range un.Changes {
		if change.Path == skill {
			t.Fatal("uninstall planned to remove a file bicz does not own")
		}
	}

	// A symlinked skills directory is refused, not followed.
	home2, exe2 := hookEnv(t)
	real := filepath.Join(home2, "elsewhere")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home2, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home2, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanInstall(Options{Harness: "claude", Executable: exe2}); err == nil {
		t.Fatal("a symlinked skills directory was followed")
	}
}
