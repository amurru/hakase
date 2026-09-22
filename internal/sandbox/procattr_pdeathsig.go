//go:build unix && !darwin

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
