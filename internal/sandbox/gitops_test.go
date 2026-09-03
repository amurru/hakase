package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
)

// gitBin returns the git executable, skipping the test when git is absent.
func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not installed: %v", err)
	}
	return p
}

// gitTestEnv returns the environment test git runs use: no global/system
// config, no terminal prompts, so a stray interactive prompt can never hang a
// test.
func gitTestEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

// initRepo creates a git repository at dir with a local identity and one
// "initial" commit. Uses the system git directly (not runGit) so test setup
// bypasses the policy stubs.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	g := gitBin(t)
	run := func(args ...string) error {
		cmd := &exec.Cmd{
			Path: g,
			Args: append([]string{g}, args...),
			Dir:  dir,
			Env:  gitTestEnv(),
		}
		_, err := cmd.CombinedOutput()
		return err
	}
	if err := run("init", "-b", "main"); err != nil {
		// git < 2.28 has no -b: init then rename.
		if err2 := run("init"); err2 != nil {
			t.Fatalf("git init: %v", err2)
		}
		if err2 := run("branch", "-M", "main"); err2 != nil {
			t.Fatalf("git branch -M main: %v", err2)
		}
	}
	if err := run("config", "user.name", "Hakase Test"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	if err := run("config", "user.email", "hakase@test.local"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := run("add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := run("commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}

// stubGitPolicy installs allow-all gate / approve-all approval stubs and
// restores the package globals afterwards. denyApproval forces approval
// denial for the approval-denied test. Returns the collected approval
// requests so tests can assert the approval identity.
func stubGitPolicy(t *testing.T, denyApproval bool) *[]interfaces.ApprovalRequest {
	t.Helper()
	origGate, origApprove, origSB := EvaluateCommandFunc, ApproveFunc, CurrentSandbox
	origAudit := AuditCommandFunc
	origExpiry := ApprovalExpiryFunc
	seen := &[]interfaces.ApprovalRequest{}
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow, Reason: ""}
	}
	ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) {
		*seen = append(*seen, req)
		return !denyApproval, nil
	}
	AuditCommandFunc = func(entry CommandAuditEntry) {}
	ApprovalExpiryFunc = func() time.Duration { return 60 * time.Second }
	t.Cleanup(func() {
		EvaluateCommandFunc, ApproveFunc, CurrentSandbox = origGate, origApprove, origSB
		AuditCommandFunc = origAudit
		ApprovalExpiryFunc = origExpiry
	})
	return seen
}

// TestCreateGitOpsTools verifies the toolset shape.
func TestCreateGitOpsTools(t *testing.T) {
	tools, err := CreateGitOpsTools(nil)
	if err != nil {
		t.Fatalf("CreateGitOpsTools: %v", err)
	}
	if len(tools) != 9 {
		t.Fatalf("expected 9 tools, got %d", len(tools))
	}
	want := []string{"git_status", "git_diff", "git_log", "git_branch", "git_stage", "git_commit", "git_clone", "git_push", "git_pull"}
	for i, name := range want {
		if tools[i].Name() != name {
			t.Errorf("tool[%d] = %q, want %q", i, tools[i].Name(), name)
		}
	}
}

func TestGitStatusFreshRepo(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	out, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("gitStatusContent: %v", err)
	}
	if out.NotARepo {
		t.Errorf("fresh repo reported NotARepo")
	}
	if out.Branch != "main" {
		t.Errorf("branch = %q, want main", out.Branch)
	}
	if len(out.Entries) != 0 {
		t.Errorf("expected no entries, got %d", len(out.Entries))
	}
}

func TestGitStatusUntrackedAndStaged(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("gitStatusContent: %v", err)
	}
	found := false
	for _, e := range out.Entries {
		if e.Path == "new.txt" && e.Status == "??" && !e.Staged {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ?? new.txt entry, got %+v", out.Entries)
	}

	// Untracked=false hides the untracked file.
	hideUntracked := false
	out, err = gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir, Untracked: &hideUntracked}, nil)
	if err != nil {
		t.Fatalf("gitStatusContent: %v", err)
	}
	for _, e := range out.Entries {
		if e.Status == "??" {
			t.Errorf("untracked file not hidden: %+v", out.Entries)
		}
	}

	// Stage it; the entry must flip to a staged 'A '.
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"new.txt"}}, nil); err != nil {
		t.Fatalf("gitStageContent: %v", err)
	}
	out, err = gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("gitStatusContent: %v", err)
	}
	staged := false
	for _, e := range out.Entries {
		if e.Path == "new.txt" && e.Status == "A " && e.Staged {
			staged = true
		}
	}
	if !staged {
		t.Errorf("expected staged A  new.txt, got %+v", out.Entries)
	}
}

func TestGitStatusRename(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"old.txt"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "add old"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	// Stage the rename with -A (both sides) so git detects it.
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"."}}, nil); err != nil {
		t.Fatal(err)
	}

	out, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range out.Entries {
		if e.From == "old.txt" && e.Path == "new.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rename old.txt -> new.txt, got %+v", out.Entries)
	}
}

func TestGitStatusNotARepo(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir() // no .git

	out, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("not-a-repo should not error, got %v", err)
	}
	if !out.NotARepo {
		t.Errorf("expected NotARepo=true for non-repo dir")
	}
}

func TestGitDiffUnstagedStagedAndPath(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	// Tracked files (committed), then modified: git diff only shows changes
	// to tracked files, never untracked ones.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "add a b", StageAll: boolPtr(true)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one-plus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two-plus\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unstaged diff contains both files.
	out, err := gitDiffContent(context.Background(), GitDiffInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Diff, "a.txt") || !strings.Contains(out.Diff, "b.txt") {
		t.Errorf("unstaged diff missing files: %q", out.Diff)
	}
	if out.Truncated {
		t.Errorf("small diff marked truncated")
	}

	// Stage a.txt only; staged diff shows just it, unstaged diff just b.txt.
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"a.txt"}}, nil); err != nil {
		t.Fatal(err)
	}
	staged := true
	outStaged, err := gitDiffContent(context.Background(), GitDiffInput{RepoDir: dir, Staged: &staged}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outStaged.Diff, "a.txt") || strings.Contains(outStaged.Diff, "b.txt") {
		t.Errorf("staged diff wrong: %q", outStaged.Diff)
	}
	outUnstaged, err := gitDiffContent(context.Background(), GitDiffInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outUnstaged.Diff, "a.txt") || !strings.Contains(outUnstaged.Diff, "b.txt") {
		t.Errorf("unstaged diff wrong: %q", outUnstaged.Diff)
	}

	// Path filter narrows the unstaged diff to b.txt (a.txt already staged).
	outPath, err := gitDiffContent(context.Background(), GitDiffInput{RepoDir: dir, Path: "b.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outPath.Diff, "b.txt") || strings.Contains(outPath.Diff, "a.txt") {
		t.Errorf("path-filtered diff wrong: %q", outPath.Diff)
	}
}

func TestGitDiffEmpty(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	out, err := gitDiffContent(context.Background(), GitDiffInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Diff != "" {
		t.Errorf("clean tree should have empty diff, got %q", out.Diff)
	}
}

func TestGitLog(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	for i, subj := range []string{"second", "third"} {
		fname := "f" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(subj+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: subj, StageAll: boolPtr(true)}, nil); err != nil {
			t.Fatal(err)
		}
	}

	out, err := gitLogContent(context.Background(), GitLogInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(out.Commits))
	}
	// Newest first.
	if out.Commits[0].Subject != "third" || out.Commits[1].Subject != "second" || out.Commits[2].Subject != "initial" {
		t.Errorf("unexpected order: %+v", Subjects(out))
	}
	if len(out.Commits[0].Sha) == 0 || len(out.Commits[0].Author) == 0 || len(out.Commits[0].Date) == 0 {
		t.Errorf("incomplete entry: %+v", out.Commits[0])
	}

	// Path filter: only commits touching fb.txt.
	outPath, err := gitLogContent(context.Background(), GitLogInput{RepoDir: dir, Path: "fb.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outPath.Commits) != 1 || outPath.Commits[0].Subject != "third" {
		t.Errorf("path-filtered log wrong: %+v", Subjects(outPath))
	}

	// Limit clamp: limit=100 -> 3 commits; limit=1 -> 1 commit.
	outClamp, err := gitLogContent(context.Background(), GitLogInput{RepoDir: dir, Limit: 1000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outClamp.Commits) != 3 {
		t.Errorf("clamped log = %d commits, want 3", len(outClamp.Commits))
	}
	outOne, err := gitLogContent(context.Background(), GitLogInput{RepoDir: dir, Limit: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outOne.Commits) != 1 || outOne.Commits[0].Subject != "third" {
		t.Errorf("limit=1 log wrong: %+v", Subjects(outOne))
	}
}

// Subjects is a small helper for readable assertions.
func Subjects(o GitLogOutput) []string {
	var s []string
	for _, c := range o.Commits {
		s = append(s, c.Subject)
	}
	return s
}

func boolPtr(b bool) *bool { return &b }

func TestGitBranch(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	g := gitBin(t)
	branchCmd := &exec.Cmd{Path: g, Args: []string{g, "-C", dir, "branch", "feature"}, Dir: dir, Env: gitTestEnv()}
	if out, err := branchCmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v (%s)", err, out)
	}
	// Simulated remote-tracking branch (no network needed).
	refCmd := &exec.Cmd{Path: g, Args: []string{g, "-C", dir, "update-ref", "refs/remotes/origin/main", "HEAD"}, Dir: dir, Env: gitTestEnv()}
	if out, err := refCmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v (%s)", err, out)
	}

	out, err := gitBranchContent(context.Background(), GitBranchInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := map[string]bool{"main": true, "feature": false}
	if len(out.Branches) != 2 {
		t.Fatalf("local branches = %+v", out.Branches)
	}
	for _, b := range out.Branches {
		cur, ok := wantLocal[b.Name]
		if !ok {
			t.Errorf("unexpected branch %q", b.Name)
		}
		if b.Current != cur {
			t.Errorf("branch %q current = %v, want %v", b.Name, b.Current, cur)
		}
	}

	all := true
	outAll, err := gitBranchContent(context.Background(), GitBranchInput{RepoDir: dir, All: &all}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sawRemote := false
	for _, b := range outAll.Branches {
		if strings.HasPrefix(b.Name, "remotes/") {
			sawRemote = true
		}
	}
	if !sawRemote {
		t.Errorf("--all did not include remote-tracking branches: %+v", outAll.Branches)
	}
}

func TestGitStageEmptyPaths(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir}, nil); err == nil {
		t.Fatal("empty paths should error")
	} else if !strings.Contains(err.Error(), "at least one path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGitStageUnstage(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "u.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"u.txt"}}, nil); err != nil {
		t.Fatal(err)
	}
	unstage := true
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"u.txt"}, Unstage: &unstage}, nil); err != nil {
		t.Fatalf("unstage: %v", err)
	}

	out, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Entries {
		if e.Path == "u.txt" && e.Staged {
			t.Errorf("u.txt still staged after unstage: %+v", out.Entries)
		}
	}
}

func TestGitStageApprovalDenied(t *testing.T) {
	origGate, origApprove, origSB := EvaluateCommandFunc, ApproveFunc, CurrentSandbox
	origAudit := AuditCommandFunc
	origExpiry := ApprovalExpiryFunc
	// Gate asks for approval on every command; approval is denied.
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAsk, Risk: RiskMedium, Reason: "gate asks"}
	}
	ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) {
		return false, nil
	}
	AuditCommandFunc = func(entry CommandAuditEntry) {}
	ApprovalExpiryFunc = func() time.Duration { return 60 * time.Second }
	t.Cleanup(func() {
		EvaluateCommandFunc, ApproveFunc, CurrentSandbox = origGate, origApprove, origSB
		AuditCommandFunc = origAudit
		ApprovalExpiryFunc = origExpiry
	})

	dir := t.TempDir()
	initRepo(t, dir)

	_, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"."}}, nil)
	if err == nil {
		t.Fatal("expected approval-denied error")
	}
	if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("expected 'not approved' error, got: %v", err)
	}
}

func TestGitCommitLifecycle(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	// Plain commit: write, stage, commit.
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"work.txt"}}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "add work"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sha) != 40 || len(out.ShortSha) != 7 {
		t.Errorf("commit sha fields wrong: %+v", out)
	}
	if out.Subject != "add work" {
		t.Errorf("subject = %q, want %q", out.Subject, "add work")
	}

	// stage_all: modify + commit in one call.
	if err := os.WriteFile(filepath.Join(dir, "stageall.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out2, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "stage all", StageAll: boolPtr(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Subject != "stage all" {
		t.Errorf("subject = %q", out2.Subject)
	}

	// log reflects both commits.
	logOut, err := gitLogContent(context.Background(), GitLogInput{RepoDir: dir, Limit: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(logOut.Commits) != 3 || logOut.Commits[0].Subject != "stage all" || logOut.Commits[1].Subject != "add work" {
		t.Errorf("log after commits wrong: %+v", Subjects(logOut))
	}
}

func TestGitCommitErrors(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	// Empty message is an argument error (no exec).
	if _, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "   "}, nil); err == nil {
		t.Fatal("empty message should error")
	} else if !strings.Contains(err.Error(), "message is required") {
		t.Errorf("unexpected error: %v", err)
	}

	// Nothing to commit surfaces the git error, not a fake success.
	_, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: dir, Message: "nothing"}, nil)
	if err == nil {
		t.Fatal("nothing-to-commit should error")
	}
	if !strings.Contains(err.Error(), "git commit") {
		t.Errorf("expected git commit error, got: %v", err)
	}
}

func TestGitConfinementOutsideWorkspace(t *testing.T) {
	stubGitPolicy(t, false)
	rootA := t.TempDir()
	rootB := t.TempDir()
	initRepo(t, rootB) // repo lives OUTSIDE the workspace

	CurrentSandbox = &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{rootA},
		ReadRoots:      []string{rootA},
	}

	// Read-only op outside the workspace must be rejected.
	if _, err := gitStatusContent(context.Background(), GitStatusInput{RepoDir: rootB}, nil); err == nil {
		t.Fatal("status on repo outside workspace should error")
	}

	// Mutating op outside the workspace must be rejected too.
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: rootB, Paths: []string{"."}}, nil); err == nil {
		t.Fatal("stage on repo outside workspace should error")
	}
}

func TestCappedCapture(t *testing.T) {
	c := &cappedCapture{remaining: 10}
	w := captureWriter{c: c, w: &c.stdout}
	if _, err := w.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatal(err)
	}
	if got := c.stdout.String(); got != "0123456789" {
		t.Errorf("stdout = %q, want first 10 bytes", got)
	}
	if !c.truncated {
		t.Error("expected truncated=true after overflow")
	}
	// Subsequent writes stay no-ops but are claimed.
	if _, err := w.Write([]byte("ZZZ")); err != nil {
		t.Fatal(err)
	}
	if c.stdout.String() != "0123456789" {
		t.Errorf("buffer grew after cap: %q", c.stdout.String())
	}
}

// ---------------------------------------------------------------------------
// Git workspace snapshot + project-root defaults
// ---------------------------------------------------------------------------

func TestParsePorcelainStatusUpstream(t *testing.T) {
	branch, upstream, ahead, behind, _ := parsePorcelainStatus("## main...origin/main [ahead 2, behind 1]\n M file.txt\n")
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
	if upstream != "origin/main" {
		t.Errorf("upstream = %q, want origin/main", upstream)
	}
	if ahead != 2 || behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 2/1", ahead, behind)
	}
}

func TestParsePorcelainStatusDetached(t *testing.T) {
	branch, upstream, _, _, _ := parsePorcelainStatus("## HEAD (no branch)\n")
	if !strings.HasSuffix(branch, " (no branch)") {
		t.Errorf("detached header branch = %q, want suffix \" (no branch)\"", branch)
	}
	if upstream != "" {
		t.Errorf("detached upstream = %q, want empty", upstream)
	}
}

func TestCountGitEntries(t *testing.T) {
	_, _, _, _, entries := parsePorcelainStatus(strings.Join([]string{
		"## main",
		"M  staged.txt",   // staged only
		" M unstaged.txt", // modified only
		"MM both.txt",     // staged + modified in worktree
		"?? new.txt",
		"UU conflict.txt",
		"AA added-both.txt",
	}, "\n"))
	staged, modified, untracked, conflicts := countGitEntries(entries)
	if staged != 2 {
		t.Errorf("staged = %d, want 2 (staged.txt, both.txt)", staged)
	}
	if modified != 1 {
		t.Errorf("modified = %d, want 1 (unstaged.txt)", modified)
	}
	if untracked != 1 {
		t.Errorf("untracked = %d, want 1", untracked)
	}
	if conflicts != 2 {
		t.Errorf("conflicts = %d, want 2 (conflict.txt, added-both.txt)", conflicts)
	}
}

func TestBuildGitWorkspaceBlock(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	// Dirty tree: one staged, one modified, one untracked file.
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: dir, Paths: []string{"staged.txt"}}, nil); err != nil {
		t.Fatalf("gitStageContent: %v", err)
	}
	// README.md is tracked (committed by initRepo); edit it for " M ".
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	block, err := BuildGitWorkspaceBlock(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("BuildGitWorkspaceBlock: %v", err)
	}
	if block == "" {
		t.Fatal("expected a non-empty workspace block")
	}
	for _, want := range []string{
		"Root: " + dir,
		"Branch: main",
		"staged 1",
		"modified 1",
		"untracked 1",
		"Recent commits (newest first):",
		"initial",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "conflicts") {
		t.Errorf("block reports conflicts on a clean-merge tree:\n%s", block)
	}
}

func TestBuildGitWorkspaceBlockCleanRepo(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	initRepo(t, dir)

	block, err := BuildGitWorkspaceBlock(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("BuildGitWorkspaceBlock: %v", err)
	}
	if !strings.Contains(block, "Status: clean") {
		t.Errorf("clean repo block missing \"Status: clean\":\n%s", block)
	}
	if !strings.Contains(block, "Branch: main") {
		t.Errorf("clean repo block missing branch:\n%s", block)
	}
}

func TestBuildGitWorkspaceBlockNotARepo(t *testing.T) {
	stubGitPolicy(t, false)
	dir := t.TempDir()
	block, err := BuildGitWorkspaceBlock(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("BuildGitWorkspaceBlock(non-repo): %v", err)
	}
	if block != "" {
		t.Errorf("non-repo block = %q, want empty", block)
	}
}

func TestResolveRepoDirDefaultsToProjectRoot(t *testing.T) {
	origSB := CurrentSandbox
	CurrentSandbox = nil
	defer func() { CurrentSandbox = origSB }()

	root := t.TempDir()
	initRepo(t, root)
	project.SetCurrentRoot(root)
	defer project.SetCurrentRoot("")

	dir, err := resolveRepoDir("", false)
	if err != nil {
		t.Fatalf("resolveRepoDir: %v", err)
	}
	if dir != root {
		t.Errorf("resolveRepoDir default = %q, want project root %q", dir, root)
	}

	// Without a session project root the working directory fallback applies.
	project.SetCurrentRoot("")
	cwd, _ := os.Getwd()
	if dir, err := resolveRepoDir("", false); err != nil || dir != cwd {
		t.Errorf("resolveRepoDir without project = %q, want cwd %q (err %v)", dir, cwd, err)
	}
}

// ---------------------------------------------------------------------------
// git_clone / git_push / git_pull
// ---------------------------------------------------------------------------

// bareCloneOf creates a bare repository from a seeded worktree (no network).
func bareCloneOf(t *testing.T, seed string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	g := gitBin(t)
	cmd := &exec.Cmd{Path: g, Args: []string{g, "clone", "--bare", seed, bare}, Dir: t.TempDir(), Env: gitTestEnv()}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v (%s)", err, out)
	}
	return bare
}

func TestValidateCloneSource(t *testing.T) {
	origSB := CurrentSandbox
	defer func() { CurrentSandbox = origSB }()

	if err := validateCloneSource("https://example.com/repo.git"); err != nil {
		t.Errorf("https URL rejected: %v", err)
	}
	if err := validateCloneSource("git@example.com:repo.git"); err == nil {
		t.Error("scp-style source accepted (no scheme)")
	} else if err := validateCloneSource("ssh://git@example.com/repo.git"); err != nil {
		t.Errorf("ssh URL rejected: %v", err)
	}
	if err := validateCloneSource("ftp://example.com/repo.git"); err == nil {
		t.Error("ftp scheme accepted")
	}
	if err := validateCloneSource(""); err == nil {
		t.Error("empty source accepted")
	}

	// file:// and local paths are fine without a sandbox...
	CurrentSandbox = nil
	if err := validateCloneSource("file:///tmp/seed"); err != nil {
		t.Errorf("file:// without sandbox rejected: %v", err)
	}
	if err := validateCloneSource("/tmp/seed"); err != nil {
		t.Errorf("local path without sandbox rejected: %v", err)
	}
	// ...and rejected while a sandbox is active (they bypass read roots).
	CurrentSandbox = LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
	if err := validateCloneSource("file:///tmp/seed"); err == nil {
		t.Error("file:// with active sandbox accepted")
	}
	if err := validateCloneSource("/tmp/seed"); err == nil {
		t.Error("local path with active sandbox accepted")
	}
}

func TestGitCloneLocal(t *testing.T) {
	stubGitPolicy(t, false)
	seed := t.TempDir()
	initRepo(t, seed)

	target := filepath.Join(t.TempDir(), "clone-a")
	out, err := gitCloneContent(context.Background(), GitCloneInput{URL: "file://" + seed, Dir: target}, nil)
	if err != nil {
		t.Fatalf("gitCloneContent: %v", err)
	}
	if out.Dir == "" {
		t.Error("clone output missing dir")
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err != nil {
		t.Errorf("clone target missing README.md: %v", err)
	}
}

func TestGitCloneRejectsSandboxedLocalSource(t *testing.T) {
	stubGitPolicy(t, false)
	origSB := CurrentSandbox
	CurrentSandbox = LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
	defer func() { CurrentSandbox = origSB }()

	_, err := gitCloneContent(context.Background(), GitCloneInput{
		URL: "file://" + t.TempDir(),
		Dir: filepath.Join(t.TempDir(), "out"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("expected sandbox rejection, got %v", err)
	}
}

func TestGitPushPullLoop(t *testing.T) {
	stubGitPolicy(t, false)
	seed := t.TempDir()
	initRepo(t, seed)
	bare := bareCloneOf(t, seed)

	workA := filepath.Join(t.TempDir(), "work-a")
	if _, err := gitCloneContent(context.Background(), GitCloneInput{URL: bare, Dir: workA}, nil); err != nil {
		t.Fatalf("clone work-a: %v", err)
	}
	workB := filepath.Join(t.TempDir(), "work-b")
	if _, err := gitCloneContent(context.Background(), GitCloneInput{URL: bare, Dir: workB}, nil); err != nil {
		t.Fatalf("clone work-b: %v", err)
	}

	// work-a adds a commit and pushes it to origin/main.
	if err := os.WriteFile(filepath.Join(workA, "extra.txt"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitStageContent(context.Background(), GitStageInput{RepoDir: workA, Paths: []string{"extra.txt"}}, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := gitCommitContent(context.Background(), GitCommitInput{RepoDir: workA, Message: "feat: extra"}, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	upstream := true
	pushOut, err := gitPushContent(context.Background(), GitPushInput{RepoDir: workA, Remote: "origin", Branch: "main", SetUpstream: &upstream}, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushOut.NotARepo || pushOut.Message == "" {
		t.Errorf("unexpected push output: %+v", pushOut)
	}

	// work-b fast-forwards from the bare remote and sees the new commit.
	pullOut, err := gitPullContent(context.Background(), GitPullInput{RepoDir: workB, Remote: "origin", Branch: "main"}, nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pullOut.NotARepo {
		t.Error("pull reported NotARepo on a valid repo")
	}
	if _, err := os.Stat(filepath.Join(workB, "extra.txt")); err != nil {
		t.Errorf("work-b missing pushed file after pull: %v", err)
	}
}

func TestGitPushPullValidationAndNotARepo(t *testing.T) {
	stubGitPolicy(t, false)

	if _, err := gitPushContent(context.Background(), GitPushInput{Remote: "bad remote"}, nil); err == nil {
		t.Error("invalid remote name accepted")
	}
	if _, err := gitPullContent(context.Background(), GitPullInput{Remote: "origin", Branch: "-force"}, nil); err == nil {
		t.Error("invalid branch name accepted")
	}

	// Not a repository: push/pull surface NotARepo, not a hard error.
	dir := t.TempDir()
	po, err := gitPushContent(context.Background(), GitPushInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("push in non-repo: %v", err)
	}
	if !po.NotARepo {
		t.Errorf("push in non-repo did not report NotARepo: %+v", po)
	}
	pull, err := gitPullContent(context.Background(), GitPullInput{RepoDir: dir}, nil)
	if err != nil {
		t.Fatalf("pull in non-repo: %v", err)
	}
	if !pull.NotARepo {
		t.Errorf("pull in non-repo did not report NotARepo: %+v", pull)
	}
}
