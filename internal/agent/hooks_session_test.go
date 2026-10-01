package agent

import (
	"runtime"
	"testing"

	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/hooks"

	"google.golang.org/adk/v2/agent"
)

// TestWireHookSessionStartNoop covers the guard rail: nil runners never
// install a provider. Any NON-nil runner installs one — even with no
// groups today — because Reload may add SessionStart groups later
// (spec HK-110); an empty render rolls back and injects nothing.
func TestWireHookSessionStartNoop(t *testing.T) {
	hb := hctx.NewHistoryBuilder(nil)
	wireHookSessionStart(hb, nil)
	if hb.SessionStartProvider() != nil {
		t.Error("nil runner must not install a provider")
	}

	empty, err := hooks.NewRunner(hooks.Config{})
	if err != nil {
		t.Fatal(err)
	}
	wireHookSessionStart(hb, empty)
	if hb.SessionStartProvider() == nil {
		t.Error("non-nil runner must install a provider (Reload may add groups later)")
	}

	// Tool-only user hooks plus a DISABLED project layer: still installs
	// (the user layer may gain SessionStart groups via Reload).
	off := false
	toolOnly, err := hooks.NewRunner(hooks.Config{
		PreToolUse: []hooks.Group{{Hooks: []hooks.Handler{{Command: []string{"/bin/true"}}}}},
		Project:    hooks.ProjectConfig{Enabled: &off},
	})
	if err != nil {
		t.Fatal(err)
	}
	hb2 := hctx.NewHistoryBuilder(nil)
	wireHookSessionStart(hb2, toolOnly)
	if hb2.SessionStartProvider() == nil {
		t.Error("non-nil runner must install a provider")
	}
}

// TestWireHookSessionStartInstalls pins the happy path: user SessionStart
// groups install a provider that delivers runner output into the session.
func TestWireHookSessionStartInstalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	hb := hctx.NewHistoryBuilder(nil)
	r, err := hooks.NewRunner(hooks.Config{
		SessionStart: []hooks.Group{{Hooks: []hooks.Handler{{Command: []string{"/bin/sh", "-c", "echo wired"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wireHookSessionStart(hb, r)
	prov := hb.SessionStartProvider()
	if prov == nil {
		t.Fatal("session-capable runner must install a provider")
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	// ContextMock panics on context methods; the runner's recover guards
	// must hold (this is the same mock the wiring tests use).
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("provider panicked on mock context: %v", rec)
		}
	}()
	if got, ran := prov(ctx); got != "wired" || !ran {
		t.Errorf("provider = %q, want the hook output", got)
	}
}

func TestWireHookUserPromptNoop(t *testing.T) {
	hb := hctx.NewHistoryBuilder(nil)
	wireHookUserPrompt(hb, nil)
	if hb.UserPromptProvider() != nil {
		t.Error("nil runner must not install a provider")
	}
	// Non-nil but group-less: installs anyway (Reload may add prompt
	// groups later); empty renders roll back.
	off := false
	nowhere, err := hooks.NewRunner(hooks.Config{Project: hooks.ProjectConfig{Enabled: &off}})
	if err != nil {
		t.Fatal(err)
	}
	wireHookUserPrompt(hb, nowhere)
	if hb.UserPromptProvider() == nil {
		t.Error("non-nil runner must install a provider")
	}
}

func TestWireHookUserPromptInstalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	hb := hctx.NewHistoryBuilder(nil)
	r, err := hooks.NewRunner(hooks.Config{
		UserPromptSubmit: []hooks.Group{{Hooks: []hooks.Handler{{Command: []string{"/bin/sh", "-c", "echo per-prompt"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wireHookUserPrompt(hb, r)
	prov := hb.UserPromptProvider()
	if prov == nil {
		t.Fatal("prompt-capable runner must install a provider")
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("provider panicked on mock context: %v", rec)
		}
	}()
	if got, ran := prov(ctx); got != "per-prompt" || !ran {
		t.Errorf("provider = %q, want the hook output", got)
	}
}
