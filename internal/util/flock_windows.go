//go:build windows

package util

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockfileExclusiveLock is LOCKFILE_EXCLUSIVE_LOCK: take an exclusive range
// lock instead of a shared one. FAIL_IMMEDIATELY is deliberately NOT set so
// the call blocks until the lock is available, matching flock(LOCK_EX).
const lockfileExclusiveLock = 0x00000002

// FlockExclusive takes a blocking exclusive advisory lock on the open file.
// It is the LockFileEx equivalent of Unix flock(LOCK_EX): the whole file is
// locked (max 64-bit range) and the call blocks until the lock is granted.
// The file must be open with read+write access so the handle carries the
// access rights LockFileEx requires.
func FlockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		lockfileExclusiveLock,
		0,
		^uint32(0),
		^uint32(0),
		ol,
	)
}

// FlockUnlock releases the advisory lock taken by FlockExclusive.
func FlockUnlock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		^uint32(0),
		^uint32(0),
		ol,
	)
}
