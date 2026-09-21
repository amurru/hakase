//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// attachProcessTree completes tree tracking after a successful Start. The
// Unix implementation has nothing left to do: Setpgid already put the whole
// tree into one process group at creation time.
func attachProcessTree(*exec.Cmd) error { return nil }

// killProcessTree terminates the process and all of its descendants by
// signalling the negated process group (the Setpgid group).
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// releaseProcessTree releases per-process tracking resources after Wait.
// No-op on Unix.
func releaseProcessTree(*exec.Cmd) {}

// shellRoutingNote is appended to system_exec tool descriptions. Empty on
// Unix, where sh -c routing semantics are unchanged.
const shellRoutingNote = ""
