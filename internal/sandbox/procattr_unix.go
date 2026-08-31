//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcess applies platform process hardening to a command before
// Start. On Unix this places the child in its own process group (Setpgid)
// with a parent-death signal (Pdeathsig:SIGKILL), so the process group can be
// killed as a whole and the kernel reaps the child if the agent dies.
// Callers must wrap Start+Wait in runtime.LockOSThread/UnlockOSThread so the
// Pdeathsig thread stays alive for the child's lifetime (golang/go#27505).
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

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
