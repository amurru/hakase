//go:build windows

package util

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestFlockExclusiveWindows verifies the LockFileEx-backed shim: an exclusive
// lock excludes a second handle, the second handle sees ERROR_LOCK_VIOLATION
// when it probes with FAIL_IMMEDIATELY, and the lock is released cleanly.
func TestFlockExclusiveWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := FlockExclusive(f); err != nil {
		t.Fatalf("FlockExclusive: %v", err)
	}

	f2, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	// Try-lock probe: EXCLUSIVE_LOCK|FAIL_IMMEDIATELY must be refused while
	// the first handle holds the lock.
	const exclusiveFailImmediately = lockfileExclusiveLock | 0x00000001
	err = windows.LockFileEx(
		windows.Handle(f2.Fd()),
		exclusiveFailImmediately,
		0, ^uint32(0), ^uint32(0),
		new(windows.Overlapped),
	)
	if err == nil {
		t.Fatal("second exclusive lock succeeded; expected ERROR_LOCK_VIOLATION")
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) || errno != windows.ERROR_LOCK_VIOLATION {
		t.Fatalf("expected ERROR_LOCK_VIOLATION, got %v", err)
	}

	if err := FlockUnlock(f); err != nil {
		t.Fatalf("FlockUnlock: %v", err)
	}
	if err := FlockExclusive(f2); err != nil {
		t.Fatalf("FlockExclusive after release: %v", err)
	}
	if err := FlockUnlock(f2); err != nil {
		t.Fatalf("FlockUnlock(f2): %v", err)
	}
}
