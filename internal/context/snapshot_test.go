package context

// Phase 2 acceptance tests for issue #108 (PC-004/PC-005): the project
// snapshot rides the run context as a frozen turn block — fresh every
// turn, byte-identical within a turn — while the persisted user message
// stays the bare prompt so dedup is exact and the prompt appears once.

import (
	stdctx "context"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
	sesspkg "amurru/hakase/internal/session"

	agent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// snapshotCtx carries a project snapshot value on top of the standard
// test callback context — the shape agentrun builds per turn with
// project.WithSnapshot.
type snapshotCtx struct {
	agent.Context
	vals stdctx.Context
}

func (s snapshotCtx) Value(key any) any { return s.vals.Value(key) }

// assembleWithSnapshot runs the callback the way a project-bound turn
// does: the live user content is the bare prompt (agentrun no longer
// mutates it) and the snapshot rides the run context.
func assembleWithSnapshot(t *testing.T, b *HistoryBuilder, taskID, snap string, live []*genai.Content, st *fakeInvocationState) []*genai.Content {
	t.Helper()
	req := &model.LLMRequest{Contents: live}
	var uc *genai.Content
	if len(live) > 0 {
		uc = live[0]
	}
	inner := &testCallbackContext{userContent: uc, sessionID: taskID, state: st}
	cctx := snapshotCtx{Context: inner, vals: project.WithSnapshot(stdctx.Background(), snap)}
	if _, err := b.BeforeModelCallback(cctx, req); err != nil {
		t.Fatalf("callback: %v", err)
	}
	return req.Contents
}

func newSnapshotSession(t *testing.T, svc *sesspkg.SessionService, taskID, prompt string) {
	t.Helper()
	sess, err := svc.CreateSession("snap")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sess.ID, "user", prompt, "", 5, nil); err != nil {
		t.Fatal(err)
	}
	interfaces.RegisterTaskSession(taskID, sess.ID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
}

func countContaining(texts []string, needle string) int {
	n := 0
	for _, txt := range texts {
		n += strings.Count(txt, needle)
	}
	return n
}

const testSnapshotBlock = "\n### GIT WORKSPACE — session bound to registered project \"demo\"\n(checkout: /tmp/demo)\nbranch main, clean"

// TestSnapshotFrozenWithinTurnFreshAcrossTurns pins PC-005: the snapshot
// renders once per turn (a later call splices the frozen bytes even when
// the run context already carries a newer value) and refreshes every
// turn.
func TestSnapshotFrozenWithinTurnFreshAcrossTurns(t *testing.T) {
	b, svc := newTestBuilder(t)
	newSnapshotSession(t, svc, "task_snap_1", "work please")
	live := func() []*genai.Content {
		return []*genai.Content{genai.NewContentFromText("work please", genai.RoleUser)}
	}

	turn := newFakeInvocationState()
	call1 := contentTexts(assembleWithSnapshot(t, b, "task_snap_1", testSnapshotBlock+" rev1", live(), turn))
	if got := countContaining(call1, "rev1"); got != 1 {
		t.Fatalf("snapshot rev1 occurs %d times, want 1: %v", got, call1)
	}
	// Same turn, newer snapshot value on the run context: the frozen
	// payload wins — no mid-turn mutation.
	call2 := contentTexts(assembleWithSnapshot(t, b, "task_snap_1", testSnapshotBlock+" rev2", live(), turn))
	if strings.Join(call2, "\x00") != strings.Join(call1, "\x00") {
		t.Fatalf("mid-turn snapshot value rewrote the request:\n%v\nvs\n%v", call2, call1)
	}
	// New turn: the fresh snapshot renders, the old one is gone.
	turn2 := newFakeInvocationState()
	call3 := contentTexts(assembleWithSnapshot(t, b, "task_snap_1", testSnapshotBlock+" rev2", live(), turn2))
	if got := countContaining(call3, "rev2"); got != 1 {
		t.Fatalf("snapshot rev2 occurs %d times, want 1: %v", got, call3)
	}
	if got := countContaining(call3, "rev1"); got != 0 {
		t.Fatalf("stale snapshot rev1 leaked into the new turn: %v", call3)
	}
}

// TestSnapshotSinglePromptOccurrence pins the PC-005 dedup half: with
// the snapshot decoupled into a frozen block (no live user-content
// mutation), the bare persisted prompt renders exactly once from the
// store.
func TestSnapshotSinglePromptOccurrence(t *testing.T) {
	b, svc := newTestBuilder(t)
	newSnapshotSession(t, svc, "task_snap_2", "unique-snapshot-prompt-456")
	live := []*genai.Content{genai.NewContentFromText("unique-snapshot-prompt-456", genai.RoleUser)}

	got := contentTexts(assembleWithSnapshot(t, b, "task_snap_2", testSnapshotBlock, live, newFakeInvocationState()))
	if n := countContaining(got, "unique-snapshot-prompt-456"); n != 1 {
		t.Fatalf("prompt occurs %d times, want exactly 1: %v", n, got)
	}
	if n := countContaining(got, "GIT WORKSPACE"); n != 1 {
		t.Fatalf("snapshot occurs %d times, want exactly 1: %v", n, got)
	}
}

// TestNoSnapshotNoBlock pins the unbound path: without a snapshot value
// on the run context the assembly carries no workspace block.
func TestNoSnapshotNoBlock(t *testing.T) {
	b, svc := newTestBuilder(t)
	newSnapshotSession(t, svc, "task_snap_3", "plain question")
	live := []*genai.Content{genai.NewContentFromText("plain question", genai.RoleUser)}

	got := contentTexts(assemble(t, b, "task_snap_3", live, newFakeInvocationState()))
	if n := countContaining(got, "GIT WORKSPACE"); n != 0 {
		t.Fatalf("unbound assembly carries a snapshot block: %v", got)
	}
}

// TestFrozenBlockOrderMemoryThenSnapshot pins the frozen-section order
// from renderFrozenTurn (spec D5): the rendered store leads, memory
// precedes the project snapshot, and the live tail follows both.
func TestFrozenBlockOrderMemoryThenSnapshot(t *testing.T) {
	b, svc := newTestBuilder(t)
	newSnapshotSession(t, svc, "task_order_1", "order please")
	b.SetMemoryProvider(func(ctx agent.Context) string { return "MEMORY-ORDER-BLOCK" })
	live := []*genai.Content{genai.NewContentFromText("order please", genai.RoleUser)}

	got := contentTexts(assembleWithSnapshot(t, b, "task_order_1", testSnapshotBlock, live, newFakeInvocationState()))
	idxMemory, idxSnap := -1, -1
	for i, txt := range got {
		switch {
		case txt == "MEMORY-ORDER-BLOCK":
			idxMemory = i
		case strings.Contains(txt, "GIT WORKSPACE"):
			idxSnap = i
		}
	}
	if idxMemory != 1 || idxSnap != 2 {
		t.Fatalf("frozen order = %v (memory idx %d, snapshot idx %d), want store, memory, snapshot", got, idxMemory, idxSnap)
	}
}
