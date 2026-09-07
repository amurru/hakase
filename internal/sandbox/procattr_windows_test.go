//go:build windows

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestKillProcessTreeWindows spawns a cmd /D /C child that in turn spawns a
// grandchild, calls the kill helper, and asserts the child exits promptly and
// the grandchild never completes its natural 30s sleep (both die).
func TestKillProcessTreeWindows(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "child.bat")
	script := "@echo off\r\n" +
		"start \"\" /b cmd /D /C \"ping -n 30 127.0.0.1 > NUL & echo done > grandchild-exit.txt\"\r\n" +
		"ping -n 30 127.0.0.1 > NUL\r\n"
	if err := os.WriteFile(bat, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	cmdExe, err := exec.LookPath("cmd")
	if err != nil {
		t.Fatalf("LookPath cmd: %v", err)
	}
	cmd := &exec.Cmd{Path: cmdExe, Args: []string{"cmd", "/D", "/C", bat}}
	configureProcess(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := attachProcessTree(cmd); err != nil {
		t.Fatalf("attachProcessTree: %v", err)
	}

	// Give the child time to spawn the grandchild before killing.
	time.Sleep(1500 * time.Millisecond)

	if err := killProcessTree(cmd); err != nil {
		t.Fatalf("killProcessTree: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		releaseProcessTree(cmd)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit within 10s of killProcessTree")
	}

	// Grace for the OS to tear down the tree; a survivor would only write
	// its marker after the full 30s ping sleep, well past this check.
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(filepath.Join(dir, "grandchild-exit.txt")); err == nil {
		t.Fatal("grandchild survived killProcessTree")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("tree kill took %v; expected prompt termination", elapsed)
	}
}
