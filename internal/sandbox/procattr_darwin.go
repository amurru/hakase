//go:build darwin

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcess applies platform process hardening to a command before
// Start: the child goes into its own process group (Setpgid) so the tree can
// be killed as a whole. The stdlib syscall package has no Pdeathsig field on
// darwin (#38), so the "kernel reaps the child if the agent dies" hardening
// is unavailable here; the process-group kill in killProcessTree still
// covers the normal shutdown paths. The LockOSThread contract of the
// Pdeathsig platforms is unnecessary but harmless on darwin.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}
