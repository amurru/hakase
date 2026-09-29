//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the hook child in its own process group so a timeout
// can kill the whole group (grandchildren included), not just the direct
// child. Unix-only; the windows file is a no-op.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the child's process group. Best-effort: the
// direct child is already dead via the context kill; this reaps runaway
// grandchildren. A negative pid targets the group.
func killProcessGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
