//go:build unix

package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
)

// buildShellCommand routes a whole command line through the shell (sh -c on
// Unix). The command string is passed to the shell verbatim.
//
// The command is a plain exec.Cmd literal with no ctx wired in: sh is
// resolved with exec.LookPath the way os/exec's Command constructor resolves
// bare names, with Args[0] keeping the original name and a failed lookup
// deferred to Start via cmd.Err. Timeout and cancellation are caller-managed
// (BuildExecCommand's sync/background runners and runGit Start/Wait and kill
// the process tree themselves), so the context is not used here.
func buildShellCommand(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	_ = dir
	cmd := &exec.Cmd{Path: "sh", Args: []string{"sh", "-c", command}}
	if resolved, err := exec.LookPath("sh"); err != nil {
		cmd.Err = err
	} else {
		cmd.Path = resolved
	}
	return cmd, nil
}

// buildDirectCommand builds the explicit (command, args...) form: no shell
// parsing, and the executable is resolved against PATH for bare names
// (path-form commands are used as given; that search never consults the
// working directory, so a workspace-planted same-named file cannot hijack).
//
// The command is a plain exec.Cmd literal (the context-free form of
// os/exec's constructor). execve does no PATH search of its own, so a bare
// name (filepath.Base == command, e.g. "git") is resolved with exec.LookPath
// exactly as os/exec's Command constructor resolved it: Path becomes the
// resolved binary while Args[0] keeps the original name, and a failed lookup
// is deferred to Start via cmd.Err. Timeout and cancellation are
// caller-managed, so the context is not used here.
func buildDirectCommand(ctx context.Context, command string, args []string, dir string) (*exec.Cmd, error) {
	_ = dir
	cmd := &exec.Cmd{Path: command, Args: append([]string{command}, args...)}
	if filepath.Base(command) == command {
		if resolved, err := exec.LookPath(command); err != nil {
			cmd.Err = err
		} else {
			cmd.Path = resolved
		}
	}
	return cmd, nil
}

// hardenChildEnv is the Unix identity: environment keys are case-sensitive
// and the sensitive-prefix scrub in ScrubEnv already applies byte-exact.
func hardenChildEnv(env []string) []string {
	return env
}
