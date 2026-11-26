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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
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

	cmd, err := buildExecCommand(context.Background(), nil, "sess", "echo hi", nil, t.TempDir(), overlay, ExecReplaceEnv(replace))
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

	cmd, err := buildExecCommand(context.Background(), nil, "sess", "echo hi", nil, t.TempDir(), map[string]string{"S22_OVERLAY": "1"})
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

// TestScrubEnvDropsGitExecKeys pins the git exec-hook scrub list:
// arbitrary config injection (COUNT/PARAMETERS/KEY_*/VALUE_*), the
// external diff driver, pagers, ssh commands, and askpass helpers never
// survive, even when the overlay sets them explicitly. Whole-file
// selectors (GIT_CONFIG_GLOBAL/SYSTEM) and ordinary keys stay: the
// engine and the harness legitimately point them at owned configs.
func TestScrubEnvDropsGitExecKeys(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL=/home/u/.gitconfig",
		"GIT_CONFIG_SYSTEM=/etc/gitconfig",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_PARAMETERS=core.pager=id",
		"GIT_CONFIG_KEY_0=core.pager",
		"GIT_CONFIG_VALUE_0=id",
		"GIT_EXTERNAL_DIFF=evil",
		"GIT_PAGER=evil",
		"GIT_SSH=evil",
		"GIT_SSH_COMMAND=evil",
		"GIT_ASKPASS=evil",
	}
	got := envKeys(ScrubEnv(env))
	for _, bad := range []string{
		"GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
		"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_SSH",
		"GIT_SSH_COMMAND", "GIT_ASKPASS",
	} {
		if got[bad] {
			t.Errorf("ScrubEnv kept %q, want absent", bad)
		}
	}
	for _, want := range []string{"PATH", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if !got[want] {
			t.Errorf("ScrubEnv dropped legitimate key %q", want)
		}
	}
}

// TestScrubEnvDropsToolOptionKeys pins the option-injection scrub list:
// make/tar parse TAR_OPTIONS/MAKEFLAGS/GNUMAKEFLAGS as pre-options and
// MAKEFILES as extra makefiles before argv, so an ambient value could add a
// hidden -C, an exec flag, or an uninspected makefile the gate never sees.
// They never survive, even when the overlay sets them explicitly; ordinary
// keys stay.
func TestScrubEnvDropsToolOptionKeys(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"TAR_OPTIONS=--to-command=id",
		"MAKEFLAGS=-C /hook",
		"GNUMAKEFLAGS=--directory=/hook",
		"MAKEFILES=/tmp/evil.mk",
		"MAKELEVEL=1",
	}
	got := envKeys(ScrubEnv(env))
	for _, bad := range []string{"TAR_OPTIONS", "MAKEFLAGS", "GNUMAKEFLAGS", "MAKEFILES"} {
		if got[bad] {
			t.Errorf("ScrubEnv kept %q, want absent", bad)
		}
	}
	for _, want := range []string{"PATH", "MAKELEVEL"} {
		if !got[want] {
			t.Errorf("ScrubEnv dropped legitimate key %q", want)
		}
	}
	for _, k := range []string{"tar_options", "MakeFlags", "gnumakeflags", "makefiles"} {
		if !isToolOptionScrubbedKey(k, true) {
			t.Errorf("isToolOptionScrubbedKey(%q, windows) = false, want true", k)
		}
		if isToolOptionScrubbedKey(k, false) {
			t.Errorf("isToolOptionScrubbedKey(%q, linux) = true, want false (byte-exact)", k)
		}
	}
	if !isToolOptionScrubbedKey("TAR_OPTIONS", false) {
		t.Error("isToolOptionScrubbedKey(TAR_OPTIONS, linux) = false, want true")
	}

	// The overlay claim is asserted where it takes effect: buildEnvList
	// merges replace+overlay and only then scrubs, so an explicitly set
	// overlay value never reaches the child either.
	bo := &execOptions{replaceEnv: map[string]string{"PATH": "/usr/bin:/bin"}, useReplaceEnv: true}
	merged := envKeys(buildEnvList(map[string]string{
		"TAR_OPTIONS":  "--to-command=id",
		"MAKEFLAGS":    "-C /hook",
		"GNUMAKEFLAGS": "-f evil.mk",
		"MAKEFILES":    "evil.mk",
	}, bo))
	for _, bad := range []string{"TAR_OPTIONS", "MAKEFLAGS", "GNUMAKEFLAGS", "MAKEFILES"} {
		if merged[bad] {
			t.Errorf("buildEnvList kept overlay %q, want absent", bad)
		}
	}
	if !merged["PATH"] {
		t.Error("buildEnvList dropped replace PATH")
	}
}

// TestIsGitScrubbedKeyWindowsCase pins the Windows case-insensitive
// folding. It runs on every platform: the matcher takes the
// case-insensitivity flag directly (only ScrubEnv reads runtime.GOOS).
func TestIsGitScrubbedKeyWindowsCase(t *testing.T) {
	for _, k := range []string{"git_config_count", "Git_External_Diff", "GIT_PAGER", "git_config_key_0"} {
		if !isGitScrubbedKey(k, true) {
			t.Errorf("isGitScrubbedKey(%q, windows) = false, want true", k)
		}
	}
	if isGitScrubbedKey("GIT_CONFIG_GLOBAL", true) {
		t.Error("isGitScrubbedKey(GIT_CONFIG_GLOBAL, windows) = true, want false (file selectors stay)")
	}
	if isGitScrubbedKey("git_config_count", false) {
		t.Error("isGitScrubbedKey(git_config_count, linux) = true, want false (byte-exact)")
	}
	if !isGitScrubbedKey("GIT_CONFIG_COUNT", false) {
		t.Error("isGitScrubbedKey(GIT_CONFIG_COUNT, linux) = false, want true")
	}
}

// TestGitPreGateWindowsCaseFolding pins Windows case-insensitive
// pre-gate classification. The matchers take the case-insensitivity flag
// directly (only auditGitPreGate reads runtime.GOOS), mirroring the
// isGitScrubbedKey folding: folded exec-hook and selector names must
// classify exactly like their canonical spellings, because git reads
// folded names on Windows.
func TestGitPreGateWindowsCaseFolding(t *testing.T) {
	// Deny leg folds: lowercase exec-hook names deny on Windows.
	for _, kv := range []struct{ key, val string }{
		{"git_pager", "evil"},
		{"git_ssh_command", "evil"},
		{"git_external_diff", "evil"},
		{"git_askpass", "evil"},
		{"git_config_count", "1"},
		{"git_config_parameters", "core.pager=evil"},
		{"git_config_key_0", "core.pager"},
		{"git_config_value_0", "evil"},
		{"git_editor", "evil"},
	} {
		if !gitPreGateDeniedKey(kv.key, kv.val, true) {
			t.Errorf("gitPreGateDeniedKey(%q, windows) = false, want true", kv.key)
		}
	}
	// ...but stays byte-exact on Linux.
	if gitPreGateDeniedKey("git_pager", "evil", false) {
		t.Error("gitPreGateDeniedKey(git_pager, linux) = true, want false (byte-exact)")
	}
	if gitPreGateDeniedKey("git_config_global", "/tmp/x", false) {
		t.Error("gitPreGateDeniedKey(git_config_global, linux) = true, want false (selectors ask, never deny)")
	}
	// Skip leg folds: sentinel editor and fail-fast prompts skip either case.
	for _, kv := range []struct{ key, val string }{
		{"git_editor", "true"},
		{"git_editor", "false"},
		{"git_terminal_prompt", "0"},
		{"GIT_TERMINAL_PROMPT", "1"},
	} {
		if !gitPreGateSkippedKey(kv.key, kv.val, true) {
			t.Errorf("gitPreGateSkippedKey(%q=%q, windows) = false, want true", kv.key, kv.val)
		}
	}
	// A non-sentinel folded editor is an executable hook, never skipped.
	if gitPreGateSkippedKey("git_editor", "evil", true) {
		t.Error("gitPreGateSkippedKey(git_editor=evil, windows) = true, want false (executable hook)")
	}
	// Ask-namespace prefix folds: a lowercase selector is a GIT_
	// override on Windows, invisible on Linux.
	if !gitEnvHasGitPrefix("git_config_global", true) {
		t.Error("gitEnvHasGitPrefix(git_config_global, windows) = false, want true")
	}
	if gitEnvHasGitPrefix("git_config_global", false) {
		t.Error("gitEnvHasGitPrefix(git_config_global, linux) = true, want false (byte-exact)")
	}
	if !gitEnvHasGitPrefix("GIT_TRACE", false) {
		t.Error("gitEnvHasGitPrefix(GIT_TRACE, linux) = false, want true")
	}
}

// gitPreGateTestStubs installs an allow-all gate, a caller-controlled
// approval stub, and a silent audit for pre-gate tests. Ambient GIT_*
// keys are cleared first so host leakage (or a parallel-ish outer
// t.Setenv, including the engine's own GIT_CONFIG_GLOBAL identity
// pointer) can never flip a silent-build assertion into an Ask.
func gitPreGateTestStubs(t *testing.T, approve func() (bool, error), seen *[]string) {
	t.Helper()
	clearAmbientGitEnv(t)
	savedGate, savedAudit, savedApprove, savedExpiry := EvaluateCommandFunc, AuditCommandFunc, ApproveFunc, ApprovalExpiryFunc
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	AuditCommandFunc = func(CommandAuditEntry) {}
	ApproveFunc = func(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) {
		*seen = append(*seen, req.Reason)
		return approve()
	}
	ApprovalExpiryFunc = func() time.Duration { return time.Minute }
	t.Cleanup(func() {
		EvaluateCommandFunc, AuditCommandFunc, ApproveFunc, ApprovalExpiryFunc = savedGate, savedAudit, savedApprove, savedExpiry
	})
}

// clearAmbientGitEnv removes ambient GIT_* keys for the duration of a
// test so pre-gate assertions observe only the overlay/replace inputs
// they pass explicitly. Saved values are restored on cleanup.
func clearAmbientGitEnv(t *testing.T) {
	t.Helper()
	caseInsensitive := runtime.GOOS == "windows"
	var saved []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && gitEnvHasGitPrefix(k, caseInsensitive) {
			saved = append(saved, kv)
			_ = os.Unsetenv(k)
		}
	}
	t.Cleanup(func() {
		for _, kv := range saved {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
	})
}

// TestGitPreGateDeniesExecHookEnv pins the pre-gate deny: an env overlay
// injecting command execution into git refuses the build even though the
// gate allowed it.
func TestGitPreGateDeniesExecHookEnv(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	for _, overlay := range []map[string]string{
		{"GIT_EXTERNAL_DIFF": "evil"},
		{"GIT_PAGER": "evil"},
		{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.pager", "GIT_CONFIG_VALUE_0": "evil"},
		{"GIT_CONFIG_PARAMETERS": "core.pager=evil"},
		{"GIT_SSH_COMMAND": "evil"},
	} {
		_, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, overlay)
		if err == nil || !strings.Contains(err.Error(), "denied by protection policy") {
			t.Errorf("git status overlay %v: want protection-policy denial, got %v", overlay, err)
		}
	}
	if len(seen) != 0 {
		t.Errorf("deny path must not prompt for approval, got %d prompts", len(seen))
	}
}

// TestGitPreGateAsksConfigInfluence pins the pre-gate ask: other GIT_*
// overrides convert a gate Allow into an approval prompt (never silent
// Allow), and approval carries the build through.
func TestGitPreGateAsksConfigInfluence(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{"GIT_TRACE": "1"}, ExecReplaceEnv(replace)); err != nil {
		t.Errorf("approved GIT_TRACE overlay should build, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("want exactly 1 approval prompt, got %d", len(seen))
	}
	if !strings.Contains(seen[0], "GIT_TRACE") {
		t.Errorf("approval reason should name the override, got %q", seen[0])
	}
}

// TestGitPreGateAskReasonDeterministic pins the Ask reason shape: every
// offending key is named, sorted and joined, so map iteration order
// never leaks into the approval prompt or audit trail.
func TestGitPreGateAskReasonDeterministic(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	overlay := map[string]string{"GIT_ZZZ": "1", "GIT_AAA": "1", "GIT_MID": "1"}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, overlay, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("approved overlay should build, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("want exactly 1 approval prompt, got %d", len(seen))
	}
	want := "git environment overrides require user approval: GIT_AAA, GIT_MID, GIT_ZZZ"
	if seen[0] != want {
		t.Errorf("approval reason = %q, want %q", seen[0], want)
	}
}

// TestGitPreGateAsksWorkdirExecConfig pins the working_dir audit: a repo
// config with exec hooks converts a gate Allow into approval; a repo
// config without exec hooks stays silent.
func TestGitPreGateAsksWorkdirExecConfig(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	evil := t.TempDir()
	if err := os.MkdirAll(filepath.Join(evil, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, ".git", "config"), []byte("[core]\n\tpager = evil-pager\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, evil, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Errorf("approved workdir exec config should build, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("want exactly 1 approval prompt, got %d", len(seen))
	}

	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, ".git", "config"), []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen = nil
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, clean, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Errorf("clean workdir config should build silently, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("clean workdir config must not prompt, got %v", seen)
	}
}

// TestGitPreGateSkipsNonGit pins that the enforcement only applies to
// git-led builds: other commands with GIT_* in the overlay are untouched.
func TestGitPreGateSkipsNonGit(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "ls", nil, wd, map[string]string{"GIT_TRACE": "1"}, ExecReplaceEnv(replace)); err != nil {
		t.Errorf("non-git build with GIT_TRACE should not be affected, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("non-git build must not prompt, got %v", seen)
	}
}

// TestGitPreGateSelectorOverlayAsksNonGitLed pins the hoisted selector
// leg: a whole-file selector (GIT_CONFIG_GLOBAL) in the overlay always
// asks, even when a shell trampoline ("sh -c 'git ...'") hides the
// git-led shape - the spawned git still reads the pointed file.
func TestGitPreGateSelectorOverlayAsksNonGitLed(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	evil := filepath.Join(wd, "evil.cfg")
	if err := os.WriteFile(evil, []byte("[core]\n\tpager = evil-pager\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := map[string]string{"GIT_CONFIG_GLOBAL": evil}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "sh", []string{"-c", "git x"}, wd, overlay); err != nil {
		t.Fatalf("approved selector overlay should build, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("selector overlay behind sh -c must prompt (want 1, got %d): %v", len(seen), seen)
	}
	if !strings.Contains(seen[0], "GIT_CONFIG_GLOBAL") {
		t.Errorf("approval reason should name the selector, got %q", seen[0])
	}

	// Shell-form trampolines hide git-led-ness the same way.
	seen = nil
	if _, err := buildExecCommand(context.Background(), nil, "sess", `sh -c "git x"`, nil, wd, overlay); err != nil {
		t.Fatalf("approved shell-form selector overlay should build, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("shell-form selector overlay must prompt (want 1, got %d): %v", len(seen), seen)
	}

	// Without the selector the same trampoline stays silent.
	seen = nil
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "sh", []string{"-c", "git x"}, wd, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("selector-free trampoline should build, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("selector-free trampoline must not prompt, got %v", seen)
	}
}

// TestScrubEnvValueAwareEditor pins the GIT_EDITOR scrub: the engine's
// own non-interactive sentinel (true/false) passes through to the child;
// any other value names an executable editor hook and is scrubbed absent,
// exactly like the other exec-hook keys. GIT_TERMINAL_PROMPT always stays.
func TestScrubEnvValueAwareEditor(t *testing.T) {
	envVal := func(env []string, key string) (string, bool) {
		for _, kv := range ScrubEnv(env) {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v, true
			}
		}
		return "", false
	}
	if v, ok := envVal([]string{"GIT_EDITOR=true"}, "GIT_EDITOR"); !ok || v != "true" {
		t.Errorf("ScrubEnv dropped engine GIT_EDITOR=true, want kept (got %q, %v)", v, ok)
	}
	if v, ok := envVal([]string{"GIT_EDITOR=false"}, "GIT_EDITOR"); !ok || v != "false" {
		t.Errorf("ScrubEnv dropped engine GIT_EDITOR=false, want kept (got %q, %v)", v, ok)
	}
	for _, kv := range []string{"GIT_EDITOR=evil", "GIT_EDITOR="} {
		if _, ok := envVal([]string{kv}, "GIT_EDITOR"); ok {
			t.Errorf("ScrubEnv kept %q, want absent (executable editor hook)", kv)
		}
	}
	if v, ok := envVal([]string{"GIT_TERMINAL_PROMPT=0"}, "GIT_TERMINAL_PROMPT"); !ok || v != "0" {
		t.Errorf("ScrubEnv dropped GIT_TERMINAL_PROMPT=0, want kept (got %q, %v)", v, ok)
	}
}

// TestGitPreGateValueAwareEditor pins the pre-gate value-aware editor
// hook: a GIT_EDITOR overlay outside true/false denies the build, while
// the engine's own sentinel (and GIT_TERMINAL_PROMPT) builds silently.
func TestGitPreGateValueAwareEditor(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	wd := t.TempDir()
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{"GIT_EDITOR": "evil"}, ExecReplaceEnv(replace)); err == nil || !strings.Contains(err.Error(), "denied by protection policy") {
		t.Errorf("git status with GIT_EDITOR=evil overlay: want protection-policy denial, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("deny path must not prompt for approval, got %d prompts", len(seen))
	}
	for _, overlay := range []map[string]string{
		{"GIT_EDITOR": "true"},
		{"GIT_EDITOR": "false"},
		{"GIT_TERMINAL_PROMPT": "0"},
	} {
		seen = nil
		if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, overlay, ExecReplaceEnv(replace)); err != nil {
			t.Errorf("git status overlay %v: want silent build, got %v", overlay, err)
		}
		if len(seen) != 0 {
			t.Errorf("engine non-interactive overlay %v must not prompt, got %v", overlay, seen)
		}
	}
}

// TestGitPreGateAmbientScrubbedSilent pins the ambient scrub-parity rule:
// an ambient key ScrubEnv always removes (exec hooks like GIT_PAGER, a
// non-sentinel GIT_EDITOR) stays silent - never denies (the child cannot
// receive it) and never asks (approval could not restore it, so asking
// would prompt on every call with no effect). Ambient keys that survive
// the scrub still ask; those legs are pinned by
// TestGitPreGateAmbientSurvivorsAsk.
func TestGitPreGateAmbientScrubbedSilent(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	// Set after the stubs: the stub's ambient GIT_* clear runs first so
	// host leakage never interferes, then this test's own key is layered
	// on top.
	t.Setenv("GIT_PAGER", "cat")

	deny, ask := auditGitPreGate("git", []string{"status"}, t.TempDir(), map[string]string{}, &execOptions{}, nil)
	if deny != "" {
		t.Errorf("ambient GIT_PAGER must not deny, got %q", deny)
	}
	if ask != "" {
		t.Errorf("ambient GIT_PAGER must not ask (scrub removes it), got %q", ask)
	}

	wd := t.TempDir()
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{}); err != nil {
		t.Errorf("silent ambient GIT_PAGER should build, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("scrubbed ambient GIT_PAGER must not prompt, got %v", seen)
	}

	seen = nil
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{}, ExecOperatorAuthorized()); err != nil {
		t.Errorf("operator ambient GIT_PAGER should build without approval, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("operator run must not prompt, got %v", seen)
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
