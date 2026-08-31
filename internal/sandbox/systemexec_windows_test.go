//go:build windows

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildExecCommandWindowsShellRouting pins the cmd /D /C routing: the
// original command string is preserved as a single argument.
func TestBuildExecCommandWindowsShellRouting(t *testing.T) {
	saved := CurrentSandbox
	CurrentSandbox = nil
	t.Cleanup(func() { CurrentSandbox = saved })

	cmd, err := BuildExecCommand("echo hi", nil, "", nil)
	if err != nil {
		t.Fatalf("BuildExecCommand: %v", err)
	}
	want := []string{"cmd", "/D", "/C", "echo hi"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("cmd.Args: expected %v, got %v", want, cmd.Args)
	}
	for i, w := range want {
		if cmd.Args[i] != w {
			t.Errorf("cmd.Args[%d]: expected %q, got %q", i, w, cmd.Args[i])
		}
	}
	if cmd.Path == "" || !strings.Contains(strings.ToLower(cmd.Path), "cmd.exe") {
		t.Errorf("cmd.Path: expected a resolved cmd.exe, got %q", cmd.Path)
	}
}

// TestBuildExecCommandWindowsNestedQuotes is the quoting golden: the command
// string including nested quotes must survive as one argument.
func TestBuildExecCommandWindowsNestedQuotes(t *testing.T) {
	saved := CurrentSandbox
	CurrentSandbox = nil
	t.Cleanup(func() { CurrentSandbox = saved })

	in := `echo "hello world" > "out file.txt"`
	cmd, err := BuildExecCommand(in, nil, "", nil)
	if err != nil {
		t.Fatalf("BuildExecCommand: %v", err)
	}
	if len(cmd.Args) != 4 || cmd.Args[3] != in {
		t.Fatalf("nested-quote golden: expected Args[3]=%q verbatim, got %v", in, cmd.Args)
	}
}

// TestSystemExecCmdShellSemantics runs real commands through the cmd /D /C
// path: redirect, pipe, and && all work; the child env carries
// NoDefaultCurrentDirectoryInExePath.
func TestSystemExecCmdShellSemantics(t *testing.T) {
	saved := CurrentSandbox
	CurrentSandbox = nil
	t.Cleanup(func() { CurrentSandbox = saved })

	dir := t.TempDir()

	// Redirect writes via cmd.
	cmd, err := BuildExecCommand("echo hi > out.txt", nil, dir, nil)
	if err != nil {
		t.Fatalf("BuildExecCommand redirect: %v", err)
	}
	if !envHasNoDefaultCurrentDirectoryInExePath(cmd.Env) {
		t.Error("child env missing NoDefaultCurrentDirectoryInExePath=1")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("redirect run: %v (%s)", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("redirect output missing: %v", err)
	}
	if !strings.Contains(strings.TrimSpace(string(data)), "hi") {
		t.Errorf("redirect: expected 'hi' in out.txt, got %q", string(data))
	}

	// Pipes work.
	cmd, err = BuildExecCommand("echo banana | findstr ana", nil, dir, nil)
	if err != nil {
		t.Fatalf("BuildExecCommand pipe: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pipe run: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "banana") {
		t.Errorf("pipe: expected 'banana' in output, got %q", string(out))
	}

	// && works.
	cmd, err = BuildExecCommand("echo one && echo two", nil, dir, nil)
	if err != nil {
		t.Fatalf("BuildExecCommand &&: %v", err)
	}
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("&& run: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "one") || !strings.Contains(string(out), "two") {
		t.Errorf("&&: expected one+two in output, got %q", string(out))
	}
}

// TestWindowsExecutableHijackRejected plants a stub executable (.exe and
// .bat variants) in a temp workspace and asserts it is NOT executed, both via
// the string form (cmd /D /C stub-cmd ...) and the explicit form.
func TestWindowsExecutableHijackRejected(t *testing.T) {
	saved := CurrentSandbox
	CurrentSandbox = nil
	t.Cleanup(func() { CurrentSandbox = saved })

	dir := t.TempDir()
	marker := filepath.Join(dir, "hijacked.txt")
	body := "@echo hijacked > hijacked.txt\r\n"
	for _, name := range []string{"stub-cmd.bat", "stub-cmd.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// String form: cmd /D /C stub-cmd --flag
	if _, err := BuildExecCommand("stub-cmd --flag", nil, dir, nil); err == nil {
		t.Fatal("string form: expected planted-executable rejection, got nil error")
	} else if !strings.Contains(err.Error(), "working directory") && !strings.Contains(err.Error(), "workspace") {
		t.Errorf("string form: unexpected error text: %v", err)
	}

	// Explicit form with the bare name.
	if _, err := BuildExecCommand("stub-cmd", []string{"--flag"}, dir, nil); err == nil {
		t.Fatal("explicit form: expected planted-executable rejection, got nil error")
	}

	// Explicit form with the extensioned name.
	if _, err := BuildExecCommand("stub-cmd.bat", nil, dir, nil); err == nil {
		t.Fatal("explicit .bat form: expected planted-executable rejection, got nil error")
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("planted executable WAS executed (marker file exists)")
	}
}

// TestWindowsPATHResolutionStillWorks verifies a legit bare system command
// still resolves and runs, and the string-form first token is rewritten to an
// absolute path.
func TestWindowsPATHResolutionStillWorks(t *testing.T) {
	saved := CurrentSandbox
	CurrentSandbox = nil
	t.Cleanup(func() { CurrentSandbox = saved })

	dir := t.TempDir()

	// Explicit form: bare "cmd" resolves via PATH (System32) and runs.
	cmd, err := BuildExecCommand("cmd", []string{"/D", "/C", "echo resolved"}, dir, nil)
	if err != nil {
		t.Fatalf("explicit bare cmd: %v", err)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Errorf("explicit form: expected absolute cmd.Path, got %q", cmd.Path)
	}
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "resolved") {
		t.Fatalf("explicit bare cmd run: %v (%s)", err, out)
	}

	// String form: "where" (not a cmd builtin) is rewritten to its absolute
	// PATH path while the rest of the line is preserved.
	resolved, err := hardenWindowsShellCommand("where cmd", dir, nil)
	if err != nil {
		t.Fatalf("hardenWindowsShellCommand: %v", err)
	}
	if !filepath.IsAbs(strings.Fields(resolved)[0]) {
		t.Errorf("string form: expected first token rewritten to absolute path, got %q", resolved)
	}
	if !strings.HasSuffix(resolved, " cmd") {
		t.Errorf("string form: expected trailing args preserved, got %q", resolved)
	}

	// A cmd builtin is never rewritten.
	unchanged, err := hardenWindowsShellCommand("echo hi", dir, nil)
	if err != nil || unchanged != "echo hi" {
		t.Errorf("builtin: expected command unchanged, got %q (err %v)", unchanged, err)
	}
}

// TestScrubEnvWindowsCaseInsensitive pins the Windows-only case-insensitive
// sensitive-prefix scrub.
func TestScrubEnvWindowsCaseInsensitive(t *testing.T) {
	env := []string{
		"PATH=C:\\Windows",
		"Aws_secret_access_key=topsecret",
		"openai_api_key=leak",
		"Hakase_Home=C:\\temp",
		"GitHub_Pat=leak",
		"HOME=C:\\Users\\u",
	}
	got := ScrubEnv(env)
	joined := strings.Join(got, ";")
	for _, bad := range []string{"Aws_secret_access_key", "openai_api_key", "Hakase_Home", "GitHub_Pat"} {
		if strings.Contains(joined, bad) {
			t.Errorf("ScrubEnv kept case-variant sensitive key %q", bad)
		}
	}
	if !strings.Contains(joined, "PATH=") || !strings.Contains(joined, "HOME=") {
		t.Errorf("ScrubEnv dropped non-sensitive keys: %v", got)
	}
}

func envHasNoDefaultCurrentDirectoryInExePath(env []string) bool {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "NoDefaultCurrentDirectoryInExePath") && v == "1" {
			return true
		}
	}
	return false
}
