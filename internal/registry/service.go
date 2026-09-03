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
	"sync"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
)

// projectStatusFetchTimeout bounds the network fetch behind a status read so a
// hung remote cannot stall the Projects page or chat header chip.
const projectStatusFetchTimeout = 30 * time.Second

// Current is the process-wide registry service, set at boot by the main
// package (cmd/hakase/web.go). Web handlers and the chat run-binder consult
// it; nil means the registry was not loaded and project features are
// unavailable. Follows the mcp.MCPManager / sandbox.CurrentSandbox precedent
// of boot-configured package globals.
var Current *Service

// Service registers, materializes, syncs, and deletes projects, persisting
// every state transition through the Store.
type Service struct {
	store *Store
	log   interfaces.LogFunc

	slotMu sync.Mutex
	busy   map[string]bool // project IDs with a materialize/sync in flight
}

// NewService returns a Service backed by s. log, when non-nil, receives
// progress lines (clone/pull are otherwise silent).
func NewService(s *Store, log interfaces.LogFunc) *Service {
	return &Service{store: s, log: log, busy: map[string]bool{}}
}

// operatorExecCtx returns a context whose effective sandbox is off for
// operator-issued registry git work. Materialization is a direct human command
// (DP-11) and must not be confined by the agent sandbox the host runs under: a
// web server started inside a git repository defaults to a paths sandbox
// rooted at that repo (LoadSandboxConfig on an empty config), while managed
// checkouts live under the hakase home - outside it - so confined clones/pulls
// would fail instantly with "outside approved workspace". Host management
// writes are the operator's own, like session files and cron state.
func operatorExecCtx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	sb := sandbox.ConfigFrom(ctx)
	if sb == nil || sb.Mode == sandbox.SandboxModeOff {
		return ctx
	}
	return sandbox.WithConfig(ctx, &sandbox.SandboxConfig{Mode: sandbox.SandboxModeOff})
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
	ctx = operatorExecCtx(ctx)
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

// syncSlot reserves an exclusive materialize/sync slot for id. A second caller
// (e.g. a page refresh racing an in-flight sync) is refused with ErrBusy
// rather than queued, so two pulls/clones never touch the same checkout.
func (svc *Service) syncSlot(id string) (func(), error) {
	svc.slotMu.Lock()
	defer svc.slotMu.Unlock()
	if svc.busy[id] {
		return nil, fmt.Errorf("%w: %s", ErrBusy, id)
	}
	svc.busy[id] = true
	return func() {
		svc.slotMu.Lock()
		delete(svc.busy, id)
		svc.slotMu.Unlock()
	}, nil
}

// ProjectState is the live repo state of one ready project's checkout: current
// branch/upstream, ahead/behind vs the upstream, and the workspace-snapshot
// dirty counts (project-ui.md).
type ProjectState struct {
	Branch    string
	Upstream  string
	Ahead     int
	Behind    int
	Staged    int
	Modified  int
	Untracked int
	Conflicts int
	// FetchError is set when the optional status fetch failed; counts then
	// reflect the last-known remote-tracking refs.
	FetchError string
}

// State reports the live checkout state of a ready project (branch/upstream,
// ahead/behind, dirty counts). When fetch is true, a bounded best-effort
// fetch runs first so "behind" reflects the remote; a fetch failure is
// reported in FetchError, never fatal. Not-ready or checkout-less projects
// return an error - the UI treats them as "run Sync first".
func (svc *Service) State(ctx context.Context, id string, fetch bool) (ProjectState, error) {
	var st ProjectState
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = operatorExecCtx(ctx)
	p, err := svc.store.Get(id)
	if err != nil {
		return st, err
	}
	if p.Status != StatusReady || p.Checkout == "" {
		return st, fmt.Errorf("projects: project %q is not ready (status %q); run sync first", p.Name, p.Status)
	}
	dir := svc.store.CheckoutDir(p)
	if !isExistingRepo(dir) {
		return st, fmt.Errorf("projects: checkout for %q is missing; run sync to re-clone", p.Name)
	}
	if fetch {
		fctx, cancel := context.WithTimeout(ctx, projectStatusFetchTimeout)
		defer cancel()
		if _, ferr := sandbox.OperatorFetch(fctx, sandbox.GitFetchInput{RepoDir: dir}, svc.log); ferr != nil {
			st.FetchError = ferr.Error()
		}
	}
	rs, err := sandbox.OperatorRepoState(ctx, dir, svc.log)
	if err != nil {
		return st, err
	}
	// Translate the porcelain headers into display-ready labels, mirroring
	// BuildGitWorkspaceBlock's detached/unborn handling.
	switch {
	case strings.HasSuffix(rs.Branch, " (no branch)"):
		st.Branch = "(detached HEAD)"
	case strings.HasPrefix(rs.Branch, "No commits yet on "):
		st.Branch = strings.TrimPrefix(rs.Branch, "No commits yet on ")
	default:
		st.Branch = rs.Branch
	}
	st.Upstream = rs.Upstream
	st.Ahead = rs.Ahead
	st.Behind = rs.Behind
	st.Staged = rs.Staged
	st.Modified = rs.Modified
	st.Untracked = rs.Untracked
	st.Conflicts = rs.Conflicts
	return st, nil
}

// Sync fast-forwards p's checkout from its remote (DP-9, git pull --ff-only).
// When no checkout exists yet - registration previously failed, or the managed
// dir was deleted - Sync re-materializes it. A working tree is never deleted
// here: existing checkouts are only ever pulled, and a diverged or dirty tree
// simply fails into sync_error. Sync is exclusive per project (ErrBusy for a
// concurrent request) and refuses to pull while the checkout holds uncommitted
// tracked changes (ErrWorkingTreeDirty) so a sync never interleaves with
// in-progress agent work.
func (svc *Service) Sync(ctx context.Context, id string) (Project, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = operatorExecCtx(ctx)
	p, err := svc.store.Get(id)
	if err != nil {
		return Project{}, err
	}
	release, err := svc.syncSlot(id)
	if err != nil {
		return p, err
	}
	defer release()

	dir := svc.store.CheckoutDir(p)
	if p.Checkout == "" || !isExistingRepo(dir) {
		return svc.materialize(ctx, p)
	}
	// project-ui.md: refuse to fast-forward under tracked in-progress work.
	// Untracked files do not block (git leaves them alone on --ff-only); a
	// read failure falls through and lets the pull surface the real error.
	if st, serr := sandbox.OperatorRepoState(ctx, dir, svc.log); serr == nil &&
		st.Staged+st.Modified+st.Conflicts > 0 {
		return p, fmt.Errorf("%w for project %q (staged %d, modified %d, conflicts %d)",
			ErrWorkingTreeDirty, p.Name, st.Staged, st.Modified, st.Conflicts)
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
	release, err := svc.syncSlot(id)
	if err != nil {
		return p, err
	}
	defer release()
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
