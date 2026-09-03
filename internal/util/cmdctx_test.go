package util

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestRunContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &exec.Cmd{Path: "true", Args: []string{"true"}}
	if err := RunContext(ctx, cmd); err != context.Canceled {
		t.Errorf("RunContext with done ctx = %v, want context.Canceled", err)
	}
}

func TestRunContextTimeoutKillsProcess(t *testing.T) {
	// sleep 5s must be killed by a 300ms context; the run returns
	// context.DeadlineExceeded, not the full sleep.
	bin := "sleep"
	if runtime.GOOS == "windows" {
		bin = "ping"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	argv := []string{bin, "5"}
	if runtime.GOOS == "windows" {
		argv = []string{bin, "-n", "6", "127.0.0.1"}
	}
	cmd := &exec.Cmd{Path: bin, Args: argv}
	start := time.Now()
	err := RunContext(ctx, cmd)
	if err != context.DeadlineExceeded {
		t.Fatalf("RunContext = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("run took %v; process was not killed on ctx timeout", elapsed)
	}
}

func TestRunContextNormalExit(t *testing.T) {
	ctx := context.Background()
	cmd := &exec.Cmd{Path: "sh", Args: []string{"sh", "-c", "exit 3"}}
	if runtime.GOOS == "windows" {
		cmd = &exec.Cmd{Path: "cmd", Args: []string{"cmd", "/D", "/C", "exit 3"}}
	}
	err := RunContext(ctx, cmd)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("RunContext error = %v, want *exec.ExitError", err)
	}
	if ee.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", ee.ExitCode())
	}
}

func TestCombinedOutputContextCapture(t *testing.T) {
	ctx := context.Background()
	cmd := &exec.Cmd{Path: "echo", Args: []string{"echo", "hello-ctx"}}
	if runtime.GOOS == "windows" {
		cmd = &exec.Cmd{Path: "cmd", Args: []string{"cmd", "/D", "/C", "echo hello-ctx"}}
	}
	out, err := CombinedOutputContext(ctx, cmd)
	if err != nil {
		t.Fatalf("CombinedOutputContext: %v", err)
	}
	if string(out) != "hello-ctx\n" {
		t.Errorf("output = %q, want %q", string(out), "hello-ctx\n")
	}
}
