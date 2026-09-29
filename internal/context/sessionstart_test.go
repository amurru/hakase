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
