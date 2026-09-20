package context

import (
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
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("current", genai.RoleUser)},
	}
	cctx := &testCallbackContext{
		userContent: genai.NewContentFromText("current", genai.RoleUser),
		sessionID:   taskID,
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

func TestMemoryInjectsOncePerSession(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_1", "one")
	b.SetMemoryProvider(func(ctx agent.Context) string { return "AUTO MEMORY BLOCK" })

	texts := runMemoryCallback(t, b, "task_mem_1")
	// The block sits at the very head, ahead of persisted history and the
	// run's own contents.
	if len(texts) == 0 || texts[0] != "AUTO MEMORY BLOCK" {
		t.Fatalf("memory block not at head: %v", texts)
	}
	// Second call of the same session: not re-injected.
	for _, txt := range runMemoryCallback(t, b, "task_mem_1") {
		if strings.Contains(txt, "AUTO MEMORY BLOCK") {
			t.Fatalf("memory block re-injected on second call: %v", texts)
		}
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

func TestMemoryEmptyBlockNotMarkedSeen(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_late", "late-notes")
	block := ""
	b.SetMemoryProvider(func(ctx agent.Context) string {
		called := block
		block = "AUTO MEMORY BLOCK" // empty on the first call, non-empty after
		return called
	})

	for _, txt := range runMemoryCallback(t, b, "task_mem_late") {
		if strings.Contains(txt, "AUTO MEMORY") {
			t.Fatalf("empty provider must not inject: %v", txt)
		}
	}
	texts := runMemoryCallback(t, b, "task_mem_late")
	if len(texts) == 0 || texts[0] != "AUTO MEMORY BLOCK" {
		t.Fatalf("block written mid-session must appear on a later call: %v", texts)
	}
}

func TestMemoryPerSessionIsolation(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_A", "A")
	newMemorySession(t, svc, "task_mem_B", "B")

	calls := 0
	b.SetMemoryProvider(func(ctx agent.Context) string {
		calls++
		return "AUTO MEMORY BLOCK"
	})

	runMemoryCallback(t, b, "task_mem_A")
	runMemoryCallback(t, b, "task_mem_B")
	runMemoryCallback(t, b, "task_mem_A")
	if calls != 2 {
		t.Fatalf("provider called %d times, want 2 (once per session)", calls)
	}
}

func TestMemoryReservationAtomicUnderConcurrency(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_mem_race", "race")

	calls := 0
	var callMu sync.Mutex
	b.SetMemoryProvider(func(ctx agent.Context) string {
		callMu.Lock()
		calls++
		callMu.Unlock()
		time.Sleep(20 * time.Millisecond) // widen the check-then-act window
		return "AUTO MEMORY BLOCK"
	})

	// Concurrent model calls for the same session (e.g. PostMessage stacking
	// runs): the reservation must make the injection exactly-once, not
	// check-then-mark per callback.
	runOne := func() (int, error) {
		req := &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("current", genai.RoleUser)},
		}
		cctx := &testCallbackContext{
			userContent: genai.NewContentFromText("current", genai.RoleUser),
			sessionID:   "task_mem_race",
		}
		if _, err := b.BeforeModelCallback(cctx, req); err != nil {
			return 0, err
		}
		n := 0
		for _, c := range req.Contents {
			if len(c.Parts) > 0 && strings.Contains(c.Parts[0].Text, "AUTO MEMORY BLOCK") {
				n++
			}
		}
		return n, nil
	}

	const callbacks = 8
	var wg sync.WaitGroup
	var injected int64
	errs := make(chan error, callbacks)
	for i := 0; i < callbacks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := runOne()
			if err != nil {
				errs <- err
				return
			}
			atomic.AddInt64(&injected, int64(n))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("callback: %v", err)
	}
	if got := atomic.LoadInt64(&injected); got != 1 {
		t.Fatalf("block injected %d times across %d concurrent callbacks, want exactly 1", got, callbacks)
	}
}
