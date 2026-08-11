package main

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/tui"
	"amurru/hakase/internal/util"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// testCallbackContext embeds agent.ContextMock (full interface) and overrides
// the two methods the callback reads: UserContent and SessionID.
type testCallbackContext struct {
	agent.ContextMock
	userContent *genai.Content
	sessionID   string
}

func (c *testCallbackContext) UserContent() *genai.Content { return c.userContent }
func (c *testCallbackContext) SessionID() string           { return c.sessionID }

// newTestBuilderForQueue creates a HistoryBuilder backed by a temp session store.
func newTestBuilderForQueue(t *testing.T) (*hctx.HistoryBuilder, *sesspkg.SessionService) {
	t.Helper()
	store, err := sesspkg.NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("sesspkg.NewSessionStore: %v", err)
	}
	svc, err := sesspkg.NewSessionService(store)
	if err != nil {
		t.Fatalf("sesspkg.NewSessionService: %v", err)
	}
	b := hctx.NewHistoryBuilder(svc)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 100_000, MaxInputTokens: 90_000})
	return b, svc
}

func TestPendingQueueFIFO(t *testing.T) {
	q := util.NewPendingQueue()
	q.Push(util.QueuedPrompt{Text: "first"})
	q.Push(util.QueuedPrompt{Text: "second"})
	q.Push(util.QueuedPrompt{Text: "third"})

	if q.Len() != 3 {
		t.Fatalf("len = %d, want 3", q.Len())
	}
	for i, want := range []string{"first", "second", "third"} {
		p, ok := q.Pop()
		if !ok {
			t.Fatalf("pop %d: unexpectedly empty", i)
		}
		if p.Text != want {
			t.Fatalf("pop %d = %q, want %q", i, p.Text, want)
		}
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("pop on empty queue should fail")
	}
	if q.Len() != 0 {
		t.Fatalf("len after drain = %d, want 0", q.Len())
	}
}

func TestPendingQueueSnapshotNonDestructive(t *testing.T) {
	q := util.NewPendingQueue()
	q.Push(util.QueuedPrompt{Text: "a"})
	q.Push(util.QueuedPrompt{Text: "b"})

	snap := q.Snapshot()
	if len(snap) != 2 || q.Len() != 2 {
		t.Fatalf("snapshot must not consume: snap=%d len=%d", len(snap), q.Len())
	}
	// Mutating the returned copy must not affect the queue.
	snap[0].Text = "mutated"
	got := q.Snapshot()
	if got[0].Text != "a" {
		t.Fatalf("snapshot returned aliased data, got %q", got[0].Text)
	}
}

func TestPendingQueuePopAll(t *testing.T) {
	q := util.NewPendingQueue()
	q.Push(util.QueuedPrompt{Text: "x"})
	q.Push(util.QueuedPrompt{Text: "y"})
	all := q.PopAll()
	if len(all) != 2 || q.Len() != 0 {
		t.Fatalf("popAll: got %d items, len %d", len(all), q.Len())
	}
	if all[0].Text != "x" || all[1].Text != "y" {
		t.Fatalf("popAll order: %v", all)
	}
}

func TestRunControlInterrupt(t *testing.T) {
	rc := util.NewRunControl()
	cancelled := false
	rc.SetCancel(func() { cancelled = true })

	if rc.WasInterrupted() {
		t.Fatal("should not be interrupted before interrupt()")
	}
	rc.Interrupt()
	if !rc.WasInterrupted() {
		t.Fatal("interrupt() must set the flag")
	}
	if !cancelled {
		t.Fatal("interrupt() must call the cancel func")
	}
	if !rc.ConsumeInterrupt() {
		t.Fatal("consumeInterrupt should report the pending interrupt")
	}
	if rc.WasInterrupted() {
		t.Fatal("consumeInterrupt must clear the flag")
	}
}

func TestRunControlInterruptNoCancel(t *testing.T) {
	rc := util.NewRunControl()
	// No cancel installed (no active run): interrupt must not panic.
	rc.Interrupt()
	if !rc.ConsumeInterrupt() {
		t.Fatal("interrupt should still set the flag for the drain merge")
	}
}

func TestSteerFraming(t *testing.T) {
	if got := util.SteerFraming("check the docs"); !strings.Contains(got, "USER INTERJECTION") || !strings.Contains(got, "check the docs") {
		t.Fatalf("steerFraming = %q", got)
	}
	if got := util.SteerFraming("   "); strings.TrimSpace(got) != "USER INTERJECTION (while you were working):" {
		t.Fatalf("blank text framing = %q", got)
	}
}

func TestSteeringContentParts(t *testing.T) {
	c := util.SteeringContent(util.QueuedPrompt{Text: "use python"})
	if c.Role != genai.RoleUser {
		t.Fatalf("role = %q, want user", c.Role)
	}
	if len(c.Parts) == 0 || !strings.Contains(c.Parts[0].Text, "use python") {
		t.Fatalf("missing framing text part: %+v", c.Parts)
	}
}

func TestMergeQueued(t *testing.T) {
	qs := []util.QueuedPrompt{
		{Text: "stop doing X"},
		{Text: "also check Y"},
	}
	text, atts := tui.MergeQueued(qs)
	if text != "stop doing X\n\nalso check Y" {
		t.Fatalf("merge text = %q", text)
	}
	if len(atts) != 0 {
		t.Fatalf("no attachments expected, got %d", len(atts))
	}
}

func TestBeforeModelCallbackInjectsQueuedAtTail(t *testing.T) {
	b, svc := newTestBuilderForQueue(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 5); err != nil {
		t.Fatal(err)
	}

	q := util.NewPendingQueue()
	q.Push(util.QueuedPrompt{Text: "steer me"})
	b.SetPendingQueue(q)

	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("q2", genai.RoleUser)},
	}
	cctx := &testCallbackContext{userContent: genai.NewContentFromText("q2", genai.RoleUser)}

	if _, err := b.BeforeModelCallback(cctx, req); err != nil {
		t.Fatalf("callback error: %v", err)
	}

	// Expect [q1, a1, q2, steering]. The steering must be the LAST content
	// (most recent user intent), after the current run's contents.
	if len(req.Contents) != 4 {
		t.Fatalf("contents len = %d, want 4 (history 2 + current + steering)", len(req.Contents))
	}
	if req.Contents[0].Parts[0].Text != "q1" {
		t.Fatalf("history[0] = %q, want q1", req.Contents[0].Parts[0].Text)
	}
	last := req.Contents[len(req.Contents)-1]
	if !strings.Contains(last.Parts[0].Text, "USER INTERJECTION") || !strings.Contains(last.Parts[0].Text, "steer me") {
		t.Fatalf("steering content missing framing: %q", last.Parts[0].Text)
	}
}

func TestBeforeModelCallbackQueuePersistsAcrossCalls(t *testing.T) {
	b, svc := newTestBuilderForQueue(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}

	q := util.NewPendingQueue()
	q.Push(util.QueuedPrompt{Text: "keep steering"})
	b.SetPendingQueue(q)

	// Two model calls (tool loop): the queued message must be injected into
	// BOTH because ADK rebuilds req.Contents fresh each call and the queue is
	// only drained at agentDoneMsg.
	for call := 0; call < 2; call++ {
		req := &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("current", genai.RoleUser)},
		}
		cctx := &testCallbackContext{userContent: genai.NewContentFromText("current", genai.RoleUser)}
		if _, err := b.BeforeModelCallback(cctx, req); err != nil {
			t.Fatalf("callback %d: %v", call, err)
		}
		last := req.Contents[len(req.Contents)-1]
		if !strings.Contains(last.Parts[0].Text, "keep steering") {
			t.Fatalf("call %d: steering missing at tail: %q", call, last.Parts[0].Text)
		}
	}
	if q.Len() != 1 {
		t.Fatalf("callback must not consume the queue, len = %d", q.Len())
	}
}

func TestBeforeModelCallbackNilPendingQueue(t *testing.T) {
	b, svc := newTestBuilderForQueue(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("q2", genai.RoleUser)},
	}
	cctx := &testCallbackContext{userContent: genai.NewContentFromText("q2", genai.RoleUser)}
	if _, err := b.BeforeModelCallback(cctx, req); err != nil {
		t.Fatalf("callback error: %v", err)
	}
	if len(req.Contents) != 2 {
		t.Fatalf("contents len = %d, want 2 (no queue attached)", len(req.Contents))
	}
}
