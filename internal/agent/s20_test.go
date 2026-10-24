// s20_test.go - scrubbed python child env + skill loader tests (S20).
package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// childEnvMap renders ["K=V", ...] as a lookup map for assertions.
func childEnvMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// TestS20EnvScrubDropsPythonPath pins the scrub: even with a poisoned
// parent env, the child carries no PYTHONPATH-family key at all (absent,
// not empty).
func TestS20EnvScrubDropsPythonPath(t *testing.T) {
	t.Setenv("PYTHONPATH", "/evil:./skills")
	t.Setenv("PYTHONHOME", "/evil")
	t.Setenv("PYTHONSTARTUP", "/evil/startup.py")
	t.Setenv("__PYVENV_LAUNCHER__", "/evil/pyvenv.cfg")
	m := childEnvMap(buildPythonChildEnv("/v/bin/python3"))
	for _, k := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "__PYVENV_LAUNCHER__"} {
		if _, ok := m[k]; ok {
			t.Errorf("child env must not carry %s", k)
		}
	}
}

// TestS20NoParentEnvMerge pins no ambient inheritance: ambient secrets and
// junk never reach the child; only the minimal PATH plus the explicit
// allowlist do.
func TestS20NoParentEnvMerge(t *testing.T) {
	t.Setenv("EVIL", "1")
	t.Setenv("HAKASE_API_KEY", "sk-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	m := childEnvMap(buildPythonChildEnv("/v/bin/python3"))
	for _, k := range []string{"EVIL", "HAKASE_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := m[k]; ok {
			t.Errorf("child env must not inherit ambient %s", k)
		}
	}
	path, ok := m["PATH"]
	if !ok || path == "" {
		t.Fatal("child env must carry a minimal PATH")
	}
	if !strings.HasPrefix(path, filepath.Dir("/v/bin/python3")) {
		t.Errorf("child PATH must start with the venv bin dir, got %q", path)
	}
	if strings.Contains(path, ".") && runtime.GOOS != "windows" {
		for _, leg := range strings.Split(path, string(os.PathListSeparator)) {
			if leg == "." || leg == "./skills" {
				t.Errorf("child PATH must not contain search-path legs, got %q", path)
			}
		}
	}
	// Allowlisted keys still pass through when present.
	t.Setenv("LANG", "C.UTF-8")
	if m := childEnvMap(buildPythonChildEnv("/v/bin/python3")); m["LANG"] != "C.UTF-8" {
		t.Error("allowlisted LANG must pass through")
	}
}

// writeSkillFixture writes dir/skills/<name>.py and a script importing it
// through the new loader, returning the script path.
func writeSkillFixture(t *testing.T, skillName, skillBody, scriptBody string) (scriptPath, scriptDir, skillsDir string) {
	t.Helper()
	scriptDir = t.TempDir()
	skillsDir = filepath.Join(scriptDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "__init__.py"), []byte("# pkg\n"), 0644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, skillName+".py"), []byte(skillBody), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	scriptPath = filepath.Join(scriptDir, "script.py")
	if err := writePythonScript(scriptPath, scriptBody, scriptDir, skillsDir); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return scriptPath, scriptDir, skillsDir
}

// runScrubbed runs a script with the scrubbed child env (no PYTHONPATH).
// It skips when no python3 is available (hermetic CI without python).
func runScrubbed(t *testing.T, scriptPath string) (string, error) {
	t.Helper()
	pyBin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	cmd := exec.Command(pyBin, scriptPath)
	cmd.Env = buildPythonChildEnv(pyBin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestS20SkillLoadsByPath pins the loader replacement: a script importing
// a skill by the new absolute-path loader succeeds with no PYTHONPATH in
// the child env.
func TestS20SkillLoadsByPath(t *testing.T) {
	scriptPath, _, _ := writeSkillFixture(t, "greet",
		"VALUE = 42\n",
		"from skills.greet import VALUE\nprint('loader-ok:', VALUE)\n")
	out, err := runScrubbed(t, scriptPath)
	if err != nil {
		t.Fatalf("loader script failed (PYTHONPATH absent): %v\n%s", err, out)
	}
	if !strings.Contains(out, "loader-ok: 42") {
		t.Fatalf("loader script must print the skill value, got %q", out)
	}
}

// TestS20ShadowingFails pins the shadowing fix: a ./skills/os.py fixture
// no longer shadows stdlib os, because sys.path holds only the script dir
// and the finder answers just the skills top-level name.
func TestS20ShadowingFails(t *testing.T) {
	scriptPath, _, _ := writeSkillFixture(t, "os",
		"raise RuntimeError('shadowed: skills/os.py imported as os')\n",
		"import os\nprint('stdlib-ok:' + os.name + ':' + str(hasattr(os, 'path')))\n")
	out, err := runScrubbed(t, scriptPath)
	if err != nil {
		t.Fatalf("stdlib import must win over skills/os.py: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stdlib-ok:") {
		t.Fatalf("expected stdlib os, got %q", out)
	}
}
