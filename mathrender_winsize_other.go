//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package main

import "errors"

// errNoWinsize reports that TIOCGWINSZ is unavailable on this platform.
var errNoWinsize = errors.New("mathrender: TIOCGWINSZ not supported on this platform")

// termWinsize is a minimal stand-in for platforms without TIOCGWINSZ.
type termWinsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// ioctlWinsize is unsupported on this platform - the renderer falls back to
// default cell sizes, which is fine (cell size only affects kitty image
// sizing, and the Unicode fallback works everywhere).
func ioctlWinsize() (*termWinsize, error) {
	return nil, errNoWinsize
}
