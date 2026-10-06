// hooks_test.go - `hakase hooks list` against an isolated HAKASE_HOME.
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeHooksTestConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestHooksListShowsConfiguredHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"PreToolUse":[{"matcher":"^system_exec$","hooks":[{"name":"nope","command":["/bin/false"]}]}]}}`)

	// list prints to stdout; only the exit code is asserted here (shape is
	// covered by the runner snapshot tests).
	if code := RunHooksCLI([]string{"list"}); code != 0 {
		t.Errorf("hooks list: exit %d, want 0", code)
	}
}

func TestHooksListEmptyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	if code := RunHooksCLI([]string{"list"}); code != 0 {
		t.Errorf("hooks list (empty): exit %d, want 0", code)
	}
}

func TestHooksUsageErrors(t *testing.T) {
	if code := RunHooksCLI(nil); code != 2 {
		t.Errorf("hooks (no args): exit %d, want 2", code)
	}
	_, code := captureDispatchStderr(t, func() int { return RunHooksCLI([]string{"bogus"}) })
	if code != 2 {
		t.Errorf("hooks bogus: exit %d, want 2", code)
	}
}

func TestHooksDispatchRegistered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	if code := Dispatch([]string{"hooks", "list"}); code != 0 {
		t.Errorf("dispatch hooks list: exit %d, want 0", code)
	}
}

// hookProjectRoot creates a fake project checkout (a .git marker is what
// FindRoot walks for) with the given .hakase/hooks.json body, and chdirs
// the test into it so cliProjectRoot resolves there.
func hookProjectRoot(t *testing.T, hooksBody string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hooksBody != "" {
		dir := filepath.Join(root, ".hakase")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(hooksBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	return root
}

func TestHooksTrustFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)
	hookProjectRoot(t, `{"PreToolUse":[{"matcher":"^t$","hooks":[{"name":"proj-guard","command":["/bin/false"]}]}]}`)

	// Bare trust = review UI, exit 0.
	if code := RunHooksCLI([]string{"trust"}); code != 0 {
		t.Fatalf("trust (review): exit %d, want 0", code)
	}
	// Trust by unique name-matched... trust takes fingerprint prefixes:
	// resolve one via list is covered below; here use --all --yes.
	if code := RunHooksCLI([]string{"trust", "--all", "--yes"}); code != 0 {
		t.Fatalf("trust --all --yes: exit %d, want 0", code)
	}
	// Second run: nothing pending.
	if code := RunHooksCLI([]string{"trust", "--all", "--yes"}); code != 0 {
		t.Fatalf("trust (nothing pending): exit %d, want 0", code)
	}
	// Untrust by prefix.
	storePath := filepath.Join(home, "hooks-trust.json")
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("trust store not written: %v", err)
	}
	start := strings.Index(string(raw), "sha256:")
	if start < 0 {
		t.Fatalf("no fingerprint in store: %s", raw)
	}
	prefix := string(raw)[start : start+20]
	if code := RunHooksCLI([]string{"untrust", prefix}); code != 0 {
		t.Fatalf("untrust: exit %d, want 0", code)
	}
	if code := RunHooksCLI([]string{"untrust", prefix}); code != 1 {
		t.Fatalf("untrust (gone): exit %d, want 1", code)
	}
}

func TestHooksTrustAmbiguousAndMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)
	hookProjectRoot(t, `{"PreToolUse":[{"hooks":[{"command":["/bin/false"]}]},{"hooks":[{"command":["/bin/true"]}]}]}`)

	_, code := captureDispatchStderr(t, func() int { return RunHooksCLI([]string{"trust", "sha256:zzz"}) })
	if code != 1 {
		t.Errorf("trust (no match): exit %d, want 1", code)
	}
	// Empty hex prefix matches both pending hooks: ambiguous, must fail.
	_, code = captureDispatchStderr(t, func() int { return RunHooksCLI([]string{"trust", "sha256:"}) })
	if code != 1 {
		t.Errorf("trust (ambiguous): exit %d, want 1", code)
	}
}

func TestHooksTestDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"PreToolUse":[{"hooks":[{"name":"dry-guard","command":["/bin/sh","-c","exit 2"]}]}]}}`)
	hookProjectRoot(t, "")

	if code := RunHooksCLI([]string{"test", "dry-guard"}); code != 0 {
		t.Errorf("test dry-guard: exit %d, want 0 (report printed)", code)
	}
	_, code := captureDispatchStderr(t, func() int { return RunHooksCLI([]string{"test", "no-such-hook"}) })
	if code != 1 {
		t.Errorf("test (no match): exit %d, want 1", code)
	}
	_, code = captureDispatchStderr(t, func() int { return RunHooksCLI([]string{"test"}) })
	if code != 2 {
		t.Errorf("test (no selector): exit %d, want 2", code)
	}
}

// captureStdout runs fn while redirecting os.Stdout into a buffer.
func captureHooksStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = old
	buf := make([]byte, 16384)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n]), code
}

// TestHooksListReflectsTrust is the regression test for the missing
// trust-store wiring: list must show [trusted] after trust, not a
// permanent [UNTRUSTED].
func TestHooksListReflectsTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)
	hookProjectRoot(t, `{"PreToolUse":[{"hooks":[{"name":"g","command":["/bin/false"]}]}]}`)

	out, _ := captureHooksStdout(t, func() int { return RunHooksCLI([]string{"list"}) })
	if !strings.Contains(out, "[UNTRUSTED") {
		t.Fatalf("list before trust must flag UNTRUSTED, got:\n%s", out)
	}
	if code := RunHooksCLI([]string{"trust", "--all", "--yes"}); code != 0 {
		t.Fatalf("trust: exit %d", code)
	}
	out, _ = captureHooksStdout(t, func() int { return RunHooksCLI([]string{"list"}) })
	if !strings.Contains(out, "[trusted]") || strings.Contains(out, "[UNTRUSTED") {
		t.Fatalf("list after trust must show [trusted], got:\n%s", out)
	}
	out, _ = captureHooksStdout(t, func() int { return RunHooksCLI([]string{"trust"}) })
	if !strings.Contains(out, "everything is trusted") {
		t.Fatalf("review after trust must report nothing pending, got:\n%s", out)
	}
}

func TestScriptPreviewSuppressesBinaries(t *testing.T) {
	if got := scriptPreview(nil); got != "" {
		t.Errorf("nil argv = %q, want empty", got)
	}
	if got := scriptPreview([]string{"/nonexistent-12345/x"}); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		t.Skip("no /bin/sh")
	}
	if got := scriptPreview([]string{sh}); !strings.Contains(got, "binary") {
		t.Errorf("interpreter preview = %q, want binary suppression", got)
	}
	// A real script previews its body.
	dir := t.TempDir()
	script := filepath.Join(dir, "guard.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := scriptPreview([]string{script}); !strings.Contains(got, "echo hi") {
		t.Errorf("script preview = %q, want the body", got)
	}
}

func TestHooksAddRmCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	if code := RunHooksCLI([]string{"add", "PreToolUse", "--matcher", "^system_exec$", "--name", "cyc", "--", "/bin/true"}); code != 0 {
		t.Fatalf("hooks add: exit %d, want 0", code)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cyc"`) {
		t.Errorf("config must contain the added hook: %s", raw)
	}
	// Fingerprint prefix of /bin/true: resolve via list is stdout-only,
	// so re-derive through the runner like the trust tests do.
	_, r, err := loadHooksRunner()
	if err != nil {
		t.Fatal(err)
	}
	snaps := r.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	fp := snaps[0].Fingerprint
	if code := RunHooksCLI([]string{"disable", fp[:16]}); code != 0 {
		t.Fatalf("hooks disable: exit %d", code)
	}
	if code := RunHooksCLI([]string{"enable", fp[:16]}); code != 0 {
		t.Fatalf("hooks enable: exit %d", code)
	}
	if code := RunHooksCLI([]string{"rm", fp[:16]}); code != 0 {
		t.Fatalf("hooks rm: exit %d", code)
	}
	_, r, err = loadHooksRunner()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshots()) != 0 {
		t.Error("hook must be gone after rm")
	}
}

func TestHooksAddValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	writeHooksTestConfig(t, home, `{"provider":"openai","model_name":"m","api_key":"k"}`)

	if code := RunHooksCLI([]string{"add", "Nope", "--", "/bin/true"}); code == 0 {
		t.Error("bad event must fail")
	}
	if code := RunHooksCLI([]string{"add", "PreToolUse", "/bin/true"}); code == 0 {
		t.Error("missing -- separator must fail")
	}
	if code := RunHooksCLI([]string{"add", "PreToolUse", "--"}); code == 0 {
		t.Error("empty argv must fail")
	}
	if code := RunHooksCLI([]string{"rm", "sha256:zzz"}); code == 0 {
		t.Error("unknown prefix must fail")
	}
	if code := RunHooksCLI([]string{"off"}); code != 0 {
		t.Fatalf("hooks off: exit %d", code)
	}
	if code := RunHooksCLI([]string{"list"}); code != 0 {
		t.Fatalf("hooks list after off: exit %d", code)
	}
	if code := RunHooksCLI([]string{"on"}); code != 0 {
		t.Fatalf("hooks on: exit %d", code)
	}
}
