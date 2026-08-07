//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import "golang.org/x/sys/unix"

// termWinsize mirrors unix.Winsize so platform files stay small.
type termWinsize = unix.Winsize

// ioctlWinsize queries TIOCGWINSZ on stdout, returning the window size
// including pixel geometry when the terminal reports it.
func ioctlWinsize() (*termWinsize, error) {
	return unix.IoctlGetWinsize(1, unix.TIOCGWINSZ)
}
