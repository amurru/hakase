//go:build unix

package sandbox

import (
	"context"
	"os/exec"
)

// buildShellCommand routes a whole command line through the shell (sh -c on
// Unix). The command string is passed to the shell verbatim.
func buildShellCommand(ctx context.Context, command, dir string) (*exec.Cmd, error) {
	_ = dir
	return exec.CommandContext(ctx, "sh", "-c", command), nil
}

// buildDirectCommand builds the explicit (command, args...) form. Unix needs
// no extra executable resolution: sh and exec already require an explicit
// path (or PATH search that never consults the working directory).
func buildDirectCommand(ctx context.Context, command string, args []string, dir string) (*exec.Cmd, error) {
	_ = dir
	return exec.CommandContext(ctx, command, args...), nil
}

// hardenChildEnv is the Unix identity: environment keys are case-sensitive
// and the sensitive-prefix scrub in ScrubEnv already applies byte-exact.
func hardenChildEnv(env []string) []string {
	return env
}
