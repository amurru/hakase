package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestTaskResolvePermissionsDeny pins permissions-policy deny enforcement
// on the resolved absolute path: edits to denied paths fail, reads of
// allowed paths pass, and no installed policy changes nothing.
func TestTaskResolvePermissionsDeny(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "prod.env")

	cp, err := permissions.Compile(permissions.Policy{Rules: []permissions.Rule{
		{Action: "edit", Resource: filepath.Join(dir, "*.env"), Effect: permissions.EffectDeny},
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })

	ctx := context.Background()
	if _, err := taskResolve(ctx, secret, true, ""); err == nil {
		t.Error("edit of denied path succeeded, want permissions error")
	} else if !strings.Contains(err.Error(), "permissions policy") {
		t.Errorf("error = %q, want permissions policy citation", err)
	}
	// Reads are not covered by the edit rule: unaffected.
	if _, err := taskResolve(ctx, secret, false, ""); err != nil {
		t.Errorf("read of allowed path failed: %v", err)
	}
	// Other edits are unaffected.
	if _, err := taskResolve(ctx, filepath.Join(dir, "main.go"), true, ""); err != nil {
		t.Errorf("edit of allowed path failed: %v", err)
	}

	// Uninstall restores prior behavior.
	permissions.Install(nil)
	if _, err := taskResolve(ctx, secret, true, ""); err != nil {
		t.Errorf("edit after uninstall failed: %v", err)
	}
}
