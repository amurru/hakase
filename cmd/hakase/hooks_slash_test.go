package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/tui"
)

func TestFindSlashCommandHooks(t *testing.T) {
	if cmd := tui.FindSlashCommand("hooks"); cmd == nil || cmd.Name != "hooks" {
		t.Fatalf("FindSlashCommand(hooks) = %v", cmd)
	}
}

func TestHooksTUIReportLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	cfgBody := `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"PreToolUse":[{"hooks":[{"name":"u","command":["/bin/true"]}]}]}}`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hdir := filepath.Join(root, ".hakase")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "hooks.json"), []byte(`{"PreToolUse":[{"hooks":[{"name":"p","command":["/bin/false"]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	lines, err := hooksTUIReport(cfgPath, root)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "1 user hooks") {
		t.Errorf("report missing user layer:\n%s", joined)
	}
	if !strings.Contains(joined, "[UNTRUSTED]") {
		t.Errorf("report must flag the untrusted project hook:\n%s", joined)
	}

	// Unknown root: project section degrades, user layer intact.
	lines, err = hooksTUIReport(cfgPath, "")
	if err != nil {
		t.Fatalf("report (noroot): %v", err)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "no project root") {
		t.Errorf("report must note missing root:\n%s", joined)
	}

	// Bad config fails loudly.
	if _, err := hooksTUIReport(filepath.Join(home, "missing.json"), root); err == nil {
		t.Error("missing config must error")
	}
}

func collectHookLogs(fn func(log func(string))) []string {
	var out []string
	fn(func(s string) { out = append(out, s) })
	return out
}

func writeTUITestConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHooksDispatchUsage(t *testing.T) {
	for _, args := range []string{"bogus", "trust", "untrust a b", "enable", "rm", "on extra", "add", "update", "test", "test a b"} {
		lines := collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, args) })
		if len(lines) == 0 {
			t.Errorf("/hooks %q produced no output", args)
		}
	}
	lines := collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "help") })
	if len(lines) == 0 || !strings.Contains(lines[0], "trust") {
		t.Errorf("/hooks help = %v", lines)
	}
}

func TestHooksDispatchUserCRUD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeTUITestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	// Add mutates disk; live reload fails without agent deps (no SetupRunner
	// in cmd tests) — assert the disk write plus the honest message.
	lines := collectHookLogs(func(log func(string)) {
		runHooksCommandWithLog(log, "add PreToolUse --matcher ^x$ --name tuicrud -- /bin/true")
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "live reload failed") {
		t.Fatalf("add without deps must report reload failure: %v", lines)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tuicrud") {
		t.Errorf("config must contain the added hook: %s", raw)
	}

	// Resolve the fingerprint through a throwaway runner for the rest.
	cfg, err := config.LoadConfig(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := hooks.NewRunner(cfg.Hooks)
	if err != nil {
		t.Fatal(err)
	}
	snaps := r.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	fp := snaps[0].Fingerprint[:16]

	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "disable "+fp) })
	if !strings.Contains(strings.Join(lines, "\n"), "live reload failed") {
		t.Errorf("disable = %v", lines)
	}
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "update "+fp+" --name tuicrud2") })
	if !strings.Contains(strings.Join(lines, "\n"), "live reload failed") {
		t.Errorf("update = %v", lines)
	}
	raw, _ = os.ReadFile(filepath.Join(home, "config.json"))
	if !strings.Contains(string(raw), "tuicrud2") {
		t.Errorf("config must contain the rename: %s", raw)
	}
	// Remove is two-step: preview first, --yes to confirm.
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "rm "+fp) })
	if !strings.Contains(strings.Join(lines, "\n"), "--yes") {
		t.Errorf("rm without --yes must ask for confirmation: %v", lines)
	}
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "rm "+fp+" --yes") })
	if !strings.Contains(strings.Join(lines, "\n"), "live reload failed") {
		t.Errorf("rm --yes = %v", lines)
	}
	raw, _ = os.ReadFile(filepath.Join(home, "config.json"))
	if strings.Contains(string(raw), "tuicrud2") {
		t.Errorf("hook must be gone after rm: %s", raw)
	}
	// Master toggle.
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "off") })
	if !strings.Contains(strings.Join(lines, "\n"), "live reload failed") {
		t.Errorf("off = %v", lines)
	}
}

func TestHooksDispatchTrustPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeTUITestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	// No project root from a temp cwd: trust degrades with a clear error.
	lines := collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "trust abc123") })
	if len(lines) == 0 {
		t.Error("trust with no project scope must explain itself")
	}
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "untrust abc123") })
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no trusted hook") && !strings.Contains(joined, "no trust store") {
		t.Errorf("untrust unknown = %v", lines)
	}
	lines = collectHookLogs(func(log func(string)) { runHooksCommandWithLog(log, "test nosuchhook") })
	if !strings.Contains(strings.Join(lines, "\n"), "no hook matches") {
		t.Errorf("test unknown = %v", lines)
	}
}
