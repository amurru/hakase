// r30_r33_loop2_test.go - Full-lane loop 2/2 regression tests for review
// findings H1, M2, M4, M5, M6 (R30-R33).
//
// H1: the interpreter venv is the run workspace's venv even when the
// process cwd differs (single absolute root via pipBinForPyBin anchoring).
// M2: sitecustomize as a package (sitecustomize/__init__.py) is an import
// hook like the module file.
// M4: an on-disk skills/<name>.py counts as existing (fresh update
// approval) even without a registry entry.
// M5: the write_file/patch reroute evaluates the unified gate
// non-interactively (no approval prompt before the inevitable denial).
// M6: empty sessions fail closed (no shared bucket).
package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// TestH1WorkspaceVenvWinsOverCwd pins the H1 single root (rogue leg):
// the run workspace holds a rogue hook while the process cwd holds a
// clean venv. The start must refuse (workspace venv governs) and never
// spawn. Anchor assertions live in
// TestH1WorkspaceVenvCreatedInWorkspace, which reaches the spawn spy;
// asserting them here would be dead code (the refused start never
// reaches the spy).
func TestH1WorkspaceVenvWinsOverCwd(t *testing.T) {
	wsDir := t.TempDir()
	wsVenvBin := filepath.Join(wsDir, ".venv", "bin")
	if err := os.MkdirAll(wsVenvBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".venv", "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsVenvBin, "python3"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wsDir, ".venv", "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".venv", "lib", "sitecustomize.py"), []byte("import os\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cwdDir := t.TempDir()
	cwdVenvBin := filepath.Join(cwdDir, ".venv", "bin")
	if err := os.MkdirAll(cwdVenvBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, ".venv", "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdVenvBin, "python3"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwdDir)

	installPipTestDeps(t, &sandbox.SandboxConfig{
		Mode:           sandbox.SandboxModePaths,
		WorkspaceRoots: []string{wsDir},
		Permissions:    map[string]string{"python_interpreter": "allow"},
	}, nil)

	spawned := false
	savedExec := execPythonScript
	execPythonScript = func(ctx context.Context, pyBin, scriptPath, workDir string, env []string) (string, string) {
		spawned = true
		return "", ""
	}
	t.Cleanup(func() { execPythonScript = savedExec })

	if _, err := drivePythonRun(t, "X = 1\n"); err == nil {
		t.Fatal("workspace venv with rogue hook: want refusal even though cwd venv is clean")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refusal must name the integrity gate, got: %v", err)
	}
	if spawned {
		t.Fatal("refused start must not spawn the interpreter")
	}
}

// TestH1WorkspaceVenvCreatedInWorkspace pins the H1 creator leg: the
// workspace holds no venv while the process cwd holds a clean one. The
// start must create the venv in the workspace (not reuse cwd/.venv) and
// reach the spawn with an absolute workspace-anchored pyBin whose pip
// root equals the workspace venv.
func TestH1WorkspaceVenvCreatedInWorkspace(t *testing.T) {
	wsDir := t.TempDir()

	cwdDir := t.TempDir()
	cwdVenvBin := filepath.Join(cwdDir, ".venv", "bin")
	if err := os.MkdirAll(cwdVenvBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, ".venv", "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdVenvBin, "python3"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwdDir)

	installPipTestDeps(t, &sandbox.SandboxConfig{
		Mode:           sandbox.SandboxModePaths,
		WorkspaceRoots: []string{wsDir},
		Permissions:    map[string]string{"python_interpreter": "allow"},
	}, nil)

	var createdVenvDir string
	savedCreate := execCreateVenv
	execCreateVenv = func(spec []string) error {
		if len(spec) == 0 {
			t.Errorf("H1: empty venv creator spec")
			return nil
		}
		createdVenvDir = spec[len(spec)-1]
		// Materialize a minimal clean venv at the requested root so
		// verify passes and the run reaches the spawn spy.
		binDir := filepath.Join(createdVenvDir, "bin")
		pyName := "python3"
		if runtime.GOOS == "windows" {
			binDir = filepath.Join(createdVenvDir, "Scripts")
			pyName = "python.exe"
		}
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(createdVenvDir, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(binDir, pyName), []byte("#!/bin/sh\n"), 0755); err != nil {
			return err
		}
		return nil
	}
	t.Cleanup(func() { execCreateVenv = savedCreate })

	spawned := false
	var gotPyBin, gotWorkDir string
	savedExec := execPythonScript
	execPythonScript = func(ctx context.Context, pyBin, scriptPath, workDir string, env []string) (string, string) {
		spawned = true
		gotPyBin, gotWorkDir = pyBin, workDir
		return "", ""
	}
	t.Cleanup(func() { execPythonScript = savedExec })

	if _, err := drivePythonRun(t, "X = 1\n"); err != nil {
		t.Fatalf("clean workspace with absent venv must start (creating it there), got: %v", err)
	}
	if !spawned {
		t.Fatal("clean-workspace start must reach the spawn spy")
	}
	if createdVenvDir == "" {
		t.Fatal("absent workspace venv must trigger creation")
	}
	if !filepath.IsAbs(createdVenvDir) {
		t.Errorf("H1: created venvDir must be absolute, got %q", createdVenvDir)
	}
	if got := filepath.Clean(createdVenvDir); got != filepath.Join(wsDir, ".venv") {
		t.Errorf("H1: venv must land in workspace %q, got %q", filepath.Join(wsDir, ".venv"), got)
	}
	if !filepath.IsAbs(gotPyBin) {
		t.Errorf("H1: pyBin must be absolute (workspace-anchored), got %q", gotPyBin)
	}
	if got := absVenvRoot(pipBinForPyBin(gotPyBin, gotWorkDir)); got != filepath.Join(wsDir, ".venv") {
		t.Errorf("H1: pip root %q must equal workspace venv %q", got, filepath.Join(wsDir, ".venv"))
	}
	if filepath.Clean(gotWorkDir) != filepath.Clean(wsDir) {
		t.Errorf("H1: spawn workDir %q must equal workspace %q", gotWorkDir, wsDir)
	}
}

// TestM2SitecustomizePackageForm pins the M2 hook leg: a
// sitecustomize/__init__.py package init executes at startup like the
// module file, so untrusted content denies with an empty trust set.
func TestM2SitecustomizePackageForm(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	sp := filepath.Join(venv, "lib", "python3.14", "site-packages", "sitecustomize")
	if err := os.MkdirAll(sp, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp, "__init__.py"), []byte("import os\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyVenvIntegrity(venv, nil); err == nil {
		t.Fatal("sitecustomize package __init__.py with empty trust: want denial")
	}
	if err := os.Remove(filepath.Join(sp, "__init__.py")); err != nil {
		t.Fatal(err)
	}
	if err := verifyVenvIntegrity(venv, nil); err != nil {
		t.Fatalf("hook-free venv must pass, got: %v", err)
	}
}

// TestM2SitecustomizePackagePyc pins the M2 compiled-package leg: a
// sitecustomize/__init__.pyc executes at startup like __init__.py, so
// untrusted content denies with an empty trust set and removal passes.
func TestM2SitecustomizePackagePyc(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	sp := filepath.Join(venv, "lib", "python3.14", "site-packages", "sitecustomize")
	if err := os.MkdirAll(sp, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp, "__init__.pyc"), []byte("compiled-hook"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyVenvIntegrity(venv, nil); err == nil {
		t.Fatal("sitecustomize package __init__.pyc with empty trust: want denial")
	}
	if err := os.Remove(filepath.Join(sp, "__init__.pyc")); err != nil {
		t.Fatal(err)
	}
	if err := verifyVenvIntegrity(venv, nil); err != nil {
		t.Fatalf("hook-free venv must pass, got: %v", err)
	}
}

// TestM2StartupImportableSuffixes pins the full M2 hook set: every
// startup-importable suffix (py, pyc, pyd, so) denies in both the module
// form (sitecustomize.<ext> / usercustomize.<ext>) and the package form
// (sitecustomize/__init__.<ext> / usercustomize/__init__.<ext>).
func TestM2StartupImportableSuffixes(t *testing.T) {
	cases := []string{}
	for _, mod := range []string{"sitecustomize", "usercustomize"} {
		for _, ext := range []string{"py", "pyc", "pyd", "so"} {
			cases = append(cases, mod+"."+ext)
			cases = append(cases, filepath.Join(mod, "__init__."+ext))
		}
	}
	for _, rel := range cases {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			venv := filepath.Join(dir, ".venv")
			if err := os.MkdirAll(venv, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
				t.Fatal(err)
			}
			full := filepath.Join(venv, "lib", rel)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("hook"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := verifyVenvIntegrity(venv, nil); err == nil {
				t.Fatalf("%s with empty trust: want denial", rel)
			}
		})
	}
}

// TestM4OnDiskSkillCountsAsExisting pins M4: skills/<name>.py on disk
// counts as existing even when skills.json lacks the entry, so the
// update path (fresh approval) applies and a headless overwrite refuses.
func TestM4OnDiskSkillCountsAsExisting(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "ghost.py"), []byte("old = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !skillExists("ghost") {
		t.Fatal("on-disk skills/ghost.py must count as existing without a registry entry")
	}
	sess := "sess-m4-ghost"
	task := "task-m4-ghost"
	code := "new = 2\n"
	recordSkillProvenance(sess, skillCodeHash(code))
	if _, err := runSaveSkillWithSession(t, task, sess, "ghost", "d", code); err == nil {
		t.Fatal("overwrite of on-disk skill headless: want fresh-approval refusal")
	} else if !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("refusal must demand fresh approval, got: %v", err)
	}
}

// TestM5RerouteDoesNotPrompt pins M5: a confined write_file into the
// skills library of an existing skill evaluates the unified gate in
// non-interactive (forWriteTool) mode - no approval prompt fires before
// the inevitable save_skill denial.
func TestM5RerouteDoesNotPrompt(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	code := "M = 5\n"
	sess := "sess-m5-reroute"
	task := "task-m5-reroute"
	recordSkillProvenance(sess, skillCodeHash(code))
	if err := os.WriteFile(filepath.Join(dir, "skills", "rerouted.py"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !skillExists("rerouted") {
		t.Fatal("fixture: on-disk skill must count as existing (M4)")
	}
	// Any approval prompt during a reroute is a failure: the gate must
	// fail closed without asking (the denial below always follows).
	_ = installPipTestDeps(t, &sandbox.SandboxConfig{}, func(req interfaces.ApprovalRequest) (bool, error) {
		t.Errorf("M5: reroute must not raise an approval prompt, got %q", req.Command)
		return false, nil
	})
	rctx := sandbox.RerouteNoPromptCtx(flowSessionCtx(t, task, sess))
	if err := checkSaveSkillAllowed(rctx, "rerouted", code, ""); err == nil {
		t.Fatal("reroute for existing skill: want denial without prompting")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("reroute denial must name save_skill, got: %v", err)
	}
}

// TestM6EmptySessionFailsClosed pins M6: provenance recorded under one
// session never authorizes another, and the empty session carries
// nothing (record drops it, check denies it).
func TestM6EmptySessionFailsClosed(t *testing.T) {
	code := "Q = 6\n"
	h := skillCodeHash(code)
	recordSkillProvenance("", h)
	if checkSkillCodeProvenance("", h) {
		t.Fatal("empty session must fail closed (no shared bucket)")
	}
	recordSkillProvenance("sess-m6-a", h)
	if !checkSkillCodeProvenance("sess-m6-a", h) {
		t.Fatal("same-session provenance must authorize")
	}
	if checkSkillCodeProvenance("sess-m6-b", h) {
		t.Fatal("cross-session provenance must deny")
	}
}
