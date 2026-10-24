// exec_replace_env_test.go - ExecReplaceEnv + S20 scrub coverage (S22/S23).
//
// ExecReplaceEnv must be honored by both buildExecCommand branches (bwrap
// and plain): the child env is built from the replace set plus the
// per-call overlay only, with no ambient inheritance, then scrubbed.
// ScrubEnv always drops the exact S20 PYTHONPATH-family keys after the
// merge (case-insensitive on Windows).
package sandbox

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// allowGate installs an allow-all gate + silent audit for build tests.
func allowGate(t *testing.T) {
	t.Helper()
	savedGate, savedAudit := EvaluateCommandFunc, AuditCommandFunc
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	AuditCommandFunc = func(CommandAuditEntry) {}
	t.Cleanup(func() { EvaluateCommandFunc, AuditCommandFunc = savedGate, savedAudit })
}

func envKeys(env []string) map[string]bool {
	out := make(map[string]bool, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		out[k] = true
	}
	return out
}

// TestScrubEnvDropsPythonPathFamily pins the S20 exact 4-key scrub list:
// poisoned PYTHONPATH-family keys never survive, even when the overlay
// sets them explicitly (scrub runs after the merge).
func TestScrubEnvDropsPythonPathFamily(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/u",
		"PYTHONPATH=/evil:./skills",
		"PYTHONHOME=/evil",
		"PYTHONSTARTUP=/evil/startup.py",
		"__PYVENV_LAUNCHER__=/evil/pyvenv.cfg",
		"HAKASE_TOKEN=secret",
	}
	got := envKeys(ScrubEnv(env))
	for _, bad := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "__PYVENV_LAUNCHER__", "HAKASE_TOKEN"} {
		if got[bad] {
			t.Errorf("ScrubEnv kept %q, want absent", bad)
		}
	}
	for _, want := range []string{"PATH", "HOME"} {
		if !got[want] {
			t.Errorf("ScrubEnv dropped legitimate key %q", want)
		}
	}
}

// TestExecReplaceEnvPlainBranch pins the plain branch: ambient env
// (including a poisoned PYTHONPATH) is not inherited, the replace set
// plus overlay is the whole child env, and the scrub still applies.
func TestExecReplaceEnvPlainBranch(t *testing.T) {
	allowGate(t)
	t.Setenv("S22_TEST_AMBIENT_LEAK", "leaked")
	t.Setenv("PYTHONPATH", "/evil-ambient")

	replace := map[string]string{"PATH": "/venv/bin:/usr/bin:/bin", "LANG": "C.UTF-8"}
	overlay := map[string]string{"PIP_ONLY": "1", "PYTHONPATH": "/evil-overlay"}

	cmd, err := buildExecCommand(context.Background(), nil, "sess", "echo hi", nil, "", overlay, ExecReplaceEnv(replace))
	if err != nil {
		t.Fatalf("buildExecCommand(replace): %v", err)
	}
	keys := envKeys(cmd.Env)
	if keys["S22_TEST_AMBIENT_LEAK"] {
		t.Error("replace env inherited ambient S22_TEST_AMBIENT_LEAK")
	}
	if keys["PYTHONPATH"] {
		t.Error("replace env kept PYTHONPATH from ambient/overlay, want scrubbed")
	}
	for _, want := range []string{"PATH", "LANG", "PIP_ONLY"} {
		if !keys[want] {
			t.Errorf("replace env missing %q: %v", want, cmd.Env)
		}
	}
}

// TestExecReplaceEnvDefaultMergesAmbient is the control: without
// ExecReplaceEnv the ambient env is still inherited with the overlay.
func TestExecReplaceEnvDefaultMergesAmbient(t *testing.T) {
	allowGate(t)
	t.Setenv("S22_TEST_AMBIENT_OK", "present")

	cmd, err := buildExecCommand(context.Background(), nil, "sess", "echo hi", nil, "", map[string]string{"S22_OVERLAY": "1"})
	if err != nil {
		t.Fatalf("buildExecCommand: %v", err)
	}
	keys := envKeys(cmd.Env)
	if !keys["S22_TEST_AMBIENT_OK"] || !keys["S22_OVERLAY"] {
		t.Errorf("default build must merge ambient + overlay: %v", cmd.Env)
	}
}

// TestBuildEnvListNoDuplicateKeys pins overlay-wins dedupe (pip passes
// the same map as both replace set and overlay): a shadowed key appears
// exactly once with the overlay value, and ambient keys shadowed by the
// overlay are dropped the same way (exec passes duplicates through and
// the child reads whichever libc prefers).
func TestBuildEnvListNoDuplicateKeys(t *testing.T) {
	bo := &execOptions{replaceEnv: map[string]string{"PATH": "/replace", "KEEP": "1"}, useReplaceEnv: true}
	got := buildEnvList(map[string]string{"PATH": "/overlay"}, bo)
	paths, keeps := 0, 0
	for _, kv := range got {
		if k, v, ok := strings.Cut(kv, "="); ok {
			switch k {
			case "PATH":
				paths++
				if v != "/overlay" {
					t.Errorf("PATH = %q, want overlay value /overlay", v)
				}
			case "KEEP":
				keeps++
			}
		}
	}
	if paths != 1 {
		t.Errorf("PATH appears %d times, want exactly 1: %v", paths, got)
	}
	if keeps != 1 {
		t.Errorf("KEEP appears %d times, want exactly 1: %v", keeps, got)
	}

	t.Setenv("S23_TEST_DUP", "ambient")
	got = buildEnvList(map[string]string{"S23_TEST_DUP": "overlay"}, &execOptions{})
	vals := map[string]int{}
	for _, kv := range got {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "S23_TEST_DUP" {
			vals[v]++
		}
	}
	if len(vals) != 1 || vals["overlay"] != 1 {
		t.Errorf("ambient/overlay dup must collapse to the overlay value, got %v", vals)
	}
}

// TestIsPythonScrubbedKeyWindowsCase pins the Windows case-insensitive
// scrub matching. It runs on every platform: the matcher takes the
// case-insensitivity flag directly (only ScrubEnv reads runtime.GOOS),
// so the Windows folding is pinned without a Windows runner.
func TestIsPythonScrubbedKeyWindowsCase(t *testing.T) {
	for _, k := range []string{"pythonpath", "PythonPath", "PYTHONHOME", "PythonStartup", "__pyvenv_launcher__", "__PYVENV_LAUNCHER__"} {
		if !isPythonScrubbedKey(k, true) {
			t.Errorf("isPythonScrubbedKey(%q, windows) = false, want true", k)
		}
	}
	if isPythonScrubbedKey("PATH", true) {
		t.Error("isPythonScrubbedKey(PATH, windows) = true, want false")
	}
	// Linux stays byte-exact: lowercase never matches.
	if isPythonScrubbedKey("pythonpath", false) {
		t.Error("isPythonScrubbedKey(pythonpath, linux) = true, want false (byte-exact)")
	}
	if !isPythonScrubbedKey("PYTHONPATH", false) {
		t.Error("isPythonScrubbedKey(PYTHONPATH, linux) = false, want true")
	}
}

// TestExecReplaceEnvBwrapBranch pins the bwrap branch: with bubblewrap

// mode the replace env is honored too. When bwrap is missing the build
// fails loud through the fallback audit onto the plain path, which
// shares the same buildEnvList helper - either way no ambient key and
// no PYTHONPATH-family key may survive.
func TestExecReplaceEnvBwrapBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: bubblewrap mode is coerced to paths on Windows")
	}
	allowGate(t)
	forceBwrapMissing(t)
	savedNotice := SandboxNoticeFunc
	SandboxNoticeFunc = func(sessionID, msg string) {}
	t.Cleanup(func() { SandboxNoticeFunc = savedNotice })

	t.Setenv("S22_TEST_AMBIENT_LEAK", "leaked")
	t.Setenv("PYTHONPATH", "/evil-ambient")

	dir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{dir},
		ReadRoots:      []string{dir},
		AllowFallback:  true,
	}
	replace := map[string]string{"PATH": "/venv/bin:/usr/bin:/bin"}
	overlay := map[string]string{"PIP_ONLY": "1", "PYTHONHOME": "/evil-overlay"}

	cmd, err := buildExecCommand(WithConfig(context.Background(), sb), sb, "sess", "echo hi", nil, dir, overlay, ExecReplaceEnv(replace))
	if err != nil {
		t.Fatalf("bwrap build (fallback) with replace env: %v", err)
	}
	keys := envKeys(cmd.Env)
	if keys["S22_TEST_AMBIENT_LEAK"] {
		t.Error("bwrap-branch replace env inherited ambient leak")
	}
	if keys["PYTHONPATH"] || keys["PYTHONHOME"] {
		t.Errorf("bwrap-branch replace env kept PYTHONPATH-family keys: %v", cmd.Env)
	}
	if !keys["PATH"] || !keys["PIP_ONLY"] {
		t.Errorf("bwrap-branch replace env missing replace/overlay keys: %v", cmd.Env)
	}
	var _ = os.Getenv
}
