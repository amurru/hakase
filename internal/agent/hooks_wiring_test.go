// hooks_wiring_test.go - the ADK callback adapters (spec HK-004/HK-005):
// a PreToolUse exit-2 hook short-circuits the tool with a model-legible
// denial and an audit entry; an allow passes through; PostToolUse context
// overrides the result.
package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/hooks"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

func errHookRunFailed() error { return errors.New("tool exploded") }

type fakeHookTool struct{ name string }

func (f *fakeHookTool) Name() string        { return f.name }
func (f *fakeHookTool) Description() string { return "test tool" }
func (f *fakeHookTool) IsLongRunning() bool { return false }

var _ tool.Tool = &fakeHookTool{}

func hookTestShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook spawn tests are unix-only")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	return "/bin/sh"
}

func hookTestRunner(t *testing.T, cfg hooks.Config) *hooks.Runner {
	t.Helper()
	r, err := hooks.NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func TestHookBeforeToolBlocksAndAudits(t *testing.T) {
	sh := hookTestShell(t)
	dir := t.TempDir()
	oldDir := auditLogDir
	auditLogDir = dir
	defer func() { auditLogDir = oldDir }()

	r := hookTestRunner(t, hooks.Config{PreToolUse: []hooks.Group{{Matcher: "^system_exec$", Hooks: []hooks.Handler{{
		Name:    "nope",
		Command: []string{sh, "-c", "echo denied >&2; exit 2"},
	}}}}})
	cb := makeHookBeforeToolCallback(r)
	ctx := agent.NewContext(&agent.ContextMock{})

	res, err := cb(ctx, &fakeHookTool{name: "system_exec"}, map[string]any{"command": "rm -rf /"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil {
		t.Fatal("blocking hook must return a non-nil result (ADK skips tool.Run)")
	}
	if res["blocked_by"] != "hook" {
		t.Errorf("result = %v, want blocked_by=hook", res)
	}

	// The denial must land on the always-on audit trail.
	raw, rerr := os.ReadFile(filepath.Join(dir, "exec-audit.jsonl"))
	if rerr != nil {
		t.Fatalf("read audit: %v", rerr)
	}
	if !strings.Contains(string(raw), "hook_blocked") || !strings.Contains(string(raw), "nope") {
		t.Errorf("audit = %s, want hook_blocked entry naming the hook", raw)
	}
}

func TestHookBeforeToolAllows(t *testing.T) {
	sh := hookTestShell(t)
	r := hookTestRunner(t, hooks.Config{PreToolUse: []hooks.Group{{Hooks: []hooks.Handler{{
		Command: []string{sh, "-c", "exit 0"},
	}}}}})
	cb := makeHookBeforeToolCallback(r)
	ctx := agent.NewContext(&agent.ContextMock{})

	res, err := cb(ctx, &fakeHookTool{name: "system_exec"}, nil)
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res != nil {
		t.Errorf("allowing hook must return nil (tool runs), got %v", res)
	}
}

func TestHookCallbacksDisabledRunner(t *testing.T) {
	r := hookTestRunner(t, hooks.Config{})
	ctx := agent.NewContext(&agent.ContextMock{})
	if res, _ := makeHookBeforeToolCallback(r)(ctx, &fakeHookTool{name: "t"}, nil); res != nil {
		t.Errorf("disabled runner must allow, got %v", res)
	}
	if res, _ := makeHookAfterToolCallback(r)(ctx, &fakeHookTool{name: "t"}, nil, map[string]any{"ok": true}, nil); res != nil {
		t.Errorf("disabled runner must pass through, got %v", res)
	}
}

func TestHookAfterToolOverridesWithContext(t *testing.T) {
	sh := hookTestShell(t)
	r, err := hooks.NewRunner(hooks.Config{PostToolUse: []hooks.Group{{Hooks: []hooks.Handler{{
		Command: []string{sh, "-c", `printf '{"hookSpecificOutput":{"additionalContext":"lint clean"}}'`},
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cb := makeHookAfterToolCallback(r)
	ctx := agent.NewContext(&agent.ContextMock{})

	res, err := cb(ctx, &fakeHookTool{name: "write_file"}, nil, map[string]any{"ok": true}, nil)
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil || res[hooks.AdditionalContextKey] != "lint clean" || res["ok"] != true {
		t.Errorf("override = %v, want original keys plus hook context", res)
	}
}

// TestHookAfterToolFailedPassthrough pins the error-path fix at the adapter
// level: when the tool failed, the callback must return (nil, nil) so ADK
// falls back to (fResult, fErr) and the failure reaches the model intact,
// even though a PostToolUse hook emitted context.
func TestHookAfterToolFailedPassthrough(t *testing.T) {
	sh := hookTestShell(t)
	r := hookTestRunner(t, hooks.Config{PostToolUse: []hooks.Group{{Hooks: []hooks.Handler{{
		Command: []string{sh, "-c", `printf '{"hookSpecificOutput":{"additionalContext":"lint clean"}}'`},
	}}}}})
	cb := makeHookAfterToolCallback(r)
	ctx := agent.NewContext(&agent.ContextMock{})

	res, err := cb(ctx, &fakeHookTool{name: "write_file"}, nil, map[string]any{"ok": false}, errHookRunFailed())
	if err != nil {
		t.Fatalf("callback must not manufacture an error, got %v", err)
	}
	if res != nil {
		t.Errorf("failed tool must pass through un-overridden, got %v", res)
	}
}

// TestHookToolCallbacksSharedHelper pins the contract delegate.go relies on:
// a nil runner yields nil callback slices (a nil receiver cannot serve
// calls); any NON-nil runner — even disabled — yields callbacks, so a
// later Reload takes effect without rebuilding agents (spec HK-110). The
// disabled pair no-ops; the enabled pair enforces the gate.
func TestHookToolCallbacksSharedHelper(t *testing.T) {
	if before, after := hookToolCallbacks(nil); before != nil || after != nil {
		t.Error("nil runner must yield nil callbacks")
	}
	disabled := hookTestRunner(t, hooks.Config{})
	before, after := hookToolCallbacks(disabled)
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("disabled runner must still yield one callback each (no-op pair), got %d/%d", len(before), len(after))
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	if res, err := before[0](ctx, &fakeHookTool{name: "system_exec"}, nil); res != nil || err != nil {
		t.Errorf("disabled before-callback must allow (nil,nil), got %v/%v", res, err)
	}
	if res, err := after[0](ctx, &fakeHookTool{name: "x"}, nil, map[string]any{}, nil); res != nil || err != nil {
		t.Errorf("disabled after-callback must pass through (nil,nil), got %v/%v", res, err)
	}

	sh := hookTestShell(t)
	enabled := hookTestRunner(t, hooks.Config{PreToolUse: []hooks.Group{{Hooks: []hooks.Handler{{
		Command: []string{sh, "-c", "exit 2"},
	}}}}})
	before, after = hookToolCallbacks(enabled)
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("enabled runner must yield one callback each, got %d/%d", len(before), len(after))
	}
	ctx = agent.NewContext(&agent.ContextMock{})
	res, err := before[0](ctx, &fakeHookTool{name: "system_exec"}, nil)
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil || res["blocked_by"] != "hook" {
		t.Errorf("shared before-callback must enforce the gate, got %v", res)
	}
}
