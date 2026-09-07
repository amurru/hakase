package cli

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// projectsStubGate installs the sandbox gate hooks the git engine requires in
// unit tests (they are main-wired in the real binary). Every command asks, so
// the operator-authorized materialization path is exercised - the collected
// approval list must stay empty.
func projectsStubGate(t *testing.T) *[]interfaces.ApprovalRequest {
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

// CLI tests drive RunProjectCLI with HAKASE_HOME redirected to a temp dir and
// a local bare remote as the clone source (sandbox-off, per D9). The sandbox
// gate hooks are main-wired, so in these tests EvaluateCommandFunc is nil and
// the gate is skipped entirely - matching any other internal/cli test.

func projectsGitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not installed: %v", err)
	}
	return p
}

func projectsGitEnv() []string {
	// GIT_CONFIG_GLOBAL/SYSTEM pinning keeps the developer's ~/.gitconfig out
	// of test checkouts; explicit author/committer env keeps commits working
	// without it.
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Hakase Test", "GIT_AUTHOR_EMAIL=hakase@test.local",
		"GIT_COMMITTER_NAME=Hakase Test", "GIT_COMMITTER_EMAIL=hakase@test.local")
}

// projectsGit runs system git in dir with the isolated test env.
func projectsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	g := projectsGitBin(t)
	cmd := &exec.Cmd{
		Path: g,
		Args: append([]string{g}, args...),
		Dir:  dir,
		Env:  projectsGitEnv(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// projectsSeedRemote creates a seeded repo and returns a bare remote for it.
func projectsSeedRemote(t *testing.T) string {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	projectsGit(t, seed, "init", "-b", "main")
	projectsGit(t, seed, "config", "user.name", "Hakase Test")
	projectsGit(t, seed, "config", "user.email", "hakase@test.local")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectsGit(t, seed, "add", ".")
	projectsGit(t, seed, "commit", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "remote.git")
	projectsGit(t, t.TempDir(), "clone", "--bare", seed, bare)
	return bare
}

// projectsStore loads the registry under the redirected HAKASE_HOME.
func projectsStore(t *testing.T) *registry.Store {
	t.Helper()
	st, err := registry.NewStore(registry.DefaultPath())
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	return st
}

// TestProjectsRegisterListSyncDeleteCLI is the end-to-end CLI lifecycle:
// register clones from a local bare remote, sync fast-forwards after an
// external push, delete removes checkout + entry and leaves the remote alone.
func TestProjectsRegisterListSyncDeleteCLI(t *testing.T) {
	approvals := projectsStubGate(t)
	t.Setenv("HAKASE_HOME", t.TempDir())
	bare := projectsSeedRemote(t)
	url := "file://" + bare

	if code := RunProjectCLI([]string{"register", "demo", url, "--ref", "main"}); code != 0 {
		t.Fatalf("register exited %d, want 0", code)
	}
	st := projectsStore(t)
	p, err := st.GetByName("demo")
	if err != nil {
		t.Fatalf("entry not persisted: %v", err)
	}
	if p.Status != registry.StatusReady {
		t.Fatalf("status = %q, want ready", p.Status)
	}
	if _, err := os.Stat(filepath.Join(p.Checkout, "README.md")); err != nil {
		t.Fatalf("checkout missing content: %v", err)
	}

	// External push to the bare remote; sync fast-forwards the checkout.
	work := filepath.Join(t.TempDir(), "work")
	projectsGit(t, filepath.Dir(work), "clone", url, work)
	if err := os.WriteFile(filepath.Join(work, "remote.txt"), []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectsGit(t, work, "add", ".")
	projectsGit(t, work, "commit", "-m", "external push")
	projectsGit(t, work, "push", "origin", "main")

	if code := RunProjectCLI([]string{"sync", "demo"}); code != 0 {
		t.Fatalf("sync exited %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(p.Checkout, "remote.txt")); err != nil {
		t.Fatalf("checkout did not receive pushed file: %v", err)
	}

	if code := RunProjectCLI([]string{"list"}); code != 0 {
		t.Fatalf("list exited %d, want 0", code)
	}

	if code := RunProjectCLI([]string{"delete", "demo"}); code != 0 {
		t.Fatalf("delete exited %d, want 0", code)
	}
	if _, err := os.Stat(p.Checkout); !os.IsNotExist(err) {
		t.Errorf("checkout still present after delete (err=%v)", err)
	}
	if _, err := projectsStore(t).GetByName("demo"); err == nil {
		t.Error("entry still present after delete")
	}
	if _, err := os.Stat(bare); err != nil {
		t.Errorf("bare remote removed by delete: %v", err)
	}
	if n := len(*approvals); n != 0 {
		t.Errorf("operator materialization consulted the approval gate %d time(s); want 0", n)
	}
}

// TestProjectsCLIUsageAndValidation covers argument/validation exit codes
// without touching git.
func TestProjectsCLIUsageAndValidation(t *testing.T) {
	projectsStubGate(t)
	t.Setenv("HAKASE_HOME", t.TempDir())
	base := t.TempDir()

	// No subcommand prints usage and exits 0.
	if code := RunProjectCLI(nil); code != 0 {
		t.Errorf("no args exited %d, want 0", code)
	}
	if code := RunProjectCLI([]string{"help"}); code != 2 {
		t.Errorf("unknown subcommand exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"register"}); code != 2 {
		t.Errorf("register without args exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"register", "../evil", "https://example.com/x.git"}); code != 2 {
		t.Errorf("register with bad name exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"register", "demo", "ftp://example.com/x.git"}); code != 2 {
		t.Errorf("register with unsupported scheme exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"register", "demo", filepath.Join(base, "local-path")}); code != 2 {
		t.Errorf("register with scheme-less path exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"sync", "nope"}); code != 1 {
		t.Errorf("sync of unknown project exited %d, want 1", code)
	}
	if code := RunProjectCLI([]string{"sync"}); code != 2 {
		t.Errorf("sync without args exited %d, want 2", code)
	}
	if code := RunProjectCLI([]string{"delete", "nope"}); code != 1 {
		t.Errorf("delete of unknown project exited %d, want 1", code)
	}
	if code := RunProjectCLI([]string{"list", "extra"}); code != 2 {
		t.Errorf("list with positional exited %d, want 2", code)
	}

	// Empty registry lists cleanly.
	if code := RunProjectCLI([]string{"list"}); code != 0 {
		t.Errorf("empty list exited %d, want 0", code)
	}
}
