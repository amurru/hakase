package sandbox

import (
	"path/filepath"
	"testing"
)

// TestHookConfigWriteDenied pins HK-105: the agent may read project hook
// files (transparency) but must never overwrite them via the file tools
// (CVE-2026-25725 persistence control).
func TestHookConfigWriteDenied(t *testing.T) {
	tmp := t.TempDir()
	ws := mustMkdir(t, filepath.Join(tmp, "ws"))
	hookDir := mustMkdir(t, filepath.Join(ws, ".hakase"))
	hookFile := filepath.Join(hookDir, "hooks.json")
	mustWriteFile(t, hookFile, `{}`)
	plainFile := filepath.Join(ws, "hooks.json")
	mustWriteFile(t, plainFile, `{}`)

	sb := LoadSandboxConfig(&SandboxJSON{
		Mode:           "paths",
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws},
	})

	if _, err := sb.ResolveScopedPath(hookFile, true); err == nil {
		t.Errorf("write to %s must be denied", hookFile)
	}
	if _, err := sb.ResolveScopedPath(hookFile, false); err != nil {
		t.Errorf("read of %s must stay allowed: %v", hookFile, err)
	}
	// A hooks.json outside a .hakase dir is ordinary user data.
	if _, err := sb.ResolveScopedPath(plainFile, true); err != nil {
		t.Errorf("write to non-.hakase hooks.json must stay allowed: %v", err)
	}
	if sb.DeniedPath(hookFile) {
		t.Errorf("DeniedPath (listing filter) must not hide %s; enforcement lives in ResolveScopedPath", hookFile)
	}
}

// TestHookTrustStoreImplicitlyDenied extends the sensitive-files control:
// the trust store joins channels.json et al. in DenyRoots.
func TestHookTrustStoreImplicitlyDenied(t *testing.T) {
	tmp := t.TempDir()
	home := mustMkdir(t, filepath.Join(tmp, "home"))
	ws := mustMkdir(t, filepath.Join(tmp, "ws"))
	t.Setenv("HAKASE_HOME", home)
	t.Chdir(ws)

	sb := LoadSandboxConfig(&SandboxJSON{
		Mode:           "paths",
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws, home},
	})
	want := filepath.Join(home, "hooks-trust.json")
	found := false
	for _, got := range sb.DenyRoots {
		if got == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DenyRoots missing %q; got %v", want, sb.DenyRoots)
	}
}
