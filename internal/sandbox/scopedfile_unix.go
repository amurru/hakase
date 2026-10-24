//go:build !windows

package sandbox

import "syscall"

// noFollowFlag refuses symlinks at open time (S24 O_NOFOLLOW leg).
const noFollowFlag = syscall.O_NOFOLLOW
