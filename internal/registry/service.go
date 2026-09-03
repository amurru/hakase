// service.go - project materialization: registering a project clones it into
// a managed checkout, syncing fast-forwards it, deleting removes it. The git
// work reuses the git_clone/git_pull engine (sandbox.OperatorClone/
// OperatorPull) so there is no second git exec surface - the registry only
// records state transitions (cloning/ready/sync_error) around it.
//
// Materialization runs under operator authority (DP-11): these operations are
// issued directly by a human - `hakase projects ...` on a host, later the
// web registry endpoints - never by an agent, so the interactive approval gate
// does not apply. Design: docs/git-tools/project-registry.md (DP-6..DP-11).
package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
)

// Service registers, materializes, syncs, and deletes projects, persisting
// every state transition through the Store.
type Service struct {
	store *Store
	log   interfaces.LogFunc
}

// NewService returns a Service backed by s. log, when non-nil, receives
// progress lines (clone/pull are otherwise silent).
func NewService(s *Store, log interfaces.LogFunc) *Service {
	return &Service{store: s, log: log}
}

// Store exposes the underlying store (listing/lookups for the CLI surface).
func (svc *Service) Store() *Store { return svc.store }

// Resolve returns the project named name or id: an exact id match wins, then
// a case-insensitive name match.
func (svc *Service) Resolve(nameOrID string) (Project, error) {
	if strings.HasPrefix(nameOrID, "proj_") {
		if p, err := svc.store.Get(nameOrID); err == nil {
			return p, nil
		}
	}
	return svc.store.GetByName(nameOrID)
}

// Register creates the entry and materializes its checkout (DP-6). The clone
// is synchronous; a failed clone leaves the entry in sync_error with no
// checkout, which a later Sync retries by re-cloning. Returns the final
// persisted project - even on error, so callers can report the state it was
// left in.
func (svc *Service) Register(ctx context.Context, name, sourceURL, ref string) (Project, error) {
	p, err := svc.store.Create(name, sourceURL, ref)
	if err != nil {
		return Project{}, err
	}
	return svc.materialize(ctx, p)
}

// materialize clones (or re-clones) p's checkout, driving status
// cloning -> ready on success, -> sync_error (checkout cleared) on failure.
func (svc *Service) materialize(ctx context.Context, p Project) (Project, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir := svc.store.CheckoutDir(p)
	// The managed dir is exclusively this project's; remove any partial
	// checkout a crashed run left behind before cloning fresh.
	if err := os.RemoveAll(dir); err != nil {
		return p, fmt.Errorf("projects: clear stale checkout: %w", err)
	}
	p.Checkout = dir
	p.Status = StatusCloning
	if err := svc.store.Update(p); err != nil {
		return p, fmt.Errorf("projects: record cloning: %w", err)
	}
	svc.logf(fmt.Sprintf("projects: cloning %s (%s) into %s", p.Name, p.SourceURL, dir))
	if _, err := sandbox.OperatorClone(ctx, sandbox.GitCloneInput{
		URL:    p.SourceURL,
		Branch: p.Ref,
		Dir:    dir,
	}, svc.log); err != nil {
		_ = os.RemoveAll(dir)
		p.Checkout = ""
		p.Status = StatusSyncError
		if uerr := svc.store.Update(p); uerr != nil {
			return p, fmt.Errorf("projects: clone failed and status could not be recorded: %v (clone: %w)", uerr, err)
		}
		return p, fmt.Errorf("projects: register %q: %w", p.Name, err)
	}
	p.Status = StatusReady
	if err := svc.store.Update(p); err != nil {
		return p, fmt.Errorf("projects: record ready: %w", err)
	}
	svc.logf(fmt.Sprintf("projects: %s ready at %s", p.Name, dir))
	return p, nil
}

// Sync fast-forwards p's checkout from its remote (DP-9, git pull --ff-only).
// When no checkout exists yet - registration previously failed, or the managed
// dir was deleted - Sync re-materializes it. A working tree is never deleted
// here: existing checkouts are only ever pulled, and a diverged or dirty tree
// simply fails into sync_error.
func (svc *Service) Sync(ctx context.Context, id string) (Project, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := svc.store.Get(id)
	if err != nil {
		return Project{}, err
	}
	dir := svc.store.CheckoutDir(p)
	if p.Checkout == "" || !isExistingRepo(dir) {
		return svc.materialize(ctx, p)
	}
	svc.logf(fmt.Sprintf("projects: syncing %s (%s)", p.Name, dir))
	out, err := sandbox.OperatorPull(ctx, sandbox.GitPullInput{RepoDir: dir}, svc.log)
	if err != nil {
		p.Status = StatusSyncError
		if uerr := svc.store.Update(p); uerr != nil {
			return p, fmt.Errorf("projects: pull failed and status could not be recorded: %v (pull: %w)", uerr, err)
		}
		return p, fmt.Errorf("projects: sync %q: %w", p.Name, err)
	}
	if out.NotARepo {
		// The pull engine reports every fatal: as NotARepo (its "not a git
		// repository" heuristic also matches any "fatal:" line). The checkout
		// had a .git dir, so this is a real pull failure - typically a
		// non-fast-forwardable divergence. Never delete the working tree: fail
		// into sync_error and surface git's message.
		p.Status = StatusSyncError
		if uerr := svc.store.Update(p); uerr != nil {
			return p, fmt.Errorf("projects: pull failed and status could not be recorded: %v", uerr)
		}
		msg := stripUntrusted(out.Stderr)
		if msg == "" {
			msg = "git pull failed"
		}
		return p, fmt.Errorf("projects: sync %q: %s", p.Name, msg)
	}
	p.Checkout = dir
	p.Status = StatusReady
	if err := svc.store.Update(p); err != nil {
		return p, fmt.Errorf("projects: record ready: %w", err)
	}
	return p, nil
}

// Delete removes the local checkout and the registry entry (DP-10). The
// remote is never touched. The checkout is removed first so a failed removal
// leaves the entry intact for a retry.
func (svc *Service) Delete(ctx context.Context, id string) (Project, error) {
	p, err := svc.store.Get(id)
	if err != nil {
		return Project{}, err
	}
	dir := svc.store.CheckoutDir(p)
	// Path hygiene: the managed root is derived from the store location and
	// the project id from us, but a hand-edited projects.json must not be able
	// to point deletion at an arbitrary directory.
	if !withinRoot(svc.store.CheckoutRoot(), dir) || filepath.Base(dir) != p.ID {
		return Project{}, fmt.Errorf("projects: refusing to remove non-managed path %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return p, fmt.Errorf("projects: remove checkout: %w", err)
	}
	svc.logf(fmt.Sprintf("projects: removed checkout %s", dir))
	if err := svc.store.Delete(id); err != nil {
		return p, fmt.Errorf("projects: delete entry: %w", err)
	}
	return p, nil
}

func (svc *Service) logf(msg string) {
	if svc.log != nil {
		svc.log(msg)
	}
}

// isExistingRepo reports whether dir holds a git checkout (.git directory or
// worktree pointer file). It decides between pull and (re)clone in Sync.
func isExistingRepo(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && st != nil
}

// withinRoot reports whether path is contained under root.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// stripUntrusted removes <UNTRUSTED_DATA> framing that the git engine wraps
// around output for model consumption; service/CLI callers surface these
// messages to a human and do not need the markers.
func stripUntrusted(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		switch strings.TrimSpace(l) {
		case "<UNTRUSTED_DATA>", "</UNTRUSTED_DATA>":
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
