package cli

import (
	"os"
	"strings"
	"testing"
)

// captureDispatchStderr runs fn while redirecting os.Stderr into a buffer,
// returning what fn printed along with its exit code.
func captureDispatchStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	code := fn()
	_ = w.Close()
	os.Stderr = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n]), code
}

// Dispatching web/serve without the main-binary wiring hits the fallback,
// which must point at the real wiring instead of claiming "not migrated".
func TestDispatchExternalFallbackMessage(t *testing.T) {
	for _, cmd := range []string{"web", "serve"} {
		out, code := captureDispatchStderr(t, func() int { return Dispatch([]string{cmd}) })
		if code != 1 {
			t.Errorf("Dispatch(%s) without wiring: expected exit 1, got %d", cmd, code)
		}
		if strings.Contains(out, "not migrated") || strings.Contains(out, "not yet migrated") {
			t.Errorf("Dispatch(%s) output still claims not-migrated: %q", cmd, out)
		}
		if !strings.Contains(out, "main binary") {
			t.Errorf("Dispatch(%s) output should point at the main-binary wiring, got: %q", cmd, out)
		}
	}
}

// RegisterCommand lets the main binary wire the real handlers; dispatch then
// reaches the registered handler like every other subcommand.
func TestRegisterCommandOverridesFallback(t *testing.T) {
	RegisterCommand("web", "serve the web UI", func(args []string) int {
		if len(args) != 1 || args[0] != "probe" {
			t.Errorf("handler args: expected [probe], got %v", args)
		}
		return 42
	})
	defer RegisterCommand("web", "serve the web UI", unregisteredExternal("web"))

	if code := Dispatch([]string{"web", "probe"}); code != 42 {
		t.Errorf("Dispatch(web probe): expected registered handler exit 42, got %d", code)
	}
}

// The bare (no-subcommand) fallback must not claim the TUI is unwired.
func TestDispatchNoArgsFallbackMessage(t *testing.T) {
	out, code := captureDispatchStderr(t, func() int { return Dispatch(nil) })
	if code != 0 {
		t.Errorf("Dispatch(nil): expected exit 0, got %d", code)
	}
	if strings.Contains(out, "not wired") {
		t.Errorf("Dispatch(nil) output still claims unwired TUI: %q", out)
	}
}

// Unknown subcommands still report usage errors.
func TestDispatchUnknownCommand(t *testing.T) {
	_, code := captureDispatchStderr(t, func() int { return Dispatch([]string{"definitely-not-a-command"}) })
	if code != 2 {
		t.Errorf("Dispatch(unknown): expected exit 2, got %d", code)
	}
}
