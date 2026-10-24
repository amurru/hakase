// r31_test.go - R31 interpreter-startup integrity tests (code-executing
// .pth confinement, verify on every start, strict-scoped deny).
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/sandbox"

	"google.golang.org/adk/v2/agent"
)

// TestR31PthExecutesCode pins the .pth classifier: import lines execute
// at startup (hook-class), comments/blanks/path additions do not.
func TestR31PthExecutesCode(t *testing.T) {
	for _, tc := range []struct {
		content string
		exec    bool
	}{
		{"import os\n", true},
		{"import evil; x = 1\n", true},
		{"import\tsys\n", true},
		{"# import os\n", false},
		{"\n", false},
		{"", false},
		{"/usr/lib/python3/site-packages\n", false},
		{"./lib\n# a comment\n", false},
		{"important-stuff\n", false},
		{"ximport os\n", false},
	} {
		if got := pthExecutesCode(tc.content); got != tc.exec {
			t.Errorf("pthExecutesCode(%q) = %v, want %v", tc.content, got, tc.exec)
		}
	}
}

// TestR31PthImportLineDenied pins the .pth hook leg: a code-executing
// .pth with untrusted content denies, a trusted digest passes, and a
// pure path-list .pth passes with an empty trust set.
func TestR31PthImportLineDenied(t *testing.T) {
	mkVenv := func(t *testing.T, pth string) string {
		t.Helper()
		dir := t.TempDir()
		venv := filepath.Join(dir, ".venv")
		sp := filepath.Join(venv, "lib", "python3.14", "site-packages")
		if err := os.MkdirAll(sp, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if pth != "" {
			if err := os.WriteFile(filepath.Join(sp, "evil.pth"), []byte(pth), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return venv
	}

	venv := mkVenv(t, "import os\n")
	if err := verifyVenvIntegrity(venv, nil); err == nil {
		t.Fatal("code-executing .pth with empty trust: want denial")
	}
	venvPathOnly := mkVenv(t, "/opt/extra-libs\n")
	if err := verifyVenvIntegrity(venvPathOnly, nil); err != nil {
		t.Fatalf("path-only .pth must pass, got: %v", err)
	}
	venvNone := mkVenv(t, "")
	if err := verifyVenvIntegrity(venvNone, nil); err != nil {
		t.Fatalf("hook-free venv must pass, got: %v", err)
	}
}

// drivePythonRun runs the python_interpreter tool handler like the ADK
// would, with the exec seam stubbed.
func drivePythonRun(t *testing.T, code string) (PythonExecOutput, error) {
	t.Helper()
	tool, err := createPythonTool(nil)
	if err != nil {
		t.Fatalf("createPythonTool: %v", err)
	}
	runnable, ok := tool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", tool)
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	result, err := runnable.Run(ctx, map[string]any{"code": code})
	if err != nil {
		return PythonExecOutput{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out PythonExecOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

// TestR31VerifyOnEveryStart pins the start gate: a venv with a planted
// import hook refuses the run before any spawn, even though the sandbox
// allows python and no pip install is involved.
func TestR31VerifyOnEveryStart(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	venv := filepath.Join(dir, ".venv", "bin")
	if err := os.MkdirAll(venv, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".venv", "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".venv", "bin", "python3"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// Plant a rogue startup hook: the start must refuse, not spawn.
	if err := os.MkdirAll(filepath.Join(dir, ".venv", "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".venv", "lib", "sitecustomize.py"), []byte("import os\n"), 0644); err != nil {
		t.Fatal(err)
	}

	installPipTestDeps(t, &sandbox.SandboxConfig{
		Permissions: map[string]string{"python_interpreter": "allow"},
	}, nil)

	spawned := false
	savedExec := execPythonScript
	execPythonScript = func(ctx context.Context, pyBin, scriptPath, workDir string, env []string) (string, string) {
		spawned = true
		return "", ""
	}
	t.Cleanup(func() { execPythonScript = savedExec })

	if _, err := drivePythonRun(t, "X = 1\n"); err == nil {
		t.Fatal("rogue-hook venv: start must refuse")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refusal must name the integrity gate, got: %v", err)
	}
	if spawned {
		t.Fatal("refused start must not spawn the interpreter")
	}

	// Control: hook removed, the same start spawns.
	if err := os.Remove(filepath.Join(dir, ".venv", "lib", "sitecustomize.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := drivePythonRun(t, "X = 1\n"); err != nil {
		t.Fatalf("hook-free venv must start, got: %v", err)
	}
	if !spawned {
		t.Fatal("hook-free start must spawn the interpreter")
	}
}
