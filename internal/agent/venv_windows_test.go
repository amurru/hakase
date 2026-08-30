//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetVenvPythonWindows exercises the real Windows venv bootstrap: py -3 /
// python probe order, Scripts/python.exe layout, script execution through the
// evaluated venv path, and pip present in Scripts (pip --version, no
// network).
func TestGetVenvPythonWindows(t *testing.T) {
	isolateHome(t)
	t.Chdir(t.TempDir())

	py, err := getVenvPython(nil)
	if err != nil {
		t.Skipf("python toolchain unavailable on this runner: %v", err)
	}

	want := filepath.Join(".venv", "Scripts", "python.exe")
	if py != want {
		t.Fatalf("getVenvPython: expected %q, got %q", want, py)
	}
	if _, err := os.Stat(py); err != nil {
		t.Fatalf("venv python missing after bootstrap: %v", err)
	}

	out, err := exec.Command(py, "-c", "print(1)").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "1") {
		t.Fatalf("venv python -c: %v (%s)", err, out)
	}

	pip := filepath.Join(".venv", "Scripts", "pip.exe")
	if _, err := os.Stat(pip); err != nil {
		t.Fatalf("venv pip missing: %v", err)
	}
	out, err = exec.Command(pip, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("pip --version: %v (%s)", err, out)
	}
}

// TestVenvCreatorCommandsOrder pins the fixed probe order on Windows:
// py before python, never python3.
func TestVenvCreatorCommandsOrder(t *testing.T) {
	cmds := venvCreatorCommands(".venv")
	if len(cmds) != 2 {
		t.Fatalf("expected 2 windows probes, got %v", cmds)
	}
	if cmds[0][0] != "py" || cmds[1][0] != "python" {
		t.Errorf("probe order must be py then python, got %v", cmds)
	}
	for _, c := range cmds {
		if c[0] == "python3" {
			t.Error("python3 must never be probed on Windows")
		}
	}
}
