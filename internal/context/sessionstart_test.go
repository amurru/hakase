package context

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
)

// TestSessionStartHookInjectsOncePerSession mirrors the memory one-shot
// contract: the hook block lands at the very head (ahead of memory) on the
// first call and never re-injects.
func TestSessionStartHookInjectsOncePerSession(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_hook_1", "one")
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })
	b.SetSessionStartProvider(func(ctx agent.Context) string { return "HOOK CTX" })

	texts := runMemoryCallback(t, b, "task_hook_1")
	if len(texts) == 0 || !strings.HasPrefix(texts[0], "HOOK SESSIONSTART CONTEXT:") {
		t.Fatalf("hook block not at head: %v", texts)
	}
	if len(texts) < 2 || texts[1] != "AUTO MEMORY BLOCK" {
		t.Fatalf("memory block must follow the hook block: %v", texts)
	}
	for _, txt := range runMemoryCallback(t, b, "task_hook_1") {
		if strings.Contains(txt, "HOOK SESSIONSTART") {
			t.Fatalf("hook block re-injected on second call: %v", txt)
		}
	}
}

func TestSessionStartHookEmptyRollsBack(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_hook_late", "late")
	block := ""
	b.SetSessionStartProvider(func(ctx agent.Context) string {
		called := block
		block = "HOOK CTX"
		return called
	})

	for _, txt := range runMemoryCallback(t, b, "task_hook_late") {
		if strings.Contains(txt, "HOOK SESSIONSTART") {
			t.Fatalf("empty provider must not inject: %v", txt)
		}
	}
	texts := runMemoryCallback(t, b, "task_hook_late")
	if len(texts) == 0 || !strings.Contains(texts[0], "HOOK CTX") {
		t.Fatalf("late hook block must appear on a later call: %v", texts)
	}
}

func TestSessionStartNilProviderNoop(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_hook_nil", "nil")
	// No provider set: the memory path is unaffected.
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })
	texts := runMemoryCallback(t, b, "task_hook_nil")
	if len(texts) == 0 || texts[0] != "AUTO MEMORY BLOCK" {
		t.Fatalf("nil hook provider must leave memory injection intact: %v", texts)
	}
}

func TestUserPromptFiresPerPromptNotPerCall(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_prompt_1", "one")
	calls := 0
	b.SetUserPromptProvider(func(ctx agent.Context) string {
		calls++
		return "PROMPT CTX"
	})

	texts := runMemoryCallback(t, b, "task_prompt_1")
	if len(texts) == 0 || !strings.HasPrefix(texts[0], "HOOK PROMPT CONTEXT:") {
		t.Fatalf("prompt block not at head: %v", texts)
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
	if len(texts) == 0 || !strings.HasPrefix(texts[0], "HOOK PROMPT CONTEXT:") {
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
