package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout swaps the process stdout/stderr for a pipe, runs fn, and
// returns everything written during the call.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()

	done := make(chan string)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	w.Close()
	return <-done
}

func TestRunVersionCLIDefaults(t *testing.T) {
	var code int
	out := captureOutput(t, func() { code = RunVersionCLI(nil) })
	if code != 0 {
		t.Fatalf("RunVersionCLI() = %d, want 0", code)
	}
	if !strings.HasPrefix(out, "hakase ") {
		t.Errorf("output should start with 'hakase <version>', got %q", out)
	}
	for _, want := range []string{"commit:", "built:", "go:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunVersionCLIShort(t *testing.T) {
	old := Version
	Version = "v9.9.9-test"
	defer func() { Version = old }()

	var code int
	out := captureOutput(t, func() { code = RunVersionCLI([]string{"--short"}) })
	if code != 0 {
		t.Fatalf("RunVersionCLI(--short) = %d, want 0", code)
	}
	if got := strings.TrimSpace(out); got != "v9.9.9-test" {
		t.Errorf("--short output = %q, want %q", got, "v9.9.9-test")
	}
}

func TestRunVersionCLIUsageError(t *testing.T) {
	var code int
	_ = captureOutput(t, func() { code = RunVersionCLI([]string{"--bogus-flag"}) })
	if code != 2 {
		t.Errorf("RunVersionCLI(--bogus-flag) = %d, want 2 (usage error)", code)
	}
}

func TestVersionCommandRegistered(t *testing.T) {
	cmd, ok := commands["version"]
	if !ok {
		t.Fatal("version command is not registered")
	}
	if cmd.Handler == nil {
		t.Fatal("version command has no handler")
	}
}
