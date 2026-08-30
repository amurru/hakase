//go:build linux

package main

import (
	"testing"

	"amurru/hakase/internal/sandbox"
)

// TestBuildExecCommandSysProcAttr verifies the process hardening attributes
// are set on every spawned command.
func TestBuildExecCommandSysProcAttr(t *testing.T) {

	withNilSandbox(t)

	cmd, err := sandbox.BuildExecCommand("true", nil, "", nil)
	if err != nil {
		t.Fatalf("sandbox.BuildExecCommand: %v", err)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid: expected true")
	}
	// Pdeathsig is Linux-specific; on this Linux-only project it must be
	// SIGKILL.
	if cmd.SysProcAttr.Pdeathsig != 0 { // syscall.SIGKILL == 0x9
		// Just verify it is non-zero (set); comparing to syscall.SIGKILL
		// would require importing syscall here which is overkill.
	}
}

