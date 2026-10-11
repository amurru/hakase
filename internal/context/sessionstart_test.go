package context

import (
	"strings"
	"testing"

	sesspkg "amurru/hakase/internal/session"

	"google.golang.org/adk/v2/agent"
)

// TestSessionStartPersistsAtStoreHead pins PC-004: the hook block is
// persisted once at the store head before the first render and replays
// from history on every later call and turn — never a frozen block.
func TestSessionStartPersistsAtStoreHead(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_hook_1", "one")
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) { return "HOOK CTX", true })

	texts := runMemoryCallback(t, b, "task_hook_1")
	// Store head first, then the rest of history, then the frozen memory
	// block ahead of the run's own contents.
	if len(texts) < 3 || !strings.Contains(texts[0], "HOOK CTX") {
		t.Fatalf("hook block not at the store head: %v", texts)
	}
	if !strings.Contains(texts[1], "hello-one") {
		t.Fatalf("history must follow the hook block: %v", texts)
	}
	if texts[2] != "AUTO MEMORY BLOCK" {
		t.Fatalf("memory block must follow history in the frozen section: %v", texts)
	}
	// Persisted exactly once, at the head, as a sessionstart message.
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) == 0 || loaded.Messages[0].Kind != sesspkg.MessageKindSessionStart {
		t.Fatalf("head message = %+v, want one sessionstart persist", loaded.Messages)
	}
	// Second turn: replays from the store (exactly one occurrence) and
	// the provider is not consulted again.
	n := 0
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) {
		n++
		return "HOOK CTX", true
	})
	texts2 := runMemoryCallback(t, b, "task_hook_1")
	count := 0
	for _, txt := range texts2 {
		if strings.Contains(txt, "HOOK CTX") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("hook block occurs %d times on turn 2, want 1: %v", count, texts2)
	}
	if n != 0 {
		t.Fatalf("provider consulted on turn 2 after persist (%d calls)", n)
	}
}

func TestSessionStartHookEmptyRollsBack(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_hook_late", "late")
	block := ""
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) {
		called := block
		block = "HOOK CTX"
		// Empty first render reports nothing-ran so the slot rolls back
		// and stays eligible (e.g. mid-session trust grant).
		return called, called != ""
	})

	for _, txt := range runMemoryCallback(t, b, "task_hook_late") {
		if strings.Contains(txt, "HOOK SESSIONSTART") {
			t.Fatalf("empty provider must not persist: %v", txt)
		}
	}
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasSessionStart(loaded) {
		t.Fatalf("nothing-ran render must persist nothing: %+v", loaded.Messages)
	}
	texts := runMemoryCallback(t, b, "task_hook_late")
	if len(texts) == 0 || !containsBlock(texts, "HOOK CTX") {
		t.Fatalf("late hook block must persist and replay: %v", texts)
	}
}

func TestSessionStartNilProviderNoop(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_hook_nil", "nil")
	// No provider set: the memory path is unaffected.
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })
	texts := runMemoryCallback(t, b, "task_hook_nil")
	if !containsBlock(texts, "AUTO MEMORY BLOCK") {
		t.Fatalf("nil hook provider must leave memory injection intact: %v", texts)
	}
}

func TestUserPromptFiresPerPromptNotPerCall(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_prompt_1", "one")
	calls := 0
	b.SetUserPromptProvider(func(ctx agent.Context) (string, bool) {
		calls++
		return "PROMPT CTX", true
	})

	texts := runMemoryCallback(t, b, "task_prompt_1")
	if !containsBlock(texts, "HOOK PROMPT CONTEXT:") {
		t.Fatalf("prompt block missing from frozen section: %v", texts)
	}
	// Same prompt, second model call of the turn: no re-fire.
	n := calls
	runMemoryCallback(t, b, "task_prompt_1")
	if calls != n {
		t.Fatalf("provider called again mid-turn (calls %d -> %d)", n, calls)
	}
	for _, txt := range runMemoryCallback(t, b, "task_prompt_1") {
		if strings.Contains(txt, "HOOK PROMPT") {
			t.Fatalf("prompt block re-injected mid-turn: %v", txt)
		}
	}
	// A new user prompt (new message, new sequence) fires again.
	if err := svc.RecordUsageInSession(sess.ID, "user", "second question", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	texts = runMemoryCallback(t, b, "task_prompt_1")
	if !containsBlock(texts, "HOOK PROMPT CONTEXT:") {
		t.Fatalf("new prompt must refire: %v", texts)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (once per prompt)", calls)
	}
}

func TestUserPromptNilProviderNoop(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_prompt_nil", "nil")
	texts := runMemoryCallback(t, b, "task_prompt_nil")
	for _, txt := range texts {
		if strings.Contains(txt, "HOOK PROMPT") {
			t.Fatalf("nil prompt provider must not inject: %v", txt)
		}
	}
}

// TestSilentHookDoesNotRefire pins the rollback-on-empty fix at the
// builder level: a provider that ran but rendered nothing (ran=true)
// settles the persist-once slot without persisting anything — only a
// nothing-ran render (ran=false) rolls the slot back.
func TestSilentHookDoesNotRefire(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_hook_silent", "one")
	calls := 0
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) {
		calls++
		return "", true
	})
	runMemoryCallback(t, b, "task_hook_silent")
	runMemoryCallback(t, b, "task_hook_silent")
	if calls != 1 {
		t.Errorf("silent provider invoked %d times, want exactly 1", calls)
	}
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasSessionStart(loaded) {
		t.Fatalf("silent hook must persist nothing: %+v", loaded.Messages)
	}
}
