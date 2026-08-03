// sandboxexec.go - Phase 2 bubblewrap subprocess sandboxing.
//
// When currentSandbox.Mode == SandboxModeBubblewrap, subprocess execution
// (system_exec, python_interpreter) is wrapped in bubblewrap (bwrap) to
// kernel-enforce filesystem confinement and network isolation. This is the
// approach Anthropic uses for Claude Code on Linux (sandbox-runtime).
//
// The wrapper builds a bwrap argument list that:
//   - Creates isolated PID/IPC/UTS/user namespaces (--unshare-pid etc.)
//   - Drops all capabilities (--cap-drop ALL)
//   - Provides a minimal /proc, /dev, /tmp, /run
//   - Read-only binds system directories (/usr, /lib, /bin, /etc, ...)
//   - Read-write binds each approved workspace root
//   - Optionally removes network (--unshare-net) for full isolation
//   - Sets the working directory to the workspace root
//
// See the "Sandboxing & workspace confinement > Phase 2" section of
// .omo/plans/hakase-debug-log-fixes.md for the design rationale.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// bwrapBinary is the bubblewrap executable name looked up on PATH.
const bwrapBinary = "bwrap"

// systemROBindDirs are the host directories bound read-only inside the
// sandbox so common commands and shared libraries resolve. /lib64 and
// /sbin use --ro-bind-try (they may not exist on all distros).
var systemROBindDirs = []struct {
	path string
	try  bool
}{
	{"/usr", false},
	{"/lib", false},
	{"/lib64", true},
	{"/bin", false},
	{"/sbin", true},
	{"/etc", false},
	{"/nix", true}, // NixOS stores
}

// bwrapPath returns the absolute path to the bwrap binary, or an error if
// it is not installed. The lookup is cached after the first call.
var bwrapCachedPath string
var bwrapCachedErr error

func bwrapPath() (string, error) {
	if bwrapCachedPath != "" {
		return bwrapCachedPath, nil
	}
	if bwrapCachedErr != nil {
		return "", bwrapCachedErr
	}
	p, err := exec.LookPath(bwrapBinary)
	if err != nil {
		bwrapCachedErr = fmt.Errorf("bubblewrap (%s) not found on PATH: %w; install the 'bubblewrap' package or set sandbox.mode to 'paths' or 'off'", bwrapBinary, err)
		return "", bwrapCachedErr
	}
	bwrapCachedPath = p
	return p, nil
}

// buildBwrapArgv constructs the argument list for a bubblewrap invocation
// that runs innerArgv inside the sandbox. When needsNetwork is false the
// network namespace is unshared (loopback-only, no external connectivity).
// extraBinds are additional --bind entries (each "src:dst" or just "path"
// which binds path to itself read-write). The workspace roots from sb are
// always bound read-write; extraBinds covers non-workspace paths like .venv.
func buildBwrapArgv(sb *SandboxConfig, innerArgv []string, workingDir string, needsNetwork bool, extraBinds []string) ([]string, error) {
	if sb == nil {
		return nil, fmt.Errorf("buildBwrapArgv: sandbox config is nil")
	}
	if len(innerArgv) == 0 {
		return nil, fmt.Errorf("buildBwrapArgv: innerArgv must not be empty")
	}

	var argv []string

	// Namespace isolation.
	argv = append(argv,
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-user",
		"--cap-drop", "ALL",
	)

	// Filesystem: minimal /proc, /dev, /tmp, /run.
	argv = append(argv,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
	)

	// Read-only system directories.
	for _, d := range systemROBindDirs {
		if _, err := os.Stat(d.path); err != nil {
			continue // skip non-existent
		}
		flag := "--ro-bind"
		if d.try {
			flag = "--ro-bind-try"
		}
		argv = append(argv, flag, d.path, d.path)
	}

	// Workspace roots: read-write.
	for _, root := range sb.WorkspaceRoots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		argv = append(argv, "--bind", root, root)
	}

	// Extra binds (e.g. .venv, downloads) - each entry is either
	// "src:dst" or a bare path (bound to itself).
	for _, b := range extraBinds {
		src, dst := b, b
		if i := lastIndexByte(b, ':'); i > 0 && filepath.IsAbs(b[:i]) {
			src, dst = b[:i], b[i+1:]
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		argv = append(argv, "--bind", src, dst)
	}

	// Working directory inside the sandbox.
	if workingDir != "" {
		argv = append(argv, "--chdir", workingDir)
	} else if root := sb.workspaceRoot(); root != "" {
		argv = append(argv, "--chdir", root)
	}

	// Network isolation: --unshare-net gives loopback-only (no route).
	if !needsNetwork {
		argv = append(argv, "--unshare-net")
	}

	// Terminator + inner command.
	argv = append(argv, "--")
	argv = append(argv, innerArgv...)

	return argv, nil
}

// lastIndexByte is a small helper to avoid importing strings just for one
// call; it returns the last index of c in s, or -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// wrapBwrapCmd builds an *exec.Cmd that runs innerArgv inside a bubblewrap
// sandbox according to sb. It resolves the bwrap binary, constructs the
// sandbox argument list, and returns the ready-to-start command. The caller
// is responsible for setting Env, SysProcAttr, Stdout/Stderr on the
// returned cmd (buildExecCommand does this after calling this function).
//
// Returns (nil, error) if bwrap is not installed or the sandbox config is
// invalid for bubblewrap mode. The caller should fall back to the non-sandbox
// exec path on error - or fail loudly, depending on policy.
func wrapBwrapCmd(sb *SandboxConfig, innerArgv []string, workingDir string, needsNetwork bool, extraBinds []string) (*exec.Cmd, error) {
	bp, err := bwrapPath()
	if err != nil {
		return nil, err
	}
	argv, err := buildBwrapArgv(sb, innerArgv, workingDir, needsNetwork, extraBinds)
	if err != nil {
		return nil, err
	}
	return exec.Command(bp, argv...), nil
}
