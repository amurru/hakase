// scopedfile_r33_test.go - R33 streaming-create hardening tests: the
// ScopedCreateFile leg gains the EvalSymlinks parent re-check the write
// legs already had (a planted symlinked parent refuses instead of
// redirecting the stream), with truncate semantics preserved.
package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestR33CreateRootedParentSymlinkDenied pins the rooted re-check: a
// pre-planted symlink inside the root redirects resolution outside, so
// the streaming create refuses and nothing lands outside. Uses raw
// TempDir (C1 SameFile identity, no EvalSymlinks pre-resolve).
func TestR33CreateRootedParentSymlinkDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(root, "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ScopedCreateFile(root, filepath.Join(root, "linkdir", "pwn.bin"), 0644); err == nil {
		t.Fatal("streaming create through symlinked parent: want refusal")
	} else if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("refusal must name the symlink defense, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(outside, "pwn.bin")); !os.IsNotExist(serr) {
		t.Fatal("escaped stream must not land outside root")
	}
}

// TestR33CreateRootlessParentSymlinkDenied pins the rootless re-check:
// streaming through a symlinked parent directory refuses. Raw TempDir
// (C1 SameFile identity).
func TestR33CreateRootlessParentSymlinkDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ScopedCreateFile("", filepath.Join(link, "pwn.bin"), 0644); err == nil {
		t.Fatal("rootless streaming create through symlinked parent: want refusal")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("refusal must name the symlink defense, got: %v", err)
	}
}

// TestR33CreateTruncatePreserved pins truncate semantics: a streaming
// create over an existing same-name file replaces its content (the
// download overwrite contract), and final-component symlinks refuse.
func TestR33CreateTruncatePreserved(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(dst, []byte("stale-content"), 0644); err != nil {
		t.Fatal(err)
	}
	f, abs, err := ScopedCreateFile("", dst, 0644)
	if err != nil {
		t.Fatalf("overwrite create must succeed: %v", err)
	}
	if _, err := f.Write([]byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Fatalf("streaming create must truncate, got %q", got)
	}
}

// TestR33CreateFinalSymlinkRefused pins the final-component leg for
// streams: a symlink at the destination refuses instead of following.
func TestR33CreateFinalSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	if err := os.WriteFile(target, []byte("t"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ScopedCreateFile("", link, 0644); err == nil {
		t.Fatal("streaming create over a final symlink: want refusal")
	}
	if got, _ := os.ReadFile(target); string(got) != "t" {
		t.Fatal("refused stream must leave the link target untouched")
	}
}
