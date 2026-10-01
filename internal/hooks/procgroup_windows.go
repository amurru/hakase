//go:build windows

package hooks

import (
	"os/exec"
)

// setProcessGroup is a no-op on Windows: exec.CommandContext already kills
// the direct child on timeout, and Windows has no process-group kill.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup is a no-op on Windows (see setProcessGroup).
func killProcessGroup(pid int) {}
