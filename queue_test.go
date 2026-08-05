package main

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestPendingQueueFIFO(t *testing.T) {
	q := newPendingQueue()
	q.push(queuedPrompt{text: "first"})
	q.push(queuedPrompt{text: "second"})
	q.push(queuedPrompt{text: "third"})

	if q.len() != 3 {
		t.Fatalf("len = %d, want 3", q.len())
	}
	for i, want := range []string{"first", "second", "third"} {
		p, ok := q.pop()
		if !ok {
			t.Fatalf("pop %d: unexpectedly empty", i)
		}
		if p.text != want {
			t.Fatalf("pop %d = %q, want %q", i, p.text, want)
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("pop on empty queue should fail")
	}
	if q.len() != 0 {
		t.Fatalf("len after drain = %d, want 0", q.len())
	}
}

func TestPendingQueueSnapshotNonDestructive(t *testing.T) {
	q := newPendingQueue()
	q.push(queuedPrompt{text: "a"})
	q.push(queuedPrompt{text: "b"})

	snap := q.snapshot()
	if len(snap) != 2 || q.len() != 2 {
		t.Fatalf("snapshot must not consume: snap=%d len=%d", len(snap), q.len())
	}
	// Mutating the returned copy must not affect the queue.
	snap[0].text = "mutated"
	got := q.snapshot()
	if got[0].text != "a" {
		t.Fatalf("snapshot returned aliased data, got %q", got[0].text)
	}
}

func TestPendingQueuePopAll(t *testing.T) {
	q := newPendingQueue()
	q.push(queuedPrompt{text: "x"})
	q.push(queuedPrompt{text: "y"})
	all := q.popAll()
	if len(all) != 2 || q.len() != 0 {
		t.Fatalf("popAll: got %d items, len %d", len(all), q.len())
	}
	if all[0].text != "x" || all[1].text != "y" {
		t.Fatalf("popAll order: %v", all)
	}
}

func TestRunControlInterrupt(t *testing.T) {
	rc := newRunControl()
	cancelled := false
	rc.setCancel(func() { cancelled = true })

	if rc.wasInterrupted() {
		t.Fatal("should not be interrupted before interrupt()")
	}
	rc.interrupt()
	if !rc.wasInterrupted() {
		t.Fatal("interrupt() must set the flag")
	}
	if !cancelled {
		t.Fatal("interrupt() must call the cancel func")
	}
	if !rc.consumeInterrupt() {
		t.Fatal("consumeInterrupt should report the pending interrupt")
	}
	if rc.wasInterrupted() {
		t.Fatal("consumeInterrupt must clear the flag")
	}
}

func TestRunControlInterruptNoCancel(t *testing.T) {
	rc := newRunControl()
	// No cancel installed (no active run): interrupt must not panic.
	rc.interrupt()
	if !rc.consumeInterrupt() {
		t.Fatal("interrupt should still set the flag for the drain merge")
	}
}

func TestSteerFraming(t *testing.T) {
	if got := steerFraming("check the docs"); !strings.Contains(got, "USER INTERJECTION") || !strings.Contains(got, "check the docs") {
		t.Fatalf("steerFraming = %q", got)
	}
	if got := steerFraming("   "); strings.TrimSpace(got) != "USER INTERJECTION (while you were working):" {
		t.Fatalf("blank text framing = %q", got)
	}
}

func TestSteeringContentParts(t *testing.T) {
	c := steeringContent(queuedPrompt{text: "use python"})
	if c.Role != genai.RoleUser {
		t.Fatalf("role = %q, want user", c.Role)
	}
	if len(c.Parts) == 0 || !strings.Contains(c.Parts[0].Text, "use python") {
		t.Fatalf("missing framing text part: %+v", c.Parts)
	}
}

func TestMergeQueued(t *testing.T) {
	qs := []queuedPrompt{
		{text: "stop doing X"},
		{text: "also check Y"},
	}
	text, atts := mergeQueued(qs)
	if text != "stop doing X\n\nalso check Y" {
		t.Fatalf("merge text = %q", text)
	}
	if len(atts) != 0 {
		t.Fatalf("no attachments expected, got %d", len(atts))
	}
}

func TestBeforeModelCallbackInjectsQueuedAtTail(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 5); err != nil {
		t.Fatal(err)
	}

	q := newPendingQueue()
	q.push(queuedPrompt{text: "steer me"})
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
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}

	q := newPendingQueue()
	q.push(queuedPrompt{text: "keep steering"})
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
	if q.len() != 1 {
		t.Fatalf("callback must not consume the queue, len = %d", q.len())
	}
}

func TestBeforeModelCallbackNilPendingQueue(t *testing.T) {
	b, svc := newTestBuilder(t)
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
