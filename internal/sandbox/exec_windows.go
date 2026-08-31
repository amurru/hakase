//go:build windows

package sandbox

import (
	"context"
	"os/exec"
)

// buildShellCommand routes a whole command line through cmd /D /C on Windows:
//   - /C carries the command and terminates cmd after it
//   - /D suppresses per-user AutoRun registry scripts
//     (HKCU\Software\Microsoft\Command Processor\AutoRun)
//
// The original command string is preserved as a single argument. POSIX-only
// constructs (globs, $(), backticks, VAR=x cmd) are not interpreted by cmd;
// the tool description documents this.
func buildShellCommand(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	roots := workspaceRootsForHijackCheck()
	hardened, err := hardenWindowsShellCommand(command, dir, roots)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "cmd", "/D", "/C", hardened), nil
}

// buildDirectCommand builds the explicit (command, args...) form with
// PATH-only executable resolution: a bare command name is rewritten to its
// absolute PATH path (never the working directory), and resolutions landing
// inside a workspace root are rejected.
func buildDirectCommand(ctx context.Context, command string, args []string, dir string) (*exec.Cmd, error) {
	resolved, err := resolveExplicitWindowsCommand(command, dir, workspaceRootsForHijackCheck())
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, resolved, args...), nil
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
