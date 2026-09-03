package main

import (
	"amurru/hakase/internal/cli"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProjectsCLIHeadlessOperator verifies the operator-authorized git path
// under the real binary wiring (cmd/hakase/init.go installs the sandbox gate
// hooks): the agent evaluator classifies `git clone`/`git pull` as approval
// asks, and headless there is no approval mechanism (ApproveExec fails
// closed). `hakase projects register/sync` must still work, because the human
// running the command is the authorizer (DP-11) - the materialization never
// consults the approval gate.

func mainGitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not installed: %v", err)
	}
	return p
}

func mainGitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func mainGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	g := mainGitBin(t)
	cmd := &exec.Cmd{
		Path: g,
		Args: append([]string{g}, args...),
		Dir:  dir,
		Env:  mainGitEnv(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestProjectsCLIHeadlessOperator(t *testing.T) {
	t.Setenv("HAKASE_HOME", filepath.Join(t.TempDir(), "home"))

	// Seed a repo and bare remote (child-process git, not the CLI under test).
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGit(t, seed, "init", "-b", "main")
	mainGit(t, seed, "config", "user.name", "Hakase Test")
	mainGit(t, seed, "config", "user.email", "hakase@test.local")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainGit(t, seed, "add", ".")
	mainGit(t, seed, "commit", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "remote.git")
	mainGit(t, t.TempDir(), "clone", "--bare", seed, bare)
	url := "file://" + bare

	// Headless register must succeed (clone classified MEDIUM -> would ask).
	if code := cli.Dispatch([]string{"projects", "register", "demo", url, "--ref", "main"}); code != 0 {
		t.Fatalf("headless register exited %d, want 0 (operator git path broken?)", code)
	}

	// External push, then a headless sync (pull classified MEDIUM -> would ask).
	work := filepath.Join(t.TempDir(), "work")
	mainGit(t, filepath.Dir(work), "clone", url, work)
	if err := os.WriteFile(filepath.Join(work, "remote.txt"), []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainGit(t, work, "add", ".")
	mainGit(t, work, "commit", "-m", "external push")
	mainGit(t, work, "push", "origin", "main")

	if code := cli.Dispatch([]string{"projects", "sync", "demo"}); code != 0 {
		t.Fatalf("headless sync exited %d, want 0", code)
	}

	checkout := filepath.Join(os.Getenv("HAKASE_HOME"), "projects")
	entries, err := os.ReadDir(checkout)
	if err != nil {
		t.Fatalf("no managed checkouts under %s: %v", checkout, err)
	}
	found := false
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(checkout, e.Name(), "remote.txt")); err == nil {
			found = true
		}
	}
	if !found {
		t.Error("synced file not found in any managed checkout")
	}

	if code := cli.Dispatch([]string{"projects", "delete", "demo"}); code != 0 {
		t.Fatalf("headless delete exited %d, want 0", code)
	}
}
