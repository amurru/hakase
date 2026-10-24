// scopedfile_test.go - S24 scoped exclusive file creation tests.
package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestS24ScopedExclusiveCreate pins O_EXCL create-only: the first create
// succeeds, a second create with overwrite=false refuses, and the
// original bytes survive.
func TestS24ScopedExclusiveCreate(t *testing.T) {
	root := t.TempDir()
	if _, err := ScopedWriteFileInRoot(root, "a.txt", []byte("first"), 0644, false); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := ScopedWriteFileInRoot(root, "a.txt", []byte("second"), 0644, false); err == nil {
		t.Fatal("second create with overwrite=false: want refusal")
	} else if !strings.Contains(err.Error(), "scoped write refused") {
		t.Fatalf("refusal must name the scoped write, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(got) != "first" {
		t.Fatalf("original bytes must survive, got %q err %v", got, err)
	}
}

// TestS24NoFollowSymlink pins O_NOFOLLOW: a symlink at the final path
// refuses instead of being followed, on every platform (Lstat leg).
func TestS24NoFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ScopedWriteFileInRoot(root, "link.txt", []byte("pwn"), 0644, true); err == nil {
		t.Fatal("symlink final path with overwrite=true: want refusal")
	}
	if _, err := ScopedWriteFile(link, []byte("pwn"), 0644, true); err == nil {
		t.Fatal("ScopedWriteFile on symlink: want refusal")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "x" {
		t.Fatal("symlink target must be untouched")
	}
}

// TestS24OverwriteNeedsFlag pins the overwrite flag: create-only refuses
// an existing file, overwrite=true replaces it.
func TestS24OverwriteNeedsFlag(t *testing.T) {
	root := t.TempDir()
	if _, err := ScopedWriteFileInRoot(root, "b.txt", []byte("one"), 0644, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ScopedWriteFileInRoot(root, "b.txt", []byte("two"), 0644, true); err != nil {
		t.Fatalf("overwrite=true on existing file: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "b.txt"))
	if string(got) != "two" {
		t.Fatalf("overwrite must replace bytes, got %q", got)
	}
}

// TestS24EscapeDenied pins scoped confinement: ".." escapes and absolute
// escapes outside root refuse, nested creates inside root succeed.
func TestS24EscapeDenied(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape.txt", "../../escape.txt", "sub/../../escape.txt"} {
		if _, err := ScopedWriteFileInRoot(root, name, []byte("x"), 0644, false); err == nil {
			t.Fatalf("escape %q: want refusal", name)
		}
	}
	if _, err := ScopedWriteFileInRoot(root, "sub/dir/c.txt", []byte("ok"), 0644, false); err != nil {
		t.Fatalf("nested create inside root: %v", err)
	}
}

// TestS24ParentSymlinkEscapeDenied pins the parent-swap defense: a
// symlinked parent directory inside root redirects EvalSymlinks outside,
// so the write refuses instead of escaping.
func TestS24ParentSymlinkEscapeDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(root, "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ScopedWriteFileInRoot(root, "linkdir/pwn.txt", []byte("x"), 0644, false); err == nil {
		t.Fatal("write through symlinked parent: want refusal")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); !os.IsNotExist(err) {
		t.Fatal("escaped write must not land outside root")
	}
}

// TestS24RootlessParentSymlinkDenied pins M1 for the no-root choke: a
// symlinked parent refuses even without a confinement root (fail-closed,
// EvalSymlinks equality). Temp roots resolve first so darwin /tmp (/tmp
// -> /private/tmp) and /var symlinks never false-positive.
func TestS24RootlessParentSymlinkDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		base = t.TempDir()
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ScopedWriteFile(filepath.Join(linkDir, "pwn.txt"), []byte("x"), 0644, false); err == nil {
		t.Fatal("rootless write through symlinked parent: want refusal")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); !os.IsNotExist(err) {
		t.Fatal("escaped rootless write must not land outside")
	}
}

// TestS24RootlessEvalSymlinksFailClosed pins M2: an unresolvable parent
// (dangling symlink component) refuses instead of proceeding.
func TestS24RootlessEvalSymlinksFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		base = t.TempDir()
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "no-such-target-xyz"), dangling); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ScopedWriteFile(filepath.Join(dangling, "x.txt"), []byte("x"), 0644, false); err == nil {
		t.Fatal("dangling parent: want refusal (fail-closed)")
	}
}
