// s27_test.go - pip exact-pin + require-hashes + .venv integrity (S27).
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
)

// strictVenvFixture builds a minimal ./.venv under dir (pyvenv.cfg
// present) and returns the workspace dir plus the interpreter's own venv
// binary (absolute), so the H1 single-root threads pyBin's venv. Callers
// pass both to gateAndInstallPip; tests that need cwd!=workspace chdir
// elsewhere after the fixture.
func strictVenvFixture(t *testing.T, hookBody string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pyBin := filepath.Join(venv, "bin", "python3")
	if hookBody != "" {
		if err := os.WriteFile(filepath.Join(venv, "lib", "sitecustomize.py"), []byte(hookBody), 0644); err != nil {
			if !os.IsNotExist(err) {
				if mk := os.MkdirAll(filepath.Join(venv, "lib"), 0755); mk != nil {
					t.Fatal(mk)
				}
				if err := os.WriteFile(filepath.Join(venv, "lib", "sitecustomize.py"), []byte(hookBody), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return dir, pyBin
}

// strictSB returns a strict-mode sandbox config with one pinned entry.
func strictSB(hashes map[string][]string) *sandbox.SandboxConfig {
	return &sandbox.SandboxConfig{
		AllowPipInstall: true,
		PipAllowlist:    []string{"requests==2.31.0"},
		PipStrictPins:   true,
		PipHashes:       hashes,
	}
}

// TestS27PinRequiresExact pins the == shape table: bare names, ranges,
// extras, URLs, markers and the installer itself refuse at parse time.
func TestS27PinRequiresExact(t *testing.T) {
	for _, tc := range []struct {
		entry string
		pin   bool // want hasPin=true
		ok    bool // want parse success
	}{
		{"requests==2.31.0", true, true},
		{"my_pkg==1.0a1", true, true},
		{"requests", false, true},
		{"requests>=2.0", false, false},
		{"requests~=2.0", false, false},
		{"requests[security]==2.0", false, false},
		{"requests; python_version>'3'", false, false},
		{"pkg@https://example.com/p.tar.gz", false, false},
		{"https://example.com/p.tar.gz", false, false},
		{"pip==23.0", false, false},
		{"pip", false, false},
		{"", false, false},
		{"requests== ", false, false},
		{"a/b==1.0", false, false},
		{strings.Repeat("a", 65) + "==1.0", false, false},
	} {
		name, ver, hasPin, err := splitPipPin(tc.entry)
		if (err == nil) != tc.ok {
			t.Errorf("splitPipPin(%q): err=%v, want ok=%v", tc.entry, err, tc.ok)
			continue
		}
		if err == nil && hasPin != tc.pin {
			t.Errorf("splitPipPin(%q): hasPin=%v, want %v", tc.entry, hasPin, tc.pin)
		}
		if err == nil && hasPin && (name == "" || ver == "") {
			t.Errorf("splitPipPin(%q): empty pin leg", tc.entry)
		}
	}
}

// TestS27HashesRequiredWhenConfigured pins require-hashes: with
// pip_hashes configured, a pinned package without recorded digests
// denies (no exec, no prompt), and a recorded package installs with
// --require-hashes argv through the choke.
func TestS27HashesRequiredWhenConfigured(t *testing.T) {
	wsDir, pyBin := strictVenvFixture(t, "")
	ctx := context.Background()

	t.Run("missing hashes deny", func(t *testing.T) {
		asked := installPipTestDeps(t, strictSB(map[string][]string{
			"other": {"ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"},
		}), func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
		_ = asked
		builds, _ := stubPipChoke(t, nil)
		if err := gateAndInstallPip(ctx, "requests", "sess-s27-1", pyBin, wsDir); err == nil {
			t.Fatal("recorded-hashes configured but package unrecorded: want denial")
		} else if !strings.Contains(err.Error(), "no recorded hashes") {
			t.Fatalf("denial must name recorded hashes, got: %v", err)
		}
		if *builds != 0 {
			t.Fatalf("denied install must not exec (builds=%d)", *builds)
		}
		if *asked != 0 {
			t.Fatalf("strict hash denial must not prompt (asked=%d)", *asked)
		}
	})

	t.Run("recorded hashes install with require-hashes argv", func(t *testing.T) {
		h := "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"
		var gotPrompt string
		installPipTestDeps(t, strictSB(map[string][]string{"requests": {h}}),
			func(req interfaces.ApprovalRequest) (bool, error) {
				gotPrompt = req.Command
				return true, nil
			})
		var gotArgs []string
		var gotPipBin, gotDir string
		savedBuild, savedRun := pipChokeBuild, startWaitChokeCmd
		pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
			gotArgs = args
			gotPipBin = pipBin
			gotDir = dir
			return nil, nil
		}
		startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error { return nil }
		t.Cleanup(func() { pipChokeBuild, startWaitChokeCmd = savedBuild, savedRun })
		if err := gateAndInstallPip(ctx, "requests", "sess-s27-2", pyBin, wsDir); err != nil {
			t.Fatalf("recorded package with approval: %v", err)
		}
		joined := strings.Join(gotArgs, " ")
		if !strings.Contains(joined, "--require-hashes") || !strings.Contains(joined, "--hash=sha256:"+h) || !strings.Contains(joined, "requests==2.31.0") {
			t.Fatalf("strict argv must pin + require hashes, got %q", gotArgs)
		}
		// H1 single root: the choke pip and the verify root agree, even
		// though the test process cwd is the package dir, not wsDir.
		if !filepath.IsAbs(gotPipBin) {
			t.Fatalf("choke pip must be absolute, got %q", gotPipBin)
		}
		if gotDir != wsDir {
			t.Fatalf("choke dir = %q, want workspace %q", gotDir, wsDir)
		}
		wantVenv := filepath.Join(wsDir, ".venv")
		gotVenv := filepath.Dir(filepath.Dir(gotPipBin))
		wantResolved, _ := filepath.EvalSymlinks(wantVenv)
		if wantResolved == "" {
			wantResolved = wantVenv
		}
		gotResolved, _ := filepath.EvalSymlinks(gotVenv)
		if gotResolved == "" {
			gotResolved = gotVenv
		}
		if gotResolved != wantResolved {
			t.Fatalf("pip venv root %q must equal workspace venv %q", gotResolved, wantResolved)
		}
		// M6: the human approves the exact pin with the hash count, not
		// a bare package name.
		if !strings.Contains(gotPrompt, "requests==2.31.0") || !strings.Contains(gotPrompt, "require-hashes:1") {
			t.Fatalf("strict prompt must show pin + hash count, got %q", gotPrompt)
		}
	})
}

// TestS27SingleVenvRootCwdDiverged pins H1: the venv verify and the choke
// spawn share one absolute root derived from the interpreter's own venv,
// even when the process cwd differs from the run workspace.
func TestS27SingleVenvRootCwdDiverged(t *testing.T) {
	wsDir, pyBin := strictVenvFixture(t, "")
	// Diverge cwd from the workspace: verify + choke must still agree on
	// the workspace .venv, never on cwd/.venv.
	other := t.TempDir()
	t.Chdir(other)
	h := "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"
	installPipTestDeps(t, strictSB(map[string][]string{"requests": {h}}),
		func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
	var gotPipBin string
	savedBuild, savedRun := pipChokeBuild, startWaitChokeCmd
	pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
		gotPipBin = pipBin
		return nil, nil
	}
	startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error { return nil }
	t.Cleanup(func() { pipChokeBuild, startWaitChokeCmd = savedBuild, savedRun })
	if err := gateAndInstallPip(context.Background(), "requests", "sess-s27-cwd", pyBin, wsDir); err != nil {
		t.Fatalf("cwd-diverged strict install: %v", err)
	}
	if !filepath.IsAbs(gotPipBin) {
		t.Fatalf("pip must be absolute, got %q", gotPipBin)
	}
	// The pip sibling of pyBin owns the install, not cwd/.venv.
	if filepath.Dir(gotPipBin) != filepath.Dir(pyBin) {
		t.Fatalf("pip dir %q must equal pyBin dir %q", filepath.Dir(gotPipBin), filepath.Dir(pyBin))
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".venv", "pyvenv.cfg")); err != nil {
		t.Fatalf("workspace venv must exist: %v", err)
	}
}

// TestS27TransitiveExtrasDenied pins the transitive leg: allowlist
// entries with extras refuse at match time, so unpinned transitive
// closures can never authorize.
func TestS27TransitiveExtrasDenied(t *testing.T) {
	sb := &sandbox.SandboxConfig{AllowPipInstall: true, PipAllowlist: []string{"requests[security]==2.31.0"}, PipStrictPins: true}
	if _, _, err := matchStrictAllowlist(sb, "requests"); err == nil {
		t.Fatal("extras entry must never match")
	}
	// Bare entry under strict mode denies with the pin rule named.
	sb2 := &sandbox.SandboxConfig{AllowPipInstall: true, PipAllowlist: []string{"requests"}, PipStrictPins: true}
	if _, _, err := matchStrictAllowlist(sb2, "requests"); err == nil || !strings.Contains(err.Error(), "exact == pin") {
		t.Fatalf("bare entry under strict must demand a pin, got: %v", err)
	}
}

// TestS27SitecustomizeRogueDenied pins the import-hook leg: an
// untrusted sitecustomize.py in .venv denies the install; a trusted
// digest (or no hook file) passes the venv gate.
func TestS27SitecustomizeRogueDenied(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	if err := os.MkdirAll(filepath.Join(venv, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hook := "import os; os.system('evil')\n"
	if err := os.WriteFile(filepath.Join(venv, "lib", "sitecustomize.py"), []byte(hook), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyVenvIntegrity(venv, nil); err == nil {
		t.Fatal("untrusted sitecustomize must deny")
	}
	// Trusted digest passes.
	sum := sha256.Sum256([]byte(hook))
	if err := verifyVenvIntegrity(venv, []string{hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("trusted hook must pass: %v", err)
	}
	// No hook file passes with empty trust set.
	os.Remove(filepath.Join(venv, "lib", "sitecustomize.py"))
	if err := verifyVenvIntegrity(venv, nil); err != nil {
		t.Fatalf("hook-free venv must pass: %v", err)
	}
}

// TestS27VenvIntegrityMissingDenied pins fail-closed venvs: a missing
// pyvenv.cfg (deleted or never-created venv) refuses the install.
func TestS27VenvIntegrityMissingDenied(t *testing.T) {
	dir := t.TempDir()
	if err := verifyVenvIntegrity(filepath.Join(dir, ".venv"), nil); err == nil {
		t.Fatal("missing venv must deny")
	} else if !strings.Contains(err.Error(), "pyvenv.cfg") {
		t.Fatalf("denial must name pyvenv.cfg, got: %v", err)
	}
	if err := verifyVenvIntegrity("", nil); err == nil {
		t.Fatal("empty venv root must deny")
	}
}

// TestS27NoV1BreakBareName pins no-v1-break: with strict pins off, bare
// names keep exact S18 behavior (preflight passes allowlisted names).
func TestS27NoV1BreakBareName(t *testing.T) {
	sb := &sandbox.SandboxConfig{AllowPipInstall: true, PipAllowlist: []string{"requests"}}
	if sb.PipStrictPins {
		t.Fatal("strict must default off")
	}
	if err := checkPipPreflight(sb, "requests"); err != nil {
		t.Fatalf("non-strict preflight must pass allowlisted bare name: %v", err)
	}
	if _, _, err := matchStrictAllowlist(sb, "requests"); err == nil {
		t.Fatal("non-strict bare entry must not satisfy the strict matcher")
	}
}
