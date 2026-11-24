//go:build !windows

package sandbox

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGitWorkdirConfigAuditNeverBlocksOnFifo pins the bounded-read rule:
// a FIFO at any audited path must never block the audit. A FIFO config
// (or an oversize/unauditable pointer) fails closed to approval without
// opening the file; a FIFO .git entry is skipped without opening while
// the walk continues.
func TestGitWorkdirConfigAuditNeverBlocksOnFifo(t *testing.T) {
	// FIFO config: fail closed to approval, without blocking.
	repo := t.TempDir()
	dotGit := mustMkdir(t, filepath.Join(repo, ".git"))
	if err := syscall.Mkfifo(filepath.Join(dotGit, "config"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- gitWorkdirConfigAskReason(repo) }()
	select {
	case reason := <-done:
		if !strings.Contains(reason, "requires user approval") {
			t.Fatalf("FIFO config should ask, got %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("audit blocked on FIFO config")
	}

	// FIFO .git entry: not a repository config, skipped without opening.
	repo2 := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(repo2, ".git"), 0o644); err != nil {
		t.Fatal(err)
	}
	done2 := make(chan string, 1)
	go func() { done2 <- gitWorkdirConfigAskReason(repo2) }()
	select {
	case reason := <-done2:
		if reason != "" {
			t.Fatalf("FIFO .git entry should stay silent, got %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("audit blocked on FIFO .git entry")
	}

	// Oversize .git pointer: too large to audit, fails closed.
	repo3 := t.TempDir()
	big := make([]byte, 64*1024+1)
	for i := range big {
		big[i] = 'x'
	}
	mustWriteFile(t, filepath.Join(repo3, ".git"), string(big))
	if reason := gitWorkdirConfigAskReason(repo3); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("oversize .git pointer should ask, got %q", reason)
	}
}

// TestGitWorkdirConfigBadCommondirAsks pins the worktree commondir cap:
// a FIFO commondir must neither block the audit nor fall through to an
// unverified fallback path - it fails closed to approval.
func TestGitWorkdirConfigBadCommondirAsks(t *testing.T) {
	tmp := t.TempDir()
	mainGit := mustMkdir(t, filepath.Join(tmp, "main", ".git"))
	mustWriteFile(t, filepath.Join(mainGit, "config"), "[core]\n\trepositoryformatversion = 0\n")
	wtGitDir := mustMkdir(t, filepath.Join(mainGit, "worktrees", "wt1"))
	if err := syscall.Mkfifo(filepath.Join(wtGitDir, "commondir"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := mustMkdir(t, filepath.Join(tmp, "wt1"))
	mustWriteFile(t, filepath.Join(wt, ".git"), "gitdir: "+wtGitDir+"\n")
	done := make(chan string, 1)
	go func() { done <- gitWorkdirConfigAskReason(wt) }()
	select {
	case reason := <-done:
		if !strings.Contains(reason, "requires user approval") {
			t.Fatalf("FIFO commondir should ask, got %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("audit blocked on FIFO commondir")
	}
}
