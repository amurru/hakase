package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
