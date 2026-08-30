//go:build linux

package main

import (
	"syscall"
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
	// exactly SIGKILL so the kernel reaps the child if the agent dies.
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig: expected SIGKILL, got %v", cmd.SysProcAttr.Pdeathsig)
	}
}

