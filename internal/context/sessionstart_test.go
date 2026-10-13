package context

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
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

// countSessionStart counts persisted sessionstart blocks in a session (the
// persist-once idempotency marker occurrences).
func countSessionStart(sess *sesspkg.Session) int {
	n := 0
	for _, msg := range sess.Messages {
		if msg.Kind == sesspkg.MessageKindSessionStart {
			n++
		}
	}
	return n
}

// TestSessionStartReplaysAcrossRestart pins the cross-process half of
// PC-004: a fresh builder (empty hookSeen, as after a process restart)
// over a store that already carries the persisted sessionstart block
// replays it from history and never consults the provider again.
func TestSessionStartReplaysAcrossRestart(t *testing.T) {
	b1, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_hook_restart", "restart")
	b1.SetSessionStartProvider(func(ctx agent.Context) (string, bool) { return "HOOK CTX", true })
	if texts := runMemoryCallback(t, b1, "task_hook_restart"); !containsBlock(texts, "HOOK CTX") {
		t.Fatalf("first process must persist the block: %v", texts)
	}

	// Fresh builder over the same store: only the persisted marker can
	// keep the block single, the in-memory slot is empty.
	b2 := NewHistoryBuilder(svc)
	b2.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 100_000, MaxInputTokens: 90_000})
	calls := 0
	b2.SetSessionStartProvider(func(ctx agent.Context) (string, bool) {
		calls++
		return "SECOND RENDER", true
	})
	texts := runMemoryCallback(t, b2, "task_hook_restart")
	if !containsBlock(texts, "HOOK CTX") {
		t.Fatalf("persisted block must replay after restart: %v", texts)
	}
	if containsBlock(texts, "SECOND RENDER") {
		t.Fatalf("provider rendered again after restart: %v", texts)
	}
	if calls != 0 {
		t.Fatalf("provider consulted %d times after restart, want 0", calls)
	}
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := countSessionStart(loaded); n != 1 {
		t.Fatalf("persisted sessionstart messages = %d, want 1", n)
	}
}

// TestSessionStartConcurrentFirstCallsPersistOnce pins the reserve gate:
// concurrent first model calls of one session (each callback loads its own
// session copy, so no caller sees another's un-saved marker) must persist
// exactly one sessionstart message and invoke the provider exactly once.
func TestSessionStartConcurrentFirstCallsPersistOnce(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess := newMemorySession(t, svc, "task_hook_race", "race")
	var calls int64
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) {
		atomic.AddInt64(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the check-then-act window
		return "HOOK CTX", true
	})

	const runs = 8
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live := genai.NewContentFromText("current", genai.RoleUser)
			req := &model.LLMRequest{Contents: []*genai.Content{live}}
			cctx := &testCallbackContext{
				userContent: live,
				sessionID:   "task_hook_race",
				state:       newFakeInvocationState(),
			}
			if _, err := b.BeforeModelCallback(cctx, req); err != nil {
				t.Errorf("callback: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("provider invoked %d times across %d concurrent first calls, want 1", got, runs)
	}
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := countSessionStart(loaded); n != 1 {
		t.Fatalf("persisted sessionstart messages = %d, want 1", n)
	}
}

// TestSessionStartEmptySessionPersists pins the brand-new path: with no
// persisted messages yet, the hook block still lands at the store head,
// is persisted as the session's first message, and renders ahead of the
// live prompt on the first request.
func TestSessionStartEmptySessionPersists(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("brand-new-hook")
	if err != nil {
		t.Fatal(err)
	}
	interfaces.RegisterTaskSession("task_hook_empty", sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask("task_hook_empty") })
	b.SetSessionStartProvider(func(ctx agent.Context) (string, bool) { return "HOOK CTX", true })

	texts := runMemoryCallback(t, b, "task_hook_empty")
	if len(texts) != 2 || !strings.Contains(texts[0], "HOOK CTX") || texts[1] != "current" {
		t.Fatalf("empty-session hook = %v, want [hook, current]", texts)
	}
	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Kind != sesspkg.MessageKindSessionStart {
		t.Fatalf("store = %+v, want exactly one sessionstart head message", loaded.Messages)
	}
}
