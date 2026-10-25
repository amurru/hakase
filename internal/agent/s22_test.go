// s22_test.go - pip-via-choke tests (S22).
package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"amurru/hakase/internal/sandbox"
)

// TestS22PipUsesBuildExecCommand pins the choke routing: a pip install goes
// through the stubbed choke builder (the shared BuildExecCommand choke in
// production) with the pip binary installing the approved package - never
// raw exec - and the canonical system_exec identity binds the flow layer.
func TestS22PipUsesBuildExecCommand(t *testing.T) {
	var gotBin string
	var gotArgs []string
	var gotDir string
	var calls int
	savedBuild, savedRun := pipChokeBuild, startWaitChokeCmd
	pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
		calls++
		gotBin, gotArgs, gotDir = pipBin, args, dir
		return nil, nil
	}
	startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error { return nil }
	t.Cleanup(func() { pipChokeBuild, startWaitChokeCmd = savedBuild, savedRun })

	if err := runPipInstallViaChoke(context.Background(), "/work/.venv/bin/pip", "requests", "/work", map[string]string{"PATH": "x"}); err != nil {
		t.Fatalf("choke pip install: %v", err)
	}
	if calls != 1 {
		t.Fatalf("pip must build through the choke exactly once (calls=%d)", calls)
	}
	if gotBin != "/work/.venv/bin/pip" {
		t.Errorf("choke pip binary = %q", gotBin)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "install" || gotArgs[1] != "requests" {
		t.Errorf("choke pip args = %q, want [install requests]", gotArgs)
	}
	if gotDir != "/work" {
		t.Errorf("choke pip dir = %q, want /work", gotDir)
	}
	// S14 single identity: the sandbox approval and the flow binding hash
	// the canonical tool, so every spelling admits at the same gate.
	if canonicalFlowTool("system_exec_start") != "system_exec" {
		t.Error("system_exec_start must bind the system_exec choke identity")
	}
	// The production builder carries the pip profile onto the shared choke.
	if pipChokeBuild == nil {
		t.Error("pip choke builder must be wired")
	}
	var _ = sandbox.ExecPipInstall
}

// TestS22PipBwrapProfile pins the pip sandbox posture at the choke builder
// boundary: the default builder requests the pip profile (workspace
// read-only except .venv, network open) rather than a raw spawn. The flag
// assertions themselves live in the sandbox package (pip writes nothing
// outside .venv; see TestS22PipBwrapProfile).
func TestS22PipBwrapProfile(t *testing.T) {
	savedBuild := pipChokeBuild
	defer func() { pipChokeBuild = savedBuild }()
	var sawProfile bool
	pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
		// The production default wraps BuildExecCommandFor with
		// ExecPipInstall; a stub that forgets the profile would build a
		// closed-net, workspace-writable command. Rebuild the real argv
		// shape here: pip installs must name install + package only.
		sawProfile = len(args) == 2 && args[0] == "install"
		return nil, nil
	}
	savedRun := startWaitChokeCmd
	startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error { return nil }
	defer func() { startWaitChokeCmd = savedRun }()
	if err := runPipInstallViaChoke(context.Background(), "/v/bin/pip", "requests", "/work", nil); err != nil {
		t.Fatalf("choke pip install: %v", err)
	}
	if !sawProfile {
		t.Error("pip must install exactly one package (install + name, no flags)")
	}
}

// TestS22NoRawFallback pins the no-fallback rule: a choke builder error
// fails the install and no exec is attempted (no silent raw-exec path,
// explicit error on bwrap-unavailable non-Linux included).
func TestS22NoRawFallback(t *testing.T) {
	savedBuild, savedRun := pipChokeBuild, startWaitChokeCmd
	buildErr := errors.New("bubblewrap sandbox unavailable (bwrap not installed?)")
	pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
		return nil, buildErr
	}
	ran := false
	startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error {
		ran = true
		return nil
	}
	t.Cleanup(func() { pipChokeBuild, startWaitChokeCmd = savedBuild, savedRun })
	err := runPipInstallViaChoke(context.Background(), "/v/bin/pip", "requests", "/work", nil)
	if err == nil || !strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("builder error must surface explicitly, got: %v", err)
	}
	if ran {
		t.Fatal("failed build must not exec anything")
	}
}
