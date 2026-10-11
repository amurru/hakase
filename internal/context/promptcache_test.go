package context

// Phase 1 acceptance tests for issue #106 (PC-001..PC-003): canonical
// renderer + frozen turn context + append-only assembly.
//
// Invariant P under test: every assembled request is
// [rendered store][frozen turn blocks][steering][un-persisted run tail].

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// assemble runs the callback with the given live contents and turn state,
// returning the assembled request contents.
func assemble(t *testing.T, b *HistoryBuilder, taskID string, live []*genai.Content, st *fakeInvocationState) []*genai.Content {
	t.Helper()
	req := &model.LLMRequest{Contents: live}
	var uc *genai.Content
	if len(live) > 0 {
		uc = live[0]
	}
	cctx := &testCallbackContext{userContent: uc, sessionID: taskID, state: st}
	if _, err := b.BeforeModelCallback(cctx, req); err != nil {
		t.Fatalf("callback: %v", err)
	}
	return req.Contents
}

func contentTexts(contents []*genai.Content) []string {
	var out []string
	for _, c := range contents {
		out = append(out, ContentText(c))
	}
	return out
}

// TestPrefixExtensionAcrossCallsAndTurns pins invariant P's append-only
// shape: later calls of a turn extend the earlier request's prefix, and the
// next turn's request starts with the previous turn's rendered store.
func TestPrefixExtensionAcrossCallsAndTurns(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("prefix")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_prefix"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	if err := svc.RecordUsageInSession(sess.ID, "user", "hello-turn1", "", 5, nil); err != nil {
		t.Fatal(err)
	}

	turn1 := newFakeInvocationState()
	live1 := []*genai.Content{genai.NewContentFromText("hello-turn1", genai.RoleUser)}
	call1 := assemble(t, b, taskID, live1, turn1)

	// Second call of the turn: ADK rebuilt the request with a tool event
	// after the user message.
	toolEvent := genai.NewContentFromText("tool-call-payload", genai.RoleModel)
	live2 := []*genai.Content{
		genai.NewContentFromText("hello-turn1", genai.RoleUser),
		toolEvent,
	}
	call2 := assemble(t, b, taskID, live2, turn1)

	if len(call2) != len(call1)+1 {
		t.Fatalf("call2 len = %d, want call1+1 (%d)", len(call2), len(call1)+1)
	}
	for i := range call1 {
		if ContentText(call2[i]) != ContentText(call1[i]) {
			t.Fatalf("prefix broken at %d: %q vs %q", i, ContentText(call2[i]), ContentText(call1[i]))
		}
	}

	// Next turn: the agent replied and the user asked again.
	if err := svc.RecordUsageInSession(sess.ID, "agent", "reply1", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sess.ID, "user", "hello-turn2", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	turn2 := newFakeInvocationState()
	live3 := []*genai.Content{genai.NewContentFromText("hello-turn2", genai.RoleUser)}
	call3 := assemble(t, b, taskID, live3, turn2)

	// Turn 2's request starts with turn 1's rendered store (now extended
	// by the reply), so the turn-1 prefix is intact.
	if len(call3) < len(call1) {
		t.Fatalf("call3 len = %d, want >= call1 (%d)", len(call3), len(call1))
	}
	for i := range call1 {
		if ContentText(call3[i]) != ContentText(call1[i]) {
			t.Fatalf("cross-turn prefix broken at %d: %q vs %q", i, ContentText(call3[i]), ContentText(call1[i]))
		}
	}
}

// TestFreezeOncePerTurn pins PC-002: a side-effectful source (counter plus
// time.Now) is invoked exactly once per turn, and every call of the turn
// splices byte-identical blocks.
func TestFreezeOncePerTurn(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_freeze", "freeze")
	var calls int64
	b.SetMemoryProvider(func(ctx agent.Context) string {
		n := atomic.AddInt64(&calls, 1)
		return fmt.Sprintf("frozen-%d-%d", n, time.Now().UnixNano())
	})

	turn := newFakeInvocationState()
	var first []string
	for i := 0; i < 3; i++ {
		texts := contentTexts(assemble(t, b, "task_freeze",
			[]*genai.Content{genai.NewContentFromText("current", genai.RoleUser)}, turn))
		if i == 0 {
			first = texts
		} else if strings.Join(texts, "\x00") != strings.Join(first, "\x00") {
			t.Fatalf("call %d differs from call 0:\n%v\nvs\n%v", i, texts, first)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("provider invoked %d times in one turn, want exactly 1", got)
	}
	if !strings.HasPrefix(first[1], "frozen-1-") {
		t.Fatalf("frozen block missing or misplaced: %v", first)
	}
}

// TestLiveReplayByteEquality pins PC-001: CRLF and NFD live sends persist
// normalized (LF, NFC) and replay byte-identically; the wrap policy is
// idempotent.
func TestLiveReplayByteEquality(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("bytes")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_bytes"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })

	crlf := "line1\r\nline2\rline3"
	nfd := "café" // e + combining acute (NFD)
	if err := svc.RecordUsageInSession(sess.ID, "user", crlf, "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sess.ID, "user", nfd, "", 5, nil); err != nil {
		t.Fatal(err)
	}

	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loaded.Messages[0].Content, "\r") {
		t.Fatalf("CRLF not normalized at ingestion: %q", loaded.Messages[0].Content)
	}
	if loaded.Messages[0].Content != "line1\nline2\nline3" {
		t.Fatalf("LF form = %q, want LF-only", loaded.Messages[0].Content)
	}
	if loaded.Messages[1].Content != "café" {
		t.Fatalf("NFC form = %q, want composed café", loaded.Messages[1].Content)
	}

	// Live send with the same CRLF bytes matches the normalized store
	// entry's successor state: the last persisted message is the café one,
	// so replay must carry the exact stored bytes for both.
	got := assemble(t, b, taskID,
		[]*genai.Content{genai.NewContentFromText(nfd, genai.RoleUser)},
		newFakeInvocationState())
	if len(got) != 2 {
		t.Fatalf("assembled len = %d, want 2 (store pair, live dropped)", len(got))
	}
	if want := WrapUntrustedData("line1\nline2\nline3"); ContentText(got[0]) != want {
		t.Fatalf("replay[0] = %q, want %q", ContentText(got[0]), want)
	}
	if want := WrapUntrustedData("café"); ContentText(got[1]) != want {
		t.Fatalf("replay[1] = %q, want %q", ContentText(got[1]), want)
	}

	// Wrap idempotence: stored bytes that already carry the frame are not
	// double-wrapped on replay.
	once := WrapUntrustedData("x")
	if twice := WrapUntrustedData(once); twice != once {
		t.Fatalf("wrap not idempotent: %q vs %q", twice, once)
	}
}

// TestAssembledRequestSingleUser pins PC-003: the persisted current user
// message renders from the store and the live copy is dropped, so the
// prompt occurs exactly once.
func TestAssembledRequestSingleUser(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("single")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_single"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	if err := svc.RecordUsageInSession(sess.ID, "user", "unique-prompt-123", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sess.ID, "agent", "ack", "", 5, nil); err != nil {
		t.Fatal(err)
	}

	got := assemble(t, b, taskID,
		[]*genai.Content{genai.NewContentFromText("unique-prompt-123", genai.RoleUser)},
		newFakeInvocationState())
	count := 0
	for _, c := range got {
		count += strings.Count(ContentText(c), "unique-prompt-123")
	}
	if count != 1 {
		t.Fatalf("prompt occurs %d times, want exactly 1: %v", count, contentTexts(got))
	}
}

// TestSidekickPersistOnlyStableOrder pins the PC-003/V6 fix: a mid-turn
// note is persisted once, renders from the store after the current user
// message, and never appears as a separate tail block on any call.
func TestSidekickPersistOnlyStableOrder(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("sidekick")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_sidekick"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	if err := svc.RecordUsageInSession(sess.ID, "user", "work please", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	q := util.NewSidekickNoteQueue()
	q.Add(util.SidekickNote{Severity: "warning", Text: "sidekick-note-xyz"})
	b.SetSidekickQueue(q)

	turn := newFakeInvocationState()
	live := func() []*genai.Content {
		return []*genai.Content{genai.NewContentFromText("work please", genai.RoleUser)}
	}
	call1 := contentTexts(assemble(t, b, taskID, live(), turn))
	call2 := contentTexts(assemble(t, b, taskID, live(), turn))

	for i, call := range [][]string{call1, call2} {
		occurrences := 0
		pos := -1
		for j, txt := range call {
			if strings.Contains(txt, "sidekick-note-xyz") {
				occurrences++
				pos = j
			}
			if strings.Contains(txt, "SIDEKICK ADVISORY NOTES") {
				t.Fatalf("call %d carries the legacy tail block: %v", i+1, call)
			}
		}
		if occurrences != 1 {
			t.Fatalf("call %d note occurs %d times, want 1: %v", i+1, occurrences, call)
		}
		// The note lands after the current user message (sequence order)
		// and stays there.
		userPos := -1
		for j, txt := range call {
			if strings.Contains(txt, "work please") {
				userPos = j
				break
			}
		}
		if userPos == -1 || pos != userPos+1 {
			t.Fatalf("call %d note at %d, want just after user at %d: %v", i+1, pos, userPos, call)
		}
	}
	if strings.Join(call1, "\x00") != strings.Join(call2, "\x00") {
		t.Fatalf("sidekick position flipped between calls:\n%v\nvs\n%v", call1, call2)
	}
}

// TestAssemblyOrderBlocksSteeringTail pins D1/D4: frozen blocks come before
// steering, steering before the un-persisted tool tail.
func TestAssemblyOrderBlocksSteeringTail(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("order")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_order"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	if err := svc.RecordUsageInSession(sess.ID, "user", "do the thing", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	b.SetMemoryProvider(func(ctx agent.Context) string { return "frozen-block-abc" })
	pq := util.NewPendingQueue()
	pq.Push(util.QueuedPrompt{Text: "steer- vivo-1"})
	b.SetPendingQueue(pq)

	toolTail := genai.NewContentFromText("tool-result-tail", genai.RoleModel)
	got := contentTexts(assemble(t, b, taskID, []*genai.Content{
		genai.NewContentFromText("do the thing", genai.RoleUser),
		toolTail,
	}, newFakeInvocationState()))

	idx := func(needle string) int {
		for i, txt := range got {
			if strings.Contains(txt, needle) {
				return i
			}
		}
		return -1
	}
	store, frozen, steering, tail := idx("do the thing"), idx("frozen-block-abc"), idx("steer- vivo-1"), idx("tool-result-tail")
	if store == -1 || frozen == -1 || steering == -1 || tail == -1 {
		t.Fatalf("missing section in %v", got)
	}
	if !(store < frozen && frozen < steering && steering < tail) {
		t.Fatalf("order violated (store=%d frozen=%d steering=%d tail=%d): %v",
			store, frozen, steering, tail, got)
	}
}

// TestSplitLiveTailNeverDropsToolProtocol pins the #109 hardening: a
// user-role content carrying a FunctionResponse (the durable-resume gate
// answer) is never treated as the live copy of the persisted message, even
// when the anchor is an attachment-only message that would textually match.
func TestSplitLiveTailNeverDropsToolProtocol(t *testing.T) {
	b, svc := newTestBuilder(t)
	sess, err := svc.CreateSession("toolproto")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_toolproto"
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })

	// Image-only anchor: textless content with a persisted attachment. This
	// is the shape the attachment-only match arm would accept.
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(img, []byte{0x89, 'P', 'N', 'G'}, 0644); err != nil {
		t.Fatal(err)
	}
	refs := []sesspkg.AttachmentRef{{Name: "pic.png", Path: img, MIME: "image/png"}}
	if err := svc.RecordUsageInSession(sess.ID, "user", "", "", 0, refs); err != nil {
		t.Fatal(err)
	}

	// Durable-resume payload: user role, FunctionResponse parts, no text.
	fr := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID: "call-9", Name: "clarify",
				Response: map[string]any{"user_response": "yes"},
			},
		}},
	}
	got := assemble(t, b, taskID, []*genai.Content{fr}, newFakeInvocationState())
	for _, c := range got {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				return // survived
			}
		}
	}
	t.Fatalf("resume FunctionResponse dropped from assembly: %v", contentTexts(got))
}

// TestCurrentUserMessageMatchesEmptyAttachmentAnchor pins the second half
// of the #109 guard: an empty stored prompt must not prefix-match live
// text (HasPrefix(x, "") is vacuously true), while the image-only re-send
// still matches through the dedicated arm.
func TestCurrentUserMessageMatchesEmptyAttachmentAnchor(t *testing.T) {
	msg := sesspkg.Message{
		Role:        "user",
		Content:     "",
		Attachments: []sesspkg.AttachmentRef{{Name: "x", Path: "/x", MIME: "image/png"}},
	}
	if CurrentUserMessageMatches(msg, "hello") {
		t.Fatal("non-empty live text must not match an empty-content attachment anchor")
	}
	if !CurrentUserMessageMatches(msg, "") {
		t.Fatal("image-only re-send must still match its anchor")
	}
}

// TestFrozenStoreFailureLogged pins the #109 observability: a context
// without invocation state logs the degraded frozen path instead of
// silently re-rendering.
func TestFrozenStoreFailureLogged(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_frozen_nostate", "frozen")

	var logs []string
	b.SetLogFunc(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	live := func() []*genai.Content {
		return []*genai.Content{genai.NewContentFromText("hello-frozen", genai.RoleUser)}
	}
	// nil state: the degraded path every call.
	assemble(t, b, "task_frozen_nostate", live(), nil)
	assemble(t, b, "task_frozen_nostate", live(), nil)

	hits := 0
	for _, l := range logs {
		if strings.Contains(l, "frozen turn state unavailable") {
			hits++
		}
	}
	if hits == 0 {
		t.Fatalf("degraded frozen path not logged; logs: %v", logs)
	}
}

// TestSteeringArrivalLoggedOnlyOnIncrease pins the #109 telemetry fix: a
// pre-turn steering queue is spliced once and never re-logged, and a real
// mid-turn arrival is logged exactly once.
func TestSteeringArrivalLoggedOnlyOnIncrease(t *testing.T) {
	b, svc := newTestBuilder(t)
	newMemorySession(t, svc, "task_steerlog", "steerlog")

	var logs []string
	b.SetLogFunc(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	arrivals := func() int {
		n := 0
		for _, l := range logs {
			if strings.Contains(l, "steering arrival mid-turn") {
				n++
			}
		}
		return n
	}

	pq := util.NewPendingQueue()
	pq.Push(util.QueuedPrompt{Text: "steer-one"})
	b.SetPendingQueue(pq)

	turn := newFakeInvocationState()
	live := func() []*genai.Content {
		return []*genai.Content{genai.NewContentFromText("hello-steerlog", genai.RoleUser)}
	}
	// Call 1: pre-turn steering, part of the initial prefix. Call 2: same
	// queue, no rewrite. Neither may log.
	assemble(t, b, "task_steerlog", live(), turn)
	assemble(t, b, "task_steerlog", live(), turn)
	if n := arrivals(); n != 0 {
		t.Fatalf("pre-turn steering logged %d arrivals, want 0; logs: %v", n, logs)
	}

	// Mid-turn arrival: exactly one log, on the call that first sees it.
	pq.Push(util.QueuedPrompt{Text: "steer-two"})
	got := contentTexts(assemble(t, b, "task_steerlog", live(), turn))
	if n := arrivals(); n != 1 {
		t.Fatalf("mid-turn arrival logged %d times, want 1; logs: %v", n, logs)
	}
	joined := strings.Join(got, "\x00")
	if !strings.Contains(joined, "steer-one") || !strings.Contains(joined, "steer-two") {
		t.Fatalf("both steering blocks must be spliced: %v", got)
	}

	// Later call of the same turn: still exactly one.
	assemble(t, b, "task_steerlog", live(), turn)
	if n := arrivals(); n != 1 {
		t.Fatalf("arrival re-logged on a later call (%d); logs: %v", n, logs)
	}
}
