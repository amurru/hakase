// choke_venv_env_test.go - High 1: venv launcher inherits no ambient env.
//
// chokeCreateVenv used to spawn with cmd.Env nil, so the launcher
// inherited the whole ambient process env (poisoned PYTHONPATH, ambient
// markers). It now carries the scrubbed S20 child env only, exactly like
// chokeRunPython.
package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readEnvDump parses an `env` dump file into a key map.
func readEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	m := make(map[string]string)
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

// assertVenvDumpScrubbed pins the High 1 contract on a launcher env dump:
// no PYTHONPATH-family key, no ambient marker, minimal PATH present.
func assertVenvDumpScrubbed(t *testing.T, dump map[string]string) {
	t.Helper()
	for _, k := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "__PYVENV_LAUNCHER__"} {
		if _, ok := dump[k]; ok {
			t.Errorf("venv launcher env must not carry %s", k)
		}
	}
	if _, ok := dump["CHOKE_VENV_AMBIENT_LEAK"]; ok {
		t.Error("venv launcher env must not inherit ambient markers")
	}
	path, ok := dump["PATH"]
	if !ok || path == "" {
		t.Fatal("venv launcher env must carry a minimal PATH")
	}
}

// TestChokeCreateVenvScrubbedEnv pins High 1: ambient PYTHONPATH and an
// ambient marker never reach the venv launcher child, in both the
// absolute-path form and the bare-name LookPath form.
func TestChokeCreateVenvScrubbedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell probe fixture")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.sh")
	dump := filepath.Join(dir, "env.dump")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nenv > \"$1\"\n"), 0755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	t.Setenv("PYTHONPATH", "/evil:./skills")
	t.Setenv("PYTHONHOME", "/evil")
	t.Setenv("CHOKE_VENV_AMBIENT_LEAK", "leaked")

	// Absolute-path form (no LookPath leg): the PATH head is the probe dir.
	if err := chokeCreateVenv([]string{probe, dump}); err != nil {
		t.Fatalf("chokeCreateVenv probe: %v", err)
	}
	m := readEnvDump(t, dump)
	assertVenvDumpScrubbed(t, m)
	if !strings.HasPrefix(m["PATH"], dir) {
		t.Errorf("venv launcher PATH must start with the launcher dir, got %q", m["PATH"])
	}

	// Bare-name form (LookPath leg): sh resolves via PATH, env still scrubbed.
	dump2 := filepath.Join(dir, "env2.dump")
	if err := chokeCreateVenv([]string{"sh", "-c", "env > " + dump2}); err != nil {
		t.Fatalf("chokeCreateVenv sh probe: %v", err)
	}
	assertVenvDumpScrubbed(t, readEnvDump(t, dump2))
}
