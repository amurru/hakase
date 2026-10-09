package sandbox

import (
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestAuditSystemCommandPathsPermissionsDeny pins permissions-policy deny
// enforcement in the exec path audit: a denied command fails closed even
// when every token passes the sandbox audit on its own.
func TestAuditSystemCommandPathsPermissionsDeny(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
	}

	// Baseline: bare words pass the audit with no policy installed.
	permissions.Install(nil)
	if err := AuditSystemCommandPaths(sb, "git push origin", nil, ""); err != nil {
		t.Fatalf("baseline audit failed: %v", err)
	}

	cp, err := permissions.Compile(permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "git push *", Effect: permissions.EffectDeny},
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })

	err = AuditSystemCommandPaths(sb, "git push origin", nil, "")
	if err == nil {
		t.Fatal("denied command passed the audit, want error")
	}
	if !strings.Contains(err.Error(), "permissions policy") {
		t.Errorf("error = %q, want permissions policy citation", err)
	}
	// Untouched commands still pass.
	if err := AuditSystemCommandPaths(sb, "git status", nil, ""); err != nil {
		t.Errorf("allowed command failed the audit: %v", err)
	}
}
