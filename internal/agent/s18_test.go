// s18_test.go - pip gate tests (S18).
package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
)

// installPipTestDeps wires deps (sandbox config) + a stub approval gate,
// restoring globals on cleanup. It returns the asked counter.
func installPipTestDeps(t *testing.T, sb *sandbox.SandboxConfig, approve func(req interfaces.ApprovalRequest) (bool, error)) *int {
	t.Helper()
	savedDeps, savedRt := deps, rt
	deps = &Deps{SandboxConfig: sb, ApprovalCfg: config.ApprovalConfig{Mode: "interactive"}}
	rt = &Runtime{}
	var asked int
	if approve != nil {
		rt.SetApprovalGate(&mockApprovalGate{approveFunc: func(req interfaces.ApprovalRequest) (bool, error) {
			asked++
			return approve(req)
		}})
	}
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
	return &asked
}

// stubPipChoke replaces the choke build/run seams with counters, restoring
// on cleanup. buildErr, when non-nil, fails the build (no exec attempted).
func stubPipChoke(t *testing.T, buildErr error) (builds, runs *int) {
	t.Helper()
	savedBuild, savedRun := pipChokeBuild, startWaitChokeCmd
	b, r := 0, 0
	pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
		b++
		if buildErr != nil {
			return nil, buildErr
		}
		return nil, nil
	}
	startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error {
		r++
		return nil
	}
	t.Cleanup(func() { pipChokeBuild, startWaitChokeCmd = savedBuild, savedRun })
	return &b, &r
}

// TestS18PipRejectsMalformedName pins the name-shape gate: flags, URLs,
// paths, whitespace and empty names refuse with no exec.
func TestS18PipRejectsMalformedName(t *testing.T) {
	mk := func(mod string) string {
		return "Traceback (most recent call last):\nModuleNotFoundError: No module named '" + mod + "'\n"
	}
	for _, tc := range []struct {
		mod string
		ok  bool
	}{
		{"requests", true},
		{"bs4.element", true}, // sub-modules take the first segment
		{"my_pkg-1.2", true},
		{"foo;rm -rf /", false},
		{"--index-url=http://evil", false},
		{"-e .", false},
		{"a/b", false},
		{"foo bar", false},
		{"", false},
		{"../evil", false},
		{strings.Repeat("a", 65), false},
	} {
		name, ok := extractMissingModule(mk(tc.mod))
		if ok != tc.ok {
			t.Errorf("extractMissingModule(%q): ok=%v, want %v", tc.mod, ok, tc.ok)
			continue
		}
		if ok && name == "" {
			t.Errorf("extractMissingModule(%q): empty name with ok=true", tc.mod)
		}
	}
	if _, ok := extractMissingModule("all imports fine\n"); ok {
		t.Error("clean output must not trigger an install")
	}
}

// TestS18PipAllowlistDenyClosed pins deny-by-default: an empty list denies
// everything and an unknown package denies even with a willing approver -
// and neither path prompts the human nor execs.
func TestS18PipAllowlistDenyClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown package", func(t *testing.T) {
		asked := installPipTestDeps(t, &sandbox.SandboxConfig{
			AllowPipInstall: true, PipAllowlist: []string{"requests"},
		}, func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
		builds, _ := stubPipChoke(t, nil)
		if err := gateAndInstallPip(ctx, "django", "sess-pip-1", "/v/bin/python3", ""); err == nil {
			t.Fatal("unknown package with approval=true: want denial")
		} else if !strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("unknown-package denial must name the allowlist, got: %v", err)
		}
		if *asked != 0 {
			t.Fatalf("denied install must not prompt the human (asked=%d)", *asked)
		}
		if *builds != 0 {
			t.Fatalf("denied install must not exec (builds=%d)", *builds)
		}
	})

	t.Run("empty allowlist", func(t *testing.T) {
		asked := installPipTestDeps(t, &sandbox.SandboxConfig{
			AllowPipInstall: true,
		}, func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
		builds, _ := stubPipChoke(t, nil)
		if err := gateAndInstallPip(ctx, "requests", "sess-pip-1", "/v/bin/python3", ""); err == nil {
			t.Fatal("empty allowlist: want denial")
		}
		if *asked != 0 {
			t.Fatalf("denied install must not prompt the human (asked=%d)", *asked)
		}
		if *builds != 0 {
			t.Fatalf("denied install must not exec (builds=%d)", *builds)
		}
	})

	t.Run("config disabled", func(t *testing.T) {
		asked := installPipTestDeps(t, &sandbox.SandboxConfig{
			AllowPipInstall: false, PipAllowlist: []string{"requests"},
		}, func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
		if err := gateAndInstallPip(ctx, "requests", "sess-pip-1", "/v/bin/python3", ""); err == nil {
			t.Fatal("AllowPipInstall=false: want denial without asking")
		}
		if *asked != 0 {
			t.Fatalf("config-denied install must not prompt (asked=%d)", *asked)
		}
	})
}

// TestS18PipRequiresApproval pins the approval leg: an allowlisted package
// with approval=false never execs (original output preserved by the
// caller), and approval=true execs exactly once.
func TestS18PipRequiresApproval(t *testing.T) {
	mkSB := func() *sandbox.SandboxConfig {
		return &sandbox.SandboxConfig{AllowPipInstall: true, PipAllowlist: []string{"requests"}}
	}
	ctx := context.Background()

	askedDeny := installPipTestDeps(t, mkSB(), func(req interfaces.ApprovalRequest) (bool, error) {
		if req.Tool != "pip" {
			t.Errorf("pip ask must carry Tool pip, got %q", req.Tool)
		}
		return false, nil
	})
	buildsDeny, _ := stubPipChoke(t, nil)
	if err := gateAndInstallPip(ctx, "requests", "sess-pip-2", "/v/bin/python3", ""); err == nil {
		t.Fatal("approval=false: want refusal")
	}
	if *askedDeny != 1 {
		t.Fatalf("allowlisted install must ask exactly once (asked=%d)", *askedDeny)
	}
	if *buildsDeny != 0 {
		t.Fatalf("denied install must not exec (builds=%d)", *buildsDeny)
	}

	installPipTestDeps(t, mkSB(), func(req interfaces.ApprovalRequest) (bool, error) { return true, nil })
	buildsAllow, runsAllow := stubPipChoke(t, nil)
	if err := gateAndInstallPip(ctx, "requests", "sess-pip-2", "/v/bin/python3", ""); err != nil {
		t.Fatalf("approval=true: want install, got %v", err)
	}
	if *buildsAllow != 1 || *runsAllow != 1 {
		t.Fatalf("approved install must exec exactly once (builds=%d runs=%d)", *buildsAllow, *runsAllow)
	}
}

// TestS18PipSingleRetry pins one install per call: a retry output that
// still names a missing module never triggers a second install.
func TestS18PipSingleRetry(t *testing.T) {
	missing := "ModuleNotFoundError: No module named 'requests'\n"
	runs, installs := 0, 0
	run := func() (string, string) {
		runs++
		return missing, ""
	}
	stdout, _ := runPythonWithAutoPip(context.Background(), run, func(ctx context.Context, pkg string) error {
		installs++
		if pkg != "requests" {
			t.Errorf("installer got package %q, want requests", pkg)
		}
		return nil
	})
	if installs != 1 {
		t.Fatalf("double missing-module: installs=%d, want exactly 1", installs)
	}
	if runs != 2 {
		t.Fatalf("expected initial run + one retry (runs=%d)", runs)
	}
	if !strings.Contains(stdout, "ModuleNotFoundError") {
		t.Fatalf("final output must be the retry output, got %q", stdout)
	}
	// Install refusal keeps the original output with no retry.
	runs, installs = 0, 0
	stdout, _ = runPythonWithAutoPip(context.Background(), run, func(ctx context.Context, pkg string) error {
		installs++
		return context.DeadlineExceeded
	})
	if installs != 1 || runs != 1 {
		t.Fatalf("refused install: installs=%d runs=%d, want 1/1 (no retry)", installs, runs)
	}
}
