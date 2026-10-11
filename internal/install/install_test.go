package install

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testEnv isolates every user-scope root inside a temp HOME so the installer
// never touches a real harness config. BICZ_RUNTIME_DIR keeps the derived
// supervisor socket inside the same temp tree.
func testEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BICZ_RUNTIME_DIR", filepath.Join(home, "runtime"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "")
	t.Setenv("BICZ_CONFIG_DIR", "")
	t.Setenv("BICZ_LEDGER", "")
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	return home
}

func installOnce(t *testing.T) (*Plan, string) {
	t.Helper()
	home := testEnv(t)
	plan, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if plan.Empty() {
		t.Fatal("first install must not be empty")
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return plan, home
}

func TestInstallTwiceIsIdempotent(t *testing.T) {
	plan, _ := installOnce(t)
	second, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err != nil {
		t.Fatalf("second PlanInstall: %v", err)
	}
	if !second.Empty() {
		t.Fatalf("second install must be a no-op, got %d changes", len(second.Changes))
	}
	// No backup may pile up on an idempotent run.
	matches, _ := filepath.Glob(plan.ModulePath + ".bak-*")
	if len(matches) != 0 {
		t.Fatalf("idempotent install left backups: %v", matches)
	}
}

func TestInstallWritesModuleAndConfig(t *testing.T) {
	plan, home := installOnce(t)
	module, err := os.ReadFile(plan.ModulePath)
	if err != nil {
		t.Fatalf("module: %v", err)
	}
	if string(module) != string(ompExtensionModule) {
		t.Fatal("deployed module must be byte-identical to the embedded extension")
	}
	if !strings.HasPrefix(string(module), moduleMarker) {
		t.Fatal("deployed module must carry the bicz ownership marker")
	}
	raw, err := os.ReadFile(plan.ConfigPath)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	if config["bicz_owned"] != true || config["kind"] != configKind {
		t.Fatalf("config must be bicz-owned, got %v", config)
	}
	if config["account_id"] != "alice" {
		t.Fatalf("config account_id = %v, want alice", config["account_id"])
	}
	socket, _ := config["socket"].(string)
	if socket == "" {
		t.Fatal("config must record the derived supervisor socket")
	}
	if !strings.Contains(socket, filepath.Join(home, "runtime")) {
		t.Fatalf("socket %q must live under the runtime dir %q", socket, home)
	}
	if config["unmanaged_mode"] != "notice" || config["no_schedule_mode"] != "notice" {
		t.Fatalf("fail-safe modes must default to notice, got %v", config)
	}
}

func TestDryRunPrintsDiffAndChangesNothing(t *testing.T) {
	home := testEnv(t)
	plan, err := PlanInstall(Options{Harness: "omp", Account: "alice", DryRun: true})
	if err != nil {
		t.Fatalf("PlanInstall dry-run: %v", err)
	}
	// Dry-run is Plan + Print: the plan is exact and nothing is applied.
	buffer := capturePrint(t, func() { plan.Print() })
	if len(plan.Changes) == 0 {
		t.Fatal("dry-run of a fresh install must show changes")
	}
	// Capture the diff and assert it mentions both targets.
	if !strings.Contains(buffer, plan.ModulePath) || !strings.Contains(buffer, plan.ConfigPath) {
		t.Fatalf("dry-run diff must name both targets:\n%s", buffer)
	}
	for _, path := range []string{plan.ModulePath, plan.ConfigPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run must not create %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".omp")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create harness directories")
	}
}

func TestUninstallRemovesOnlyBiczEntries(t *testing.T) {
	_, home := installOnce(t)
	// Unrelated entries: another extension module in the OMP root, and an
	// unrelated file in the bicz config directory.
	extensionsDir := filepath.Join(home, ".omp", "agent", "extensions")
	friendModule := filepath.Join(extensionsDir, "friend.ts")
	if err := os.WriteFile(friendModule, []byte("export default () => {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(home, "xdg", "bicz", "notes.txt")
	if err := os.WriteFile(unrelated, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanUninstall("omp")
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if plan.Empty() {
		t.Fatal("uninstall after install must have work to do")
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	modulePath, _ := modulePath("omp")
	configPath := ConfigPath("omp")
	if _, err := os.Stat(modulePath); !os.IsNotExist(err) {
		t.Fatalf("bicz module must be removed, stat: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("bicz config must be removed, stat: %v", err)
	}
	if _, err := os.Stat(friendModule); err != nil {
		t.Fatalf("unrelated extension module must survive: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated config entry must survive: %v", err)
	}

	// A second uninstall is a clean no-op.
	again, err := PlanUninstall("omp")
	if err != nil {
		t.Fatalf("second PlanUninstall: %v", err)
	}
	if !again.Empty() {
		t.Fatalf("second uninstall must be a no-op, got %d changes", len(again.Changes))
	}
}

func TestUninstallRestoresNewestBackup(t *testing.T) {
	plan, _ := installOnce(t)
	// The operator adapts the bicz-owned module (marker kept, body changed).
	adapted := "// bicz — Oh My Pi harness adapter.\n// operator-edited\n// bicz.adapter/1\n"
	if err := os.WriteFile(plan.ModulePath, []byte(adapted), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reinstall backs the adapted copy up and restores the deployed module.
	second, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err != nil {
		t.Fatalf("reinstall over adapted module: %v", err)
	}
	if len(second.Changes) != 1 || second.Changes[0].Backup == "" {
		t.Fatalf("reinstall must back up the adapted module, got %+v", second.Changes)
	}
	if err := second.Apply(); err != nil {
		t.Fatalf("reinstall Apply: %v", err)
	}

	uninstall, err := PlanUninstall("omp")
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if err := uninstall.Apply(); err != nil {
		t.Fatalf("uninstall Apply: %v", err)
	}
	restored, err := os.ReadFile(plan.ModulePath)
	if err != nil {
		t.Fatalf("restored module: %v", err)
	}
	if string(restored) != adapted {
		t.Fatalf("uninstall must restore the adapted bicz-owned copy, got:\n%s", restored)
	}
	// The stale deployed-version backup is removed with the rest.
	matches, _ := filepath.Glob(plan.ModulePath + ".bak-*")
	if len(matches) != 0 {
		t.Fatalf("stale backups must be cleaned up, got %v", matches)
	}
}

func TestMalformedConfigAbortsWithoutClobbering(t *testing.T) {
	_ = testEnv(t)
	configPath := ConfigPath("omp")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"kind":"other","account":"bob",`
	if err := os.WriteFile(configPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err == nil || !strings.Contains(err.Error(), "refusing to clobber") {
		t.Fatalf("malformed config must abort install, got %v", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil || string(after) != foreign {
		t.Fatalf("malformed config must be untouched, read=%q err=%v", after, readErr)
	}
	// The dry-run must refuse identically and change nothing.
	if _, err := PlanInstall(Options{Harness: "omp", Account: "alice", DryRun: true}); err == nil {
		t.Fatal("dry-run must also refuse a malformed config")
	}
	modulePath, _ := modulePath("omp")
	if _, err := os.Stat(modulePath); !os.IsNotExist(err) {
		t.Fatal("aborted install must not have written the module")
	}
}

func TestForeignModuleRefused(t *testing.T) {
	_ = testEnv(t)
	modulePath, _ := modulePath("omp")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modulePath, []byte("export default 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err == nil || !strings.Contains(err.Error(), "not a bicz-owned") {
		t.Fatalf("foreign module must be refused, got %v", err)
	}
	content, _ := os.ReadFile(modulePath)
	if string(content) != "export default 1;\n" {
		t.Fatal("foreign module must be untouched")
	}
}

func TestSymlinkedTargetRefused(t *testing.T) {
	home := testEnv(t)
	// Plant a symlink in the middle of the module path: HOME/.omp -> elsewhere.
	realOmp := filepath.Join(home, "real-omp")
	if err := os.MkdirAll(realOmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realOmp, filepath.Join(home, ".omp")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked path must be refused, got %v", err)
	}
}

func TestUnsupportedHarnessesReportHonestly(t *testing.T) {
	for _, id := range []string{"opencode-v1", "opencode-v2"} {
		info, ok := DetectHarness(id)
		if !ok {
			t.Fatalf("%s: detection row missing", id)
		}
		if info.Supported {
			t.Fatalf("%s: must report not yet supported", id)
		}
		if !strings.Contains(info.Note, "not yet supported") {
			t.Fatalf("%s: note must say not yet supported, got %q", id, info.Note)
		}
		if _, err := PlanInstall(Options{Harness: id}); err == nil || !strings.Contains(err.Error(), "not yet supported") {
			t.Fatalf("%s: install must refuse honestly, got %v", id, err)
		}
		if _, err := PlanUninstall(id); err == nil || !strings.Contains(err.Error(), "not yet supported") {
			t.Fatalf("%s: uninstall must refuse honestly, got %v", id, err)
		}
	}
	if _, ok := DetectHarness("definitely-not-a-harness"); ok {
		t.Fatal("unknown harness must not have a detection row")
	}
}

func TestDefaultAccountFallsBackToUser(t *testing.T) {
	_ = testEnv(t)
	plan, err := PlanInstall(Options{Harness: "omp", Account: ""})
	if err != nil {
		t.Fatalf("plan without --account: %v", err)
	}
	if plan.Account == "" {
		t.Fatal("default account must resolve to the OS user or 'agent'")
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The config records that resolved identity.
	raw, err := os.ReadFile(plan.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	if config["account_id"] != plan.Account {
		t.Fatalf("config account %v != plan account %q", config["account_id"], plan.Account)
	}
}

// capturePrint runs f and returns everything it printed to stdout.
func capturePrint(t *testing.T, f func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	f()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// TestEmbedMatchesDeployedSource guards against drift: the module the installer
// deploys and the extension source in adapters/omp must stay identical.
func TestEmbedMatchesDeployedSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "adapters", "omp", "extension.ts"))
	if err != nil {
		t.Fatalf("read adapters/omp/extension.ts: %v", err)
	}
	if string(source) != string(ompExtensionModule) {
		t.Fatal("adapters/omp/extension.ts and internal/install/embed/omp_extension.ts drifted; copy the source into the embed")
	}
}

func TestPathEscapeRefused(t *testing.T) {
	_ = testEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "agent"))
	t.Setenv("XDG_CONFIG_HOME", "")
	// Build the raw env value by hand, NOT via filepath.Join (which cleans):
	// a literal ".." component in an env-relocated root is a traversal and must
	// be refused rather than resolved away.
	t.Setenv("BICZ_CONFIG_DIR", home+string(filepath.Separator)+".."+string(filepath.Separator)+"escape")
	_, err = PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err == nil {
		t.Fatal("a config root with a literal .. component must be refused")
	}
}

func TestEnvRelocatedRootAllowed(t *testing.T) {
	_ = testEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// A clean, explicit relocation outside HOME is the operator's choice and
	// must work (traversal-free roots are, and only are, legitimate).
	outside := filepath.Join(home, "..", "relocated-config")
	clean := filepath.Clean(outside)
	t.Setenv("BICZ_CONFIG_DIR", clean)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "agent"))
	plan, err := PlanInstall(Options{Harness: "omp", Account: "alice"})
	if err != nil {
		t.Fatalf("clean relocation must be allowed: %v", err)
	}
	_ = plan
}

func TestDryRunSecondInstallIsNoop(t *testing.T) {
	_ = testEnv(t)
	if true {
		installOnce(t)
	} else {
	}
	plan, err := PlanInstall(Options{Harness: "omp", Account: "alice", DryRun: true})
	if err != nil {
		t.Fatalf("dry-run after install: %v", err)
	}
	if !plan.Empty() {
		t.Fatalf("dry-run of an up-to-date install must show zero changes, got %d", len(plan.Changes))
	}
}
