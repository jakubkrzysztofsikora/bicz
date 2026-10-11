package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeGenerationOverride(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		fail  bool
	}{{"1", 1, false}, {"2", 2, false}, {"3", 0, true}, {"x", 0, true}} {
		got, err := OpenCodeGeneration(tc.value)
		if (err != nil) != tc.fail || (!tc.fail && got != tc.want) {
			t.Fatalf("OpenCodeGeneration(%q) = %d, %v", tc.value, got, err)
		}
	}
}

func TestOpenCodeGenerationAutoDetectsMajor(t *testing.T) {
	bin := t.TempDir()
	exe := filepath.Join(bin, "opencode")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 2.0.26\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got, err := OpenCodeGeneration("")
	if err != nil || got != 2 {
		t.Fatalf("OpenCodeGeneration auto-detect = %d, %v", got, err)
	}
}

func TestOpenCodeConfigDirOverride(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "custom-opencode")
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_CONFIG_DIR", root)
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	plan, err := PlanOpenCodeInstall(Options{Account: "tester"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ModulePath != filepath.Join(root, "plugins", "bicz.ts") || plan.ConfigPath != filepath.Join(root, "bicz", "opencode.json") {
		t.Fatalf("config-dir override paths: plugin=%s config=%s", plan.ModulePath, plan.ConfigPath)
	}
}

// The native /bicz handlers run `bicz command` from the config's bicz_path, so
// an explicit executable must be recorded verbatim and a non-bicz one refused.
func TestOpenCodeConfigRecordsSlashExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	exe := filepath.Join(home, "bin", "bicz")
	plan, err := PlanOpenCodeInstall(Options{Account: "tester", Executable: exe}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.ConfigBody), `"bicz_path": "`+exe+`"`) {
		t.Fatalf("config does not record bicz_path:\n%s", plan.ConfigBody)
	}
	if _, err := PlanOpenCodeInstall(Options{Account: "tester", Executable: filepath.Join(home, "bin", "sh")}, 2); err == nil {
		t.Fatal("a non-bicz executable must be refused")
	}
}

func TestPlanOpenCodeInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	t.Setenv("BICZ_LEDGER", filepath.Join(home, "ledger.sqlite"))
	plan, err := PlanOpenCodeInstall(Options{Harness: "opencode", Account: "tester"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("expected plugin+config changes, got %d", len(plan.Changes))
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "bicz.ts")
	b, err := os.ReadFile(plugin)
	if err != nil || !strings.Contains(string(b), "opencode1:auth:") {
		t.Fatalf("V1 plugin missing: %v", err)
	}
	again, err := PlanOpenCodeInstall(Options{Harness: "opencode", Account: "tester"}, 1)
	if err != nil || !again.Empty() {
		t.Fatalf("install not idempotent: plan=%v err=%v", again, err)
	}
	uninstall, err := PlanOpenCodeUninstall()
	if err != nil {
		t.Fatal(err)
	}
	if len(uninstall.Changes) != 2 {
		t.Fatalf("expected owned plugin+config removal, got %d", len(uninstall.Changes))
	}
	if err := uninstall.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Fatalf("plugin remains: %v", err)
	}
}

func TestOpenCodePlanDoesNotCreateBackupBeforeApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	first, err := PlanOpenCodeInstall(Options{Account: "first"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Apply(); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOpenCodeInstall(Options{Account: "second"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var backup string
	for _, change := range plan.Changes {
		if change.Prior != nil {
			backup = change.Backup
			break
		}
	}
	if backup == "" {
		t.Fatal("expected a deferred backup for the replaced config")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("planning created backup before apply: %v", err)
	}
}

func TestPlanOpenCodeRefusesForeignPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "bicz.ts")
	if err := os.MkdirAll(filepath.Dir(plugin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOpenCodeInstall(Options{Account: "tester"}, 2); err == nil {
		t.Fatal("expected refusal for foreign plugin")
	}
}

// A reinstall (new plugin, new identity) must keep what the operator set by hand
// in the adapter config: without schedule_path every tool call is unmetered.
func TestOpenCodeReinstallKeepsOperatorKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("BICZ_CONFIG_DIR", filepath.Join(home, ".config", "bicz"))
	first, err := PlanOpenCodeInstall(Options{Account: "first"}, 1)
	mustApply(t, first, err)
	operatorEditConfig(t, first.ConfigPath)

	second, err := PlanOpenCodeInstall(Options{Account: "second"}, 2)
	mustApply(t, second, err)
	assertOperatorKeysKept(t, first.ConfigPath, "second")

	again, err := PlanOpenCodeInstall(Options{Account: "second"}, 2)
	if err != nil || !again.Empty() {
		t.Fatalf("operator-edited config must not be rewritten when identity and plugin are unchanged: plan=%v err=%v", again, err)
	}
}

// OMP builds its adapter config the same way, so it has the same contract.
func TestOMPReinstallKeepsOperatorKeys(t *testing.T) {
	testEnv(t)
	first, err := PlanInstall(Options{Harness: "omp", Account: "first"})
	mustApply(t, first, err)
	operatorEditConfig(t, first.ConfigPath)

	second, err := PlanInstall(Options{Harness: "omp", Account: "second"})
	mustApply(t, second, err)
	assertOperatorKeysKept(t, first.ConfigPath, "second")
}

func operatorEditConfig(t *testing.T, path string) {
	t.Helper()
	cfg := decode(t, mustRead(t, path))
	cfg["schedule_path"] = "/somewhere/schedule.json"
	cfg["policy_version"] = "7"
	cfg["unmanaged_mode"] = "block"
	cfg["no_schedule_mode"] = "block"
	edited, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(edited, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertOperatorKeysKept(t *testing.T, path, account string) {
	t.Helper()
	cfg := decode(t, mustRead(t, path))
	if cfg["schedule_path"] != "/somewhere/schedule.json" || cfg["policy_version"] != "7" ||
		cfg["unmanaged_mode"] != "block" || cfg["no_schedule_mode"] != "block" {
		t.Fatalf("reinstall dropped operator-set keys: %v", cfg)
	}
	if cfg["account_id"] != account {
		t.Fatalf("identity must be refreshed to %q: %v", account, cfg)
	}
}
