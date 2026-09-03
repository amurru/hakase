package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(root, "a", "b")
	if got := FindRoot(sub); got != root {
		t.Errorf("FindRoot(%q) = %q, want %q", sub, got, root)
	}
	if got := FindRoot(root); got != root {
		t.Errorf("FindRoot(%q) = %q, want %q", root, got, root)
	}
}

func TestFindRootWorktreeGitFile(t *testing.T) {
	// In a linked worktree .git is a file pointing at the gitdir, not a
	// directory. os.Stat matches both shapes.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindRoot(root); got != root {
		t.Errorf("FindRoot(%q) = %q, want %q", root, got, root)
	}
}

func TestFindRootNoGit(t *testing.T) {
	dir := t.TempDir()
	if got := FindRoot(dir); got != dir {
		t.Errorf("FindRoot(%q) = %q, want fallback to the dir itself", dir, got)
	}
}

func TestSessionRoot(t *testing.T) {
	if got := CurrentRoot(); got != "" {
		t.Fatalf("CurrentRoot before SetCurrentRoot = %q, want empty", got)
	}
	SetCurrentRoot("/tmp/project-a")
	if got := CurrentRoot(); got != "/tmp/project-a" {
		t.Errorf("CurrentRoot = %q, want /tmp/project-a", got)
	}
	SetCurrentRoot("")
	if got := CurrentRoot(); got != "" {
		t.Errorf("CurrentRoot after clear = %q, want empty", got)
	}
}

func TestContextRoot(t *testing.T) {
	SetCurrentRoot("/proc/root")
	defer SetCurrentRoot("")

	// No context value: the process root is the fallback.
	if got := RootFrom(nil); got != "/proc/root" {
		t.Errorf("RootFrom(nil) = %q, want process root", got)
	}
	if got := RootFrom(context.Background()); got != "/proc/root" {
		t.Errorf("RootFrom(background) = %q, want process root", got)
	}

	// A context value overrides the process root and propagates.
	ctx := WithRoot(context.Background(), "/srv/checkouts/proj_x")
	if got := RootFrom(ctx); got != "/srv/checkouts/proj_x" {
		t.Errorf("RootFrom(ctx) = %q, want context root", got)
	}

	// Empty context root falls back to the process root.
	if got := RootFrom(WithRoot(ctx, "")); got != "/proc/root" {
		t.Errorf("RootFrom(empty ctx root) = %q, want process root", got)
	}
}
