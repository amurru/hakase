package context

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"

	agent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// newMemorySession creates a persisted session with one user message and
// binds taskID to it (the way driven runs do), since SessionIDFromCtx
// resolves ADK task keys through the task→session registry.
func newMemorySession(t *testing.T, svc *sesspkg.SessionService, taskID, title string) *sesspkg.Session {
	t.Helper()
	sess, err := svc.CreateSession(title)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sess.ID, "user", "hello-"+title, "", 5, nil); err != nil {
		t.Fatal(err)
	}
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	return sess
}

func runMemoryCallback(t *testing.T, b *HistoryBuilder, taskID string) []string {
	t.Helper()
	return runMemoryCallbackWithState(t, b, taskID, newFakeInvocationState())
}

// runMemoryCallbackWithState runs the callback with an explicit turn state:
// calls sharing one state are calls of the same turn (frozen payload
// reused); a fresh state is a new turn (sources re-evaluated).
func runMemoryCallbackWithState(t *testing.T, b *HistoryBuilder, taskID string, st *fakeInvocationState) []string {
	t.Helper()
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("current", genai.RoleUser)},
	}
	cctx := &testCallbackContext{
		userContent: genai.NewContentFromText("current", genai.RoleUser),
		sessionID:   taskID,
		state:       st,
	}
	if _, err := b.BeforeModelCallback(cctx, req); err != nil {
		t.Fatalf("callback: %v", err)
	}
	var texts []string
	for _, c := range req.Contents {
		if len(c.Parts) > 0 && c.Parts[0].Text != "" {
			texts = append(texts, c.Parts[0].Text)
		}
	}
	return texts
}

// TestMemoryRendersEveryTurnFrozenWithinTurn pins PC-004: the memory
// block is re-rendered on the first call of every turn (so a remember
// write lands on the next turn) and spliced byte-identically for the
// rest of that turn. It sits after the rendered store, never prepended.
func TestMemoryRendersEveryTurnFrozenWithinTurn(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_1", "one")
	var calls int64
	b.SetMemoryProvider(func(ctx agent.Context) string {
		n := atomic.AddInt64(&calls, 1)
		return fmt.Sprintf("AUTO MEMORY BLOCK rev%d", n)
	})

	turn := newFakeInvocationState()
	texts := runMemoryCallbackWithState(t, b, "task_mem_1", turn)
	if len(texts) != 3 || texts[1] != "AUTO MEMORY BLOCK rev1" {
		t.Fatalf("memory block not in frozen section: %v", texts)
	}
	if !strings.Contains(texts[0], "hello-one") {
		t.Fatalf("rendered store must lead: %v", texts)
	}
	// Second call of the same turn: the frozen payload is spliced
	// byte-identically, the provider is not re-invoked.
	texts2 := runMemoryCallbackWithState(t, b, "task_mem_1", turn)
	if strings.Join(texts2, "\x00") != strings.Join(texts, "\x00") {
		t.Fatalf("same-turn splice differs:\n%v\nvs\n%v", texts2, texts)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("provider invoked %d times in one turn, want exactly 1", got)
	}
	// New turn: re-rendered (a remember write would land here), and the
	// stale revision does not leak across.
	texts3 := runMemoryCallback(t, b, "task_mem_1")
	if !containsBlock(texts3, "AUTO MEMORY BLOCK rev2") {
		t.Fatalf("memory block missing on the new turn: %v", texts3)
	}
	for _, txt := range texts3 {
		if strings.Contains(txt, "AUTO MEMORY BLOCK rev1") {
			t.Fatalf("stale revision leaked into the new turn: %v", texts3)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("provider invoked %d times across two turns, want 2", got)
	}
}

func TestMemoryInjectsIntoEmptySession(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("brand-new")
	if err != nil {
		t.Fatal(err)
	}
	interfaces.RegisterTaskSession("task_mem_empty", sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask("task_mem_empty") })
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })

	// No persisted messages yet: the history prepend early-returns, but the
	// memory block must still land at the head of the run's contents.
	texts := runMemoryCallback(t, b, "task_mem_empty")
	if len(texts) != 2 || texts[0] != "AUTO MEMORY BLOCK" || texts[1] != "current" {
		t.Fatalf("empty-session injection = %v, want [block, current]", texts)
	}
}

func TestMemoryNilProviderNoop(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_nil", "nil-provider")

	for _, txt := range runMemoryCallback(t, b, "task_mem_nil") {
		if strings.Contains(txt, "AUTO MEMORY") {
			t.Fatalf("nil provider must not inject: %v", txt)
		}
	}
}

// TestMemoryLateWriteVisibleNextTurn pins the PC-004 freshness half: an
// empty store renders nothing, and a note written mid-session (remember)
// is visible on the very next turn — per-turn re-render, no gate to
// roll back.
func TestMemoryLateWriteVisibleNextTurn(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_late", "late-notes")
	block := ""
	b.SetMemoryProvider(func(ctx agent.Context) string { return block })

	for _, txt := range runMemoryCallback(t, b, "task_mem_late") {
		if strings.Contains(txt, "AUTO MEMORY") {
			t.Fatalf("empty provider must not inject: %v", txt)
		}
	}
	block = "AUTO MEMORY BLOCK"
	texts := runMemoryCallback(t, b, "task_mem_late")
	if len(texts) == 0 || !containsBlock(texts, "AUTO MEMORY BLOCK") {
		t.Fatalf("block written mid-session must appear on the next turn: %v", texts)
	}
}

func containsBlock(texts []string, needle string) bool {
	for _, txt := range texts {
		if strings.Contains(txt, needle) {
			return true
		}
	}
	return false
}

// TestMemoryRendersPerSessionWithoutCrossLeak pins per-turn, per-session
// correctness: every turn re-renders, and each session's assembly carries
// only its own block.
func TestMemoryRendersPerSessionWithoutCrossLeak(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_A", "A")
	newMemorySession(t, svc, "task_mem_B", "B")

	calls := 0
	b.SetMemoryProvider(func(ctx agent.Context) string {
		calls++
		return "MEM FOR " + ctx.SessionID()
	})

	textsA := runMemoryCallback(t, b, "task_mem_A")
	textsB := runMemoryCallback(t, b, "task_mem_B")
	textsA2 := runMemoryCallback(t, b, "task_mem_A")
	if calls != 3 {
		t.Fatalf("provider called %d times, want 3 (re-rendered every turn)", calls)
	}
	if !containsBlock(textsA, "MEM FOR task_mem_A") || containsBlock(textsA, "MEM FOR task_mem_B") {
		t.Fatalf("session A leaked B's block: %v", textsA)
	}
	if !containsBlock(textsB, "MEM FOR task_mem_B") || containsBlock(textsB, "MEM FOR task_mem_A") {
		t.Fatalf("session B leaked A's block: %v", textsB)
	}
	if !containsBlock(textsA2, "MEM FOR task_mem_A") {
		t.Fatalf("session A lost its block on turn 2: %v", textsA2)
	}
}

// TestMemoryConcurrentTurnsEachRender pins per-turn semantics under
// concurrency: concurrent turns (distinct invocation states, e.g.
// stacked runs) each render their own block — the once-per-session gate
// is gone, so every turn carries it.
func TestMemoryConcurrentTurnsEachRender(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_race", "race")

	var calls int64
	b.SetMemoryProvider(func(ctx agent.Context) string {
		atomic.AddInt64(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the interleave window
		return "AUTO MEMORY BLOCK"
	})

	runOne := func() bool {
		req := &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("current", genai.RoleUser)},
		}
		cctx := &testCallbackContext{
			userContent: genai.NewContentFromText("current", genai.RoleUser),
			sessionID:   "task_mem_race",
			state:       newFakeInvocationState(),
		}
		if _, err := b.BeforeModelCallback(cctx, req); err != nil {
			return false
		}
		for _, c := range req.Contents {
			if len(c.Parts) > 0 && strings.Contains(c.Parts[0].Text, "AUTO MEMORY BLOCK") {
				return true
			}
		}
		return false
	}

	const turns = 8
	results := make([]bool, turns)
	var wg sync.WaitGroup
	for i := 0; i < turns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = runOne()
		}(i)
	}
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Fatalf("turn %d rendered no memory block", i)
		}
	}
	if got := atomic.LoadInt64(&calls); got != turns {
		t.Fatalf("provider invoked %d times across %d turns, want %d", got, turns, turns)
	}
}
