// exec_choke.go - the two-layer process-spawn choke for agent tools (S23).
//
// Layer 1 (this file, raw but scrubbed): the python interpreter
// (chokeRunPython) and venv creation (chokeCreateVenv) spawn directly
// here with the scrubbed S20 child env and no ambient inheritance.
// This unsandboxed-python exception is explicit: the interpreter must
// run outside BuildExecCommand so its scrubbed env stays exact, and the
// S20 loader preamble (absolute-path skill imports, no PYTHONPATH)
// is the compensating control.
// Layer 2 (shared sandbox.BuildExecCommand): pip installs (pipChokeBuild
// with the ExecPipInstall profile) and system_exec go through the
// policy + path-confinement + bwrap pipeline with ExecReplaceEnv.
//
// No other non-test file in internal/agent may construct process-spawn
// primitives directly: TestS23SingleChoke scans both layers (raw sites
// only in this file with a choke-allow annotation, everything else via
// the shared choke) and fails the lane on any reintroduction.
//
// S22: pip installs are built by sandbox.BuildExecCommandFor with the
// ExecPipInstall profile (bwrap workspace read-only except .venv,
// network explicitly open for index egress with the S18 allowlist +
// approval + name-shape gates as compensating controls) and run with the
// same bounded wait + process-tree kill as every other choke spawn.
package agent

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"

	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/util"
)

// execPythonScript runs a prepared python script file through the choke.
// S23 inventory row: python_interpreter run -> choke (net closed, tmp
// script dir only writable). Stub-injectable for tests.
var execPythonScript = func(ctx context.Context, pyBin, scriptPath, workDir string, env []string) (string, string) {
	return chokeRunPython(ctx, pyBin, scriptPath, workDir, env)
}

// execCreateVenv creates a virtualenv via one launcher probe through the
// choke. S23 inventory row: venv create -> choke (net closed, .venv only
// writable). Stub-injectable for tests.
var execCreateVenv = func(spec []string) error {
	return chokeCreateVenv(spec)
}

// pipChokeBuild builds the pip install command through the shared sandbox
// choke with the pip profile (system_exec canonical identity per S14, so
// the flow binding hashes system_exec) and the scrubbed S20 child env as
// a replace env (no ambient inheritance). S23 inventory row: pip install
// is the only BuildExecCommand row. Stub-injectable for tests.
var pipChokeBuild = func(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) (*exec.Cmd, error) {
	// ExecPreApproved carries the S18 gate decision (name-shape +
	// allowlist + human approval, collected in gateAndInstallPip before
	// this builder is reached): the shared choke skips only the
	// allowed_commands leg and the known-risk re-prompt, while hard
	// denies, deny patterns, permissions/flow verdicts, and the path
	// audit still apply.
	return sandbox.BuildExecCommandFor(ctx, pipBin, args, dir, env, sandbox.ExecPipInstall(), sandbox.ExecReplaceEnv(env),
		sandbox.ExecPreApproved("s18 pip name-shape + allowlist + human approval"))
}

// startWaitChokeCmd starts a choke-built command and waits with the
// bounded process-tree discipline. Stub-injectable for tests.
var startWaitChokeCmd = func(ctx context.Context, cmd *exec.Cmd) error {
	return waitChokeCmd(ctx, cmd)
}

// chokeRunPython spawns the python interpreter on a prepared script file
// (the single python spawn site). Stdout and stderr merge into the first
// return; the second return carries the error text ("" on success).
func chokeRunPython(ctx context.Context, pyBin, scriptPath, workDir string, env []string) (string, string) {
	// choke-allow: exec_choke.go owns python spawn (S23 single choke).
	cmd := &exec.Cmd{Path: pyBin, Args: []string{pyBin, scriptPath}} // choke-allow: single python spawn site (S23).
	cmd.Env = env
	if workDir != "" {
		cmd.Dir = workDir
	}
	// Process hardening: platform tree management (process group +
	// Pdeathsig on Unix, Job Object on Windows).
	sandbox.ConfigureProcess(cmd)
	// Pdeathsig fires on the OS thread that calls Start; lock it so the
	// runtime does not recycle it before the child is started
	// (golang/go#27505). Harmless on Windows.
	runtime.LockOSThread()
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	if err := cmd.Start(); err != nil {
		runtime.UnlockOSThread()
		return "", err.Error()
	}
	if err := sandbox.AttachProcessTree(cmd); err != nil {
		util.DebugWarn("python_job_attach_failed", "error", err.Error())
	}
	runtime.UnlockOSThread()

	// Bounded wait: CommandContext cancels only the direct child; Wait
	// cannot return while a descendant still holds the inherited output
	// pipe. On cancellation, kill the whole tree first so Wait is
	// guaranteed to complete.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var errOut error
	select {
	case errOut = <-waitCh:
	case <-ctx.Done():
		_ = sandbox.KillProcessTree(cmd)
		errOut = <-waitCh
	}
	sandbox.ReleaseProcessTree(cmd)
	errStr := ""
	if errOut != nil {
		errStr = errOut.Error()
	}
	return outBuf.String(), errStr
}

// chokeCreateVenv runs one venv launcher probe (the single venv spawn
// site). PATH resolution mirrors the stdlib os/exec behavior for a bare
// name; a lookup failure is stored on cmd.Err and surfaces from Run.
func chokeCreateVenv(spec []string) error {
	// choke-allow: exec_choke.go owns venv creation spawn (S23 single choke).
	cmd := &exec.Cmd{Path: spec[0], Args: append([]string(nil), spec...)} // choke-allow: single venv spawn site (S23).
	// Mirror the stdlib os/exec PATH resolution of a bare name; a
	// lookup failure is stored on cmd.Err and surfaces from cmd.Run.
	if filepath.Base(spec[0]) == spec[0] {
		if lp, lerr := exec.LookPath(spec[0]); lp != "" {
			cmd.Path = lp
			if lerr != nil {
				cmd.Err = lerr
			}
		} else if lerr != nil {
			cmd.Err = lerr
		}
	}
	// S20: the venv launcher inherits no ambient env. The child env is
	// the scrubbed build (minimal PATH headed by the resolved launcher
	// dir, explicit allowlist only) so a poisoned PYTHONPATH or ambient
	// marker never reaches the launcher, exactly like chokeRunPython.
	cmd.Env = buildPythonChildEnv(cmd.Path)
	sandbox.ConfigureProcess(cmd)
	return cmd.Run()
}

// runPipInstallViaChoke builds the pip install through the shared sandbox
// choke (S22) and runs it with the bounded process-tree discipline. A
// builder error fails the install: there is no silent raw-exec fallback.
// bwrap-unavailable on non-Linux surfaces here as an explicit error.
func runPipInstallViaChoke(ctx context.Context, pipBin, pkg, dir string, env map[string]string) error {
	return runPipInstallViaChokeArgs(ctx, pipBin, []string{"install", pkg}, dir, env)
}

// runPipInstallViaChokeArgs is the argv-explicit form (S27 strict pins
// install ["install", "--require-hashes", "--hash=...", pin] through the
// same choke). Same no-fallback contract as runPipInstallViaChoke.
func runPipInstallViaChokeArgs(ctx context.Context, pipBin string, args []string, dir string, env map[string]string) error {
	cmd, err := pipChokeBuild(ctx, pipBin, args, dir, env)
	if err != nil {
		return err
	}
	return startWaitChokeCmd(ctx, cmd)
}

// waitChokeCmd starts cmd and waits with cancellation-aware process-tree
// teardown (the same discipline the python and pip spawns share).
func waitChokeCmd(ctx context.Context, cmd *exec.Cmd) error {
	runtime.LockOSThread()
	startErr := cmd.Start()
	if startErr == nil {
		if aerr := sandbox.AttachProcessTree(cmd); aerr != nil {
			util.DebugWarn("choke_job_attach_failed", "error", aerr.Error())
		}
	}
	runtime.UnlockOSThread()
	if startErr != nil {
		sandbox.ReleaseProcessTree(cmd)
		return startErr
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		sandbox.ReleaseProcessTree(cmd)
		return err
	case <-ctx.Done():
		_ = sandbox.KillProcessTree(cmd)
		err := <-waitCh
		sandbox.ReleaseProcessTree(cmd)
		return err
	}
}
