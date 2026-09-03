package registry

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
)

// Service tests run git against a local bare remote (sandbox-off, per D9):
// registering materializes a clone, syncing after an external push
// fast-forwards it, deleting removes checkout + entry but never the remote.

// stubOperatorGate installs the sandbox gate hooks the engine needs and makes
// every command an approval ask. Operator-authorized exec (used by the
// Service) must bypass the approval gate, so the collected request list must
// stay empty across a register/sync.
func stubOperatorGate(t *testing.T) *[]interfaces.ApprovalRequest {
	t.Helper()
	origGate, origApprove := sandbox.EvaluateCommandFunc, sandbox.ApproveFunc
	origAudit, origExpiry := sandbox.AuditCommandFunc, sandbox.ApprovalExpiryFunc
	seen := &[]interfaces.ApprovalRequest{}
	sandbox.EvaluateCommandFunc = func(sb *sandbox.SandboxConfig, command string, args []string) sandbox.GateDecision {
		return sandbox.GateDecision{Action: sandbox.ActionAsk, Risk: sandbox.RiskMedium, Reason: "test: gate asks"}
	}
	sandbox.ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) {
		*seen = append(*seen, req)
		return true, nil
	}
	sandbox.AuditCommandFunc = func(entry sandbox.CommandAuditEntry) {}
	sandbox.ApprovalExpiryFunc = func() time.Duration { return 60 * time.Second }
	t.Cleanup(func() {
		sandbox.EvaluateCommandFunc, sandbox.ApproveFunc = origGate, origApprove
		sandbox.AuditCommandFunc, sandbox.ApprovalExpiryFunc = origAudit, origExpiry
	})
	return seen
}

// gitBin returns the git executable, skipping the test when git is absent.
func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not installed: %v", err)
	}
	return p
}

// gitTestEnv isolates git from global/system config and terminal prompts.
func gitTestEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

// gitCmd runs a system git command in dir with the test env.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	g := gitBin(t)
	cmd := &exec.Cmd{
		Path: g,
		Args: append([]string{g}, args...),
		Dir:  dir,
		Env:  gitTestEnv(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo creates a git repository at dir with a local identity and one
// "initial" commit.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Hakase Test")
	gitCmd(t, dir, "config", "user.email", "hakase@test.local")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "initial")
}

// newSeedRemote creates a source repo and returns its bare remote path.
func newSeedRemote(t *testing.T) string {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	initRepo(t, seed)
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, t.TempDir(), "clone", "--bare", seed, bare)
	return bare
}

// TestServiceRegisterSyncDeleteLifecycle drives the full DP-6/DP-9/DP-10 loop.
func TestServiceRegisterSyncDeleteLifecycle(t *testing.T) {
	approvals := stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	bare := newSeedRemote(t)
	url := "file://" + bare

	// Register materializes a clone into the managed checkout.
	p, err := svc.Register(context.Background(), "demo", url, "main")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.Status != StatusReady {
		t.Fatalf("status = %q, want ready", p.Status)
	}
	checkout := p.Checkout
	if checkout == "" {
		t.Fatal("no checkout recorded")
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		t.Fatalf("checkout is not a repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, "README.md")); err != nil {
		t.Fatalf("checkout missing seed content: %v", err)
	}
	if filepath.Dir(checkout) != store.CheckoutRoot() || filepath.Base(checkout) != p.ID {
		t.Errorf("checkout %q not derived from store root + project id", checkout)
	}

	// External push to the bare remote, then Sync fast-forwards the checkout.
	work := filepath.Join(t.TempDir(), "work")
	gitCmd(t, filepath.Dir(work), "clone", url, work)
	if err := os.WriteFile(filepath.Join(work, "pushed.txt"), []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "external push")
	gitCmd(t, work, "push", "origin", "main")

	p, err = svc.Sync(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if p.Status != StatusReady {
		t.Fatalf("sync status = %q, want ready", p.Status)
	}
	if _, err := os.Stat(filepath.Join(p.Checkout, "pushed.txt")); err != nil {
		t.Fatalf("checkout did not receive pushed file: %v", err)
	}

	// Delete removes checkout + entry; the remote survives.
	before, err := os.Stat(bare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Errorf("checkout still present after delete (err=%v)", err)
	}
	if _, err := store.Get(p.ID); err == nil {
		t.Error("entry still present after delete")
	}
	if after, err := os.Stat(bare); err != nil || !after.IsDir() || !os.SameFile(before, after) {
		t.Errorf("bare remote was touched by delete: %v", err)
	}
	if n := len(*approvals); n != 0 {
		t.Errorf("operator materialization consulted the approval gate %d time(s); want 0", n)
	}
}

// TestServiceFailedRegisterLeavesSyncError verifies DP-6 failure transition: a
// clone that fails leaves a sync_error entry with no checkout, and Sync then
// re-materializes it once the source is fixed.
func TestServiceFailedRegisterLeavesSyncError(t *testing.T) {
	stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)

	// file:// pointing at a nonexistent dir fails fast (no network needed).
	missing := filepath.Join(t.TempDir(), "does-not-exist.git")
	p, err := svc.Register(context.Background(), "broken", "file://"+missing, "")
	if err == nil {
		t.Fatal("register against a missing source succeeded")
	}
	if p.ID == "" {
		t.Fatalf("failed register left no entry (err %v)", err)
	}
	if p.Status != StatusSyncError {
		t.Errorf("status = %q, want sync_error", p.Status)
	}
	if p.Checkout != "" {
		t.Errorf("checkout = %q, want empty after failed clone", p.Checkout)
	}

	// Now make the source exist and Sync re-materializes the same entry.
	initRepo(t, missing)
	if _, err := svc.Sync(context.Background(), p.ID); err != nil {
		t.Fatalf("Sync after fixing source: %v", err)
	}
	got, err := store.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusReady {
		t.Errorf("status after re-materialize = %q, want ready", got.Status)
	}
	if _, err := os.Stat(filepath.Join(got.Checkout, "README.md")); err != nil {
		t.Errorf("re-materialized checkout missing content: %v", err)
	}
}

// TestServiceSyncRematerializesWhenCheckoutMissing covers a ready entry whose
// managed checkout was deleted out from under the registry.
func TestServiceSyncRematerializesWhenCheckoutMissing(t *testing.T) {
	stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	bare := newSeedRemote(t)

	p, err := svc.Register(context.Background(), "demo", "file://"+bare, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := os.RemoveAll(p.Checkout); err != nil {
		t.Fatal(err)
	}

	p, err = svc.Sync(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Sync with missing checkout: %v", err)
	}
	if p.Status != StatusReady {
		t.Errorf("status = %q, want ready", p.Status)
	}
	if _, err := os.Stat(filepath.Join(p.Checkout, "README.md")); err != nil {
		t.Errorf("re-cloned checkout missing content: %v", err)
	}
}

// TestServiceSyncDivergedFailsWithoutDeletingWork guards the no-clobber rule:
// a sync that cannot fast-forward fails into sync_error and never touches the
// working tree.
func TestServiceSyncDivergedFailsWithoutDeletingWork(t *testing.T) {
	stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	bare := newSeedRemote(t)

	p, err := svc.Register(context.Background(), "demo", "file://"+bare, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A local commit in the checkout, then an upstream push the checkout does
	// not have: ff-only pull must refuse and leave both intact.
	local := filepath.Join(p.Checkout, "local.txt")
	if err := os.WriteFile(local, []byte("local work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, p.Checkout, "add", ".")
	gitCmd(t, p.Checkout, "commit", "-m", "local commit")

	work := filepath.Join(t.TempDir(), "work")
	gitCmd(t, filepath.Dir(work), "clone", "file://"+bare, work)
	if err := os.WriteFile(filepath.Join(work, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "upstream commit")
	gitCmd(t, work, "push", "origin", "main")

	p, err = svc.Sync(context.Background(), p.ID)
	if err == nil {
		t.Fatal("sync of a diverged checkout succeeded")
	}
	if p.Status != StatusSyncError {
		t.Errorf("status = %q, want sync_error", p.Status)
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("local work was lost on failed sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Checkout, ".git")); err != nil {
		t.Errorf("checkout was destroyed on failed sync: %v", err)
	}
}

// TestServiceSyncDirtyTreeGuard verifies the project-ui.md rule: Sync refuses
// (ErrWorkingTreeDirty, status untouched) while the checkout holds uncommitted
// tracked changes, but untracked files alone never block a pull.
func TestServiceSyncDirtyTreeGuard(t *testing.T) {
	stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	bare := newSeedRemote(t)

	p, err := svc.Register(context.Background(), "demo", "file://"+bare, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Untracked-only dirtiness is fine: --ff-only never touches untracked
	// files, and refusing on their presence would block agent-shaped trees.
	if err := os.WriteFile(filepath.Join(p.Checkout, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(context.Background(), p.ID); err != nil {
		t.Fatalf("Sync with only an untracked file: %v", err)
	}

	// A tracked modification refuses the pull and leaves status untouched.
	if err := os.WriteFile(filepath.Join(p.Checkout, "README.md"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Sync(context.Background(), p.ID)
	if err == nil {
		t.Fatal("sync under tracked uncommitted changes succeeded")
	}
	if !errors.Is(err, ErrWorkingTreeDirty) {
		t.Errorf("err = %v, want ErrWorkingTreeDirty", err)
	}
	if got.Status != StatusReady {
		t.Errorf("refused sync changed status to %q; want ready", got.Status)
	}

	// Once the work is committed (clean tree), sync proceeds again.
	gitCmd(t, p.Checkout, "commit", "-am", "wip")
	if _, err := svc.Sync(context.Background(), p.ID); err != nil {
		t.Fatalf("Sync after committing the work: %v", err)
	}
}

// TestServiceStateReportsAheadBehind verifies State() drives the Projects page
// "behind upstream" affordance: a bounded fetch updates the remote-tracking
// refs, then branch/upstream and ahead/behind counts reflect both sides.
func TestServiceStateReportsAheadBehind(t *testing.T) {
	stubOperatorGate(t)
	home := t.TempDir()
	store, err := NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	bare := newSeedRemote(t)

	p, err := svc.Register(context.Background(), "demo", "file://"+bare, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// External push: the checkout is now one commit behind.
	work := filepath.Join(t.TempDir(), "work")
	gitCmd(t, filepath.Dir(work), "clone", "file://"+bare, work)
	if err := os.WriteFile(filepath.Join(work, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "upstream commit")
	gitCmd(t, work, "push", "origin", "main")

	// Local commit on top: the checkout is now one ahead AND one behind.
	if err := os.WriteFile(filepath.Join(p.Checkout, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, p.Checkout, "add", ".")
	gitCmd(t, p.Checkout, "commit", "-m", "local commit")

	st, err := svc.State(context.Background(), p.ID, true)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if st.Behind != 1 {
		t.Errorf("behind = %d, want 1 (fetch must have updated refs)", st.Behind)
	}
	if st.Ahead != 1 {
		t.Errorf("ahead = %d, want 1", st.Ahead)
	}
	if st.Upstream == "" {
		t.Error("upstream not reported")
	}
	if st.Staged+st.Modified+st.Untracked+st.Conflicts != 0 {
		t.Errorf("expected clean counts, got %+v", st)
	}
}
