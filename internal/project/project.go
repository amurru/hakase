// Package project resolves and tracks the session's project identity: the
// repository root (or fallback directory) a session is anchored to. The
// project is derived locally by walking up for .git - it is an identity, not
// a stored entity. It stays distinct from the sandbox "workspace", which
// remains the confinement boundary (what the agent may touch); the project
// root is what git tools default to and what the repo-awareness snapshot in
// the system prompt anchors to. Context-file and skill discovery already walk
// to the same git root.
package project

import (
	"os"
	"path/filepath"
	"sync"
)

// FindRoot walks from dir upward and returns the first directory containing a
// .git entry. The entry is a directory in a normal clone and a file in a
// linked worktree, so it is checked via os.Stat either way. When no .git is
// found before reaching the filesystem root, the absolute dir itself is
// returned (documented fallback; no silent guess).
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs
		}
		d = parent
	}
}

// Session root state. Set once per session in SetupRunner so every agent,
// tool, and prompt block in that session shares one project identity.
var (
	mu   sync.RWMutex
	root string
)

// SetCurrentRoot records the session project root. An empty root clears the
// identity (no project).
func SetCurrentRoot(r string) {
	mu.Lock()
	defer mu.Unlock()
	root = r
}

// CurrentRoot returns the session project root, or "" when none is set.
func CurrentRoot() string {
	mu.RLock()
	defer mu.RUnlock()
	return root
}
