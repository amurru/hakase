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
	"context"
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
// A context-scoped root (WithRoot) overrides the process root for the
// duration of one run - the mechanism registered remote projects use so a
// server hosting several checkouts resolves each session against its own
// project (docs/git-tools/project-registry.md DP-7).
var (
	mu   sync.RWMutex
	root string
)

// ctxKey is the context key for a per-run project root.
type ctxKey struct{}

// snapshotKey carries the run's pre-rendered GIT WORKSPACE snapshot
// (prompt-cache PC-005): agentrun renders it once per turn and the history
// builder splices it as a frozen turn block. A plain string (not a struct)
// keeps the plumbing visible in one place with the root above.
type snapshotKey struct{}

// WithSnapshot returns a context carrying the turn's project snapshot
// ("" = session unbound, no block). Read once per turn by the history
// builder's frozen-turn render; later model calls of the turn reuse the
// frozen payload, so the snapshot is fresh per turn and byte-identical
// within it.
func WithSnapshot(ctx context.Context, snapshot string) context.Context {
	return context.WithValue(ctx, snapshotKey{}, snapshot)
}

// SnapshotFrom returns the turn's project snapshot ("" when none).
// ctx may be nil. Best-effort by contract: a context that cannot be
// probed (half-initialized test mocks) yields "" instead of panicking,
// so a snapshot lookup can never fail a model call.
func SnapshotFrom(ctx context.Context) (snap string) {
	if ctx == nil {
		return ""
	}
	defer func() { _ = recover() }()
	if v, ok := ctx.Value(snapshotKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRoot returns a context whose project root is root, overriding the
// process-wide root for everything downstream.
func WithRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, ctxKey{}, root)
}

// RootFrom returns the context-scoped project root when set, falling back
// to the process root. ctx may be nil.
func RootFrom(ctx context.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
			return v
		}
	}
	return CurrentRoot()
}

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
