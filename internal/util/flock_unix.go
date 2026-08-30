//go:build unix

package util

import (
	"os"
	"syscall"
)

// FlockExclusive takes a blocking exclusive advisory lock on the open file
// (syscall.LOCK_EX semantics).
func FlockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// FlockUnlock releases the advisory lock on the open file.
func FlockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
