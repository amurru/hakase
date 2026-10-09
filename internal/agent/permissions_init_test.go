package agent

import (
	"os"
	"path/filepath"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/permissions"
)

type stubTrust map[string]bool

func (m stubTrust) Trusted(fp string) bool { return m[fp] }

// TestInitPermissionsDisabled pins explicit disable skipping everything.
func TestInitPermissionsDisabled(t *testing.T) {
	f := false
	cfg := &config.Config{Permissions: config.PermissionsConfig{Enabled: &f}}
	if err := initPermissions(cfg, t.TempDir(), stubTrust{}, nil); err != nil {
		t.Fatalf("initPermissions: %v", err)
	}
	if _, _, ok := permissions.Lookup("shell", "anything"); ok {
		t.Error("Lookup ok with permissions disabled, want false")
	}
}

// TestInitPermissionsLoadsUserLayer pins end-to-end file load + install.
func TestInitPermissionsLoadsUserLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writePermFile(t, filepath.Join(home, "permissions.json"), `{"version":1,"rules":[
		{"action":"shell","resource":"git push *","effect":"deny"}]}`)

	cfg := &config.Config{}
	if err := initPermissions(cfg, t.TempDir(), stubTrust{}, nil); err != nil {
		t.Fatalf("initPermissions: %v", err)
	}
	t.Cleanup(func() { permissions.InstallLayered(nil) })
	eff, _, ok := permissions.Lookup("shell", "git push origin")
	if !ok || eff != permissions.EffectDeny {
		t.Errorf("Lookup = %q/%v, want deny/true", eff, ok)
	}
}

// TestInitPermissionsCorruptUserFailsLoud pins startup failure on a
// corrupt user file (landlock precedent).
func TestInitPermissionsCorruptUserFailsLoud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writePermFile(t, filepath.Join(home, "permissions.json"), `{"version":1,"rulez":[]}`)

	cfg := &config.Config{}
	if err := initPermissions(cfg, t.TempDir(), stubTrust{}, nil); err == nil {
		t.Error("initPermissions with corrupt user file: nil error, want error")
	}
}

func writePermFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
