package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/permissions"
)

type stubTrust map[string]bool

func (m stubTrust) Trusted(fp string) bool { return m[fp] }

// TestRefreshInterval pins cadence resolution and the minimum clamp.
func TestRefreshInterval(t *testing.T) {
	oldMin := permissionsRefreshMin
	permissionsRefreshMin = 2 * time.Hour
	t.Cleanup(func() { permissionsRefreshMin = oldMin })

	if got := refreshInterval(&config.Config{}); got != 2*time.Hour {
		t.Errorf("default interval = %v, want clamped min 2h", got)
	}
	permissionsRefreshMin = time.Second
	cfg := &config.Config{}
	cfg.Permissions.PollMinutes = 30
	if got := refreshInterval(cfg); got != 30*time.Minute {
		t.Errorf("config interval = %v, want 30m", got)
	}
}

// TestStartPermissionsRefreshReloads pins the ticker swapping in a new
// snapshot (M2): startup allows, mid-run enterprise deny lands.
func TestStartPermissionsRefreshReloads(t *testing.T) {
	permissionsRefreshForced = 20 * time.Millisecond
	t.Cleanup(func() { permissionsRefreshForced = 0 })

	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	ent := filepath.Join(t.TempDir(), "enterprise.json")
	writePermFile(t, ent, `{"version":1,"rules":[]}`)

	cfg := &config.Config{}
	cfg.Permissions.EnterprisePath = ent
	root := t.TempDir()
	if err := initPermissions(cfg, root, stubTrust{}, nil); err != nil {
		t.Fatalf("initPermissions: %v", err)
	}
	t.Cleanup(func() { permissions.InstallLayered(nil) })
	if eff, _, _ := permissions.Lookup("shell", "deploy prod"); eff != permissions.EffectAsk {
		t.Fatalf("initial = %q, want ask", eff)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartPermissionsRefresh(ctx, cfg, root, stubTrust{}, nil)
	defer stop()

	// Tighten the enterprise file mid-run; the ticker must pick it up.
	writePermFile(t, ent, `{"version":1,"rules":[
		{"action":"shell","resource":"deploy *","effect":"deny"}]}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eff, _, _ := permissions.Lookup("shell", "deploy prod"); eff == permissions.EffectDeny {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("refreshed enterprise deny never landed")
}

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
