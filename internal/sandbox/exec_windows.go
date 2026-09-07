//go:build windows

package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
)

// buildShellCommand routes a whole command line through cmd /D /C on Windows:
//   - /C carries the command and terminates cmd after it
//   - /D suppresses per-user AutoRun registry scripts
//     (HKCU\Software\Microsoft\Command Processor\AutoRun)
//
// The original command string is preserved as a single argument. POSIX-only
// constructs (globs, $(), backticks, VAR=x cmd) are not interpreted by cmd;
// the tool description documents this.
//
// The command is a plain exec.Cmd literal with no ctx wired in: "cmd" is
// resolved with exec.LookPath the way os/exec's Command constructor resolves
// bare names (PATH only, PATHEXT extensions, cwd excluded via
// NoDefaultCurrentDirectoryInExePath=1 set in init), with Args[0] keeping
// the original bare name and a failed lookup deferred to Start via cmd.Err.
// Timeout and cancellation are caller-managed (the system_exec runners
// Start/Wait and kill the process tree themselves), so the context is not
// used here.
func buildShellCommand(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	roots := workspaceRootsForHijackCheck()
	hardened, err := hardenWindowsShellCommand(command, dir, roots)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: "cmd", Args: []string{"cmd", "/D", "/C", hardened}}
	if resolved, err := exec.LookPath("cmd"); err != nil {
		cmd.Err = err
	} else {
		cmd.Path = resolved
	}
	return cmd, nil
}

// buildDirectCommand builds the explicit (command, args...) form with
// PATH-only executable resolution: a bare command name is rewritten to its
// absolute PATH path (never the working directory), and resolutions landing
// inside a workspace root are rejected.
//
// The command is a plain exec.Cmd literal (the context-free form of
// os/exec's constructor). resolved is the absolute PATH-resolved binary
// whenever resolution succeeded; an unresolvable bare name is passed through
// unchanged and re-resolved here with exec.LookPath so its lookup error is
// deferred to Start via cmd.Err, exactly as before. Timeout and cancellation
// are caller-managed, so the context is not used here.
func buildDirectCommand(ctx context.Context, command string, args []string, dir string) (*exec.Cmd, error) {
	resolved, err := resolveExplicitWindowsCommand(command, dir, workspaceRootsForHijackCheck())
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: resolved, Args: append([]string{resolved}, args...)}
	if filepath.Base(resolved) == resolved {
		if p, err := exec.LookPath(resolved); err != nil {
			cmd.Err = err
		} else {
			cmd.Path = p
		}
	}
	return cmd, nil
}

// workspaceRootsForHijackCheck returns the sandbox workspace roots (untrusted
// content ground) used by the executable-resolution checks.
func workspaceRootsForHijackCheck() []string {
	if CurrentSandbox == nil {
		return nil
	}
	return CurrentSandbox.WorkspaceRoots
}

// hardenChildEnv ensures every spawned child carries
// NoDefaultCurrentDirectoryInExePath=1 even when its environment was
// assembled from a non-os.Environ source.
func hardenChildEnv(env []string) []string {
	return fixupWindowsChildEnv(env)
}
