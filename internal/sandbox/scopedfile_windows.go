//go:build windows

package sandbox

// noFollowFlag is zero on Windows (the OS open has no O_NOFOLLOW): the
// Lstat pre-check in scopedfile.go is the control there, and over-long
// TOCTOU windows are documented at the call sites. Windows CI asserts
// the Lstat leg only.
const noFollowFlag = 0
