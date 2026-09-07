package sandbox

import (
	"context"
	"path/filepath"
	"testing"
)

// Context-scoped sandbox tests (project-registry DP-7): a project-bound run
// pins the effective sandbox to its checkout via sandbox.WithConfig +
// sandbox.PinnedTo, and the git/file resolvers honor that override over the
// process CurrentSandbox.

func TestPinnedToReplacesRootsAndPreservesPolicy(t *testing.T) {
	root := t.TempDir()
	base := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{t.TempDir()},
		DenyRoots:      []string{"/some/deny"},
		DenyBasenames:  []string{".env"},
		Permissions:    map[string]string{"system_exec": "ask"},
		RiskThreshold:  "medium",
	}
	pinned := PinnedTo(base, root)
	if len(pinned.WorkspaceRoots) != 1 || pinned.WorkspaceRoots[0] != root {
		t.Errorf("pinned workspace roots = %v, want [%s]", pinned.WorkspaceRoots, root)
	}
	if len(pinned.ReadRoots) != 1 || pinned.ReadRoots[0] != root {
		t.Errorf("pinned read roots = %v, want [%s]", pinned.ReadRoots, root)
	}
	// Policy fields survive the pin.
	if pinned.RiskThreshold != "medium" || pinned.Permissions["system_exec"] != "ask" {
		t.Errorf("pinned lost policy fields: %+v", pinned)
	}
	if pinned.DenyRoots[0] != "/some/deny" || pinned.DenyBasenames[0] != ".env" {
		t.Errorf("pinned lost deny rules: %+v", pinned)
	}
	// The base is not mutated.
	if len(base.WorkspaceRoots) != 1 || base.WorkspaceRoots[0] == root {
		t.Errorf("base was mutated by PinnedTo: %+v", base)
	}
	if PinnedTo(nil, root) != nil {
		t.Error("PinnedTo(nil, root) should stay nil (sandbox off stays off)")
	}
}

func TestConfigFromUsesContextOverride(t *testing.T) {
	orig := CurrentSandbox
	t.Cleanup(func() { CurrentSandbox = orig })

	base := &SandboxConfig{Mode: SandboxModePaths}
	CurrentSandbox = base
	if got := ConfigFrom(context.Background()); got != base {
		t.Error("ConfigFrom without override should return CurrentSandbox")
	}
	pinned := PinnedTo(base, t.TempDir())
	if got := ConfigFrom(WithConfig(context.Background(), pinned)); got != pinned {
		t.Error("ConfigFrom should return the context override when set")
	}
	// WithConfig(nil, ...) must not panic and must round-trip.
	if got := ConfigFrom(WithConfig(nil, pinned)); got != pinned {
		t.Error("WithConfig on a nil ctx lost the override")
	}
}

// TestResolveRepoDirPinnedOverride verifies the git default resolution honors
// the run's pinned sandbox over the process CurrentSandbox.
func TestResolveRepoDirPinnedOverride(t *testing.T) {
	origSB := CurrentSandbox
	t.Cleanup(func() { CurrentSandbox = origSB })

	procRoot := t.TempDir()
	checkout := t.TempDir()
	CurrentSandbox = &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{procRoot},
		ReadRoots:      []string{procRoot},
	}

	// Without an override the process workspace root wins.
	dir, err := resolveRepoDir(context.Background(), "", false)
	if err != nil {
		t.Fatalf("resolveRepoDir: %v", err)
	}
	if dir != procRoot {
		t.Errorf("no-override dir = %q, want process workspace %q", dir, procRoot)
	}

	// A project-bound run pins the workspace to the checkout.
	pinned := PinnedTo(CurrentSandbox, checkout)
	ctx := WithConfig(context.Background(), pinned)
	dir, err = resolveRepoDir(ctx, "", false)
	if err != nil {
		t.Fatalf("resolveRepoDir with override: %v", err)
	}
	if dir != checkout {
		t.Errorf("pinned dir = %q, want checkout %q", dir, checkout)
	}
}

// TestTaskResolvePinnedOverride verifies file-op resolution confines writes to
// the run's pinned checkout even when the process workspace is elsewhere.
func TestTaskResolvePinnedOverride(t *testing.T) {
	origSB := CurrentSandbox
	t.Cleanup(func() { CurrentSandbox = origSB })

	procRoot := t.TempDir()
	checkout := t.TempDir()
	CurrentSandbox = &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{procRoot},
		ReadRoots:      []string{procRoot},
	}

	pinned := PinnedTo(CurrentSandbox, checkout)
	ctx := WithConfig(context.Background(), pinned)

	inRepo, err := taskResolve(ctx, filepath.Join(checkout, "src", "main.go"), true, "")
	if err != nil {
		t.Fatalf("write inside pinned checkout rejected: %v", err)
	}
	if !filepath.HasPrefix(inRepo, checkout) {
		t.Errorf("resolved %q not under checkout %q", inRepo, checkout)
	}

	if _, err := taskResolve(ctx, filepath.Join(procRoot, "outside.txt"), true, ""); err == nil {
		t.Error("write into the process workspace accepted under a pinned checkout sandbox")
	}
}
