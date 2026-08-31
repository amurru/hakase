//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsHakaseHomeResolution verifies (WIN-006) that the config layer
// resolves the user home via os.UserHomeDir (USERPROFILE on Windows) and that
// HAKASE_HOME still wins over it.
func TestWindowsHakaseHomeResolution(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv("HOME", fakeHome)
	t.Setenv("HAKASE_HOME", "")

	if got := HakaseHome(); got != filepath.Join(fakeHome, ".hakase") {
		t.Errorf("HakaseHome under redirected USERPROFILE: expected %q, got %q",
			filepath.Join(fakeHome, ".hakase"), got)
	}

	// HAKASE_HOME override wins.
	override := t.TempDir()
	t.Setenv("HAKASE_HOME", override)
	if got := HakaseHome(); got != override {
		t.Errorf("HakaseHome with HAKASE_HOME override: expected %q, got %q", override, got)
	}

	// A write into the redirected home lands under the fake USERPROFILE.
	target := filepath.Join(fakeHome, ".hakase", "probe.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write under redirected home: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("probe file missing: %v", err)
	}
}
