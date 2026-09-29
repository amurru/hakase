// hooks_test.go - `hakase hooks list` against an isolated HAKASE_HOME.
package cli

import (
	"os"
	"path/filepath"
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
