package context

import (
	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
	"path/filepath"
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

func newTestBuilder(t *testing.T) (*HistoryBuilder, *sesspkg.SessionService) {
	t.Helper()
	store, err := sesspkg.NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("sesspkg.NewSessionStore: %v", err)
	}
	svc, err := sesspkg.NewSessionService(store)
	if err != nil {
		t.Fatalf("sesspkg.NewSessionService: %v", err)
	}
	b := NewHistoryBuilder(svc)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 100_000, MaxInputTokens: 90_000})
	return b, svc
}

func TestBuildHistoryRolesAndDedup(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("user", "q2", "", 5); err != nil {
		t.Fatal(err)
	}

	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}

	// Dedup: the current user message (q2) matches ctx.UserContent.
	history := b.buildHistory(session.Messages, "q2")
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2 (q1,a1)", len(history))
	}
	if history[0].Role != genai.RoleUser {
		t.Fatalf("history[0].Role = %q, want user", history[0].Role)
	}
	if history[1].Role != genai.RoleModel {
		t.Fatalf("history[1].Role = %q, want model", history[1].Role)
	}
	if got := history[0].Parts[0].Text; got != "q1" {
		t.Fatalf("history[0] text = %q, want q1", got)
	}
}

func TestBuildHistorySummaryFirst(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()
	if err := svc.AppendSummary(id, "SUMMARY TEXT"); err != nil {
		t.Fatal(err)
	}

	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	history := b.buildHistory(session.Messages, "")
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if got := history[0].Parts[0].Text; got != "SUMMARY TEXT" {
		t.Fatalf("summary must come first, got %q", got)
	}
	if history[0].Role != genai.RoleModel {
		t.Fatalf("summary role = %q, want model", history[0].Role)
	}
}

func TestBeforeModelCallbackPrependsHistory(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 5); err != nil {
		t.Fatal(err)
	}

	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("q2", genai.RoleUser)},
	}
	cctx := &testCallbackContext{userContent: genai.NewContentFromText("q2", genai.RoleUser)}

	resp, err := b.BeforeModelCallback(cctx, req)
	if err != nil {
		t.Fatalf("callback error: %v", err)
	}
	if resp != nil {
		t.Fatalf("callback must return nil response to proceed with the real call")
	}
	// History (q1,a1) prepended in front of current (q2); q2 deduped out.
	if len(req.Contents) != 3 {
		t.Fatalf("contents len = %d, want 3", len(req.Contents))
	}
	if req.Contents[0].Parts[0].Text != "q1" || req.Contents[2].Parts[0].Text != "q2" {
		t.Fatalf("unexpected contents order: %+v", req.Contents)
	}
}

func TestBeforeModelCallbackNoHistory(t *testing.T) {
	b, _ := newTestBuilder(t)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("first", genai.RoleUser)}}
	cctx := &testCallbackContext{userContent: genai.NewContentFromText("first", genai.RoleUser)}
	// No persisted messages yet -> nothing prepended.
	resp, err := b.BeforeModelCallback(cctx, req)
	if err != nil || resp != nil {
		t.Fatalf("callback = %v, %v; want nil,nil", resp, err)
	}
	if len(req.Contents) != 1 {
		t.Fatalf("contents len = %d, want 1 (no history)", len(req.Contents))
	}
}

func TestBeforeModelCallbackNilSvc(t *testing.T) {
	b := NewHistoryBuilder(nil)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("x", genai.RoleUser)}}
	resp, err := b.BeforeModelCallback(&testCallbackContext{}, req)
	if err != nil || resp != nil {
		t.Fatalf("nil-svc callback = %v, %v; want nil,nil", resp, err)
	}
	if len(req.Contents) != 1 {
		t.Fatalf("contents modified without svc")
	}
}

func TestFitToBudgetNoTruncationUnderTrigger(t *testing.T) {
	b, svc := newTestBuilder(t)
	// 4 small messages -> well under trigger.
	for i := 0; i < 4; i++ {
		if err := svc.RecordUsage("user", "short question", "", 50); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordUsage("agent", "short answer", "", 50); err != nil {
			t.Fatal(err)
		}
	}
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	history := b.buildHistory(session.Messages, "")
	before := len(history)
	history = b.fitToBudget(session, history, []*genai.Content{genai.NewContentFromText("new", genai.RoleUser)}, &interfaces.ModelInfo{ContextWindow: 1_000_000})
	if len(history) != before {
		t.Fatalf("history truncated under trigger: %d -> %d", before, len(history))
	}
}

func TestFitToBudgetStageBSnip(t *testing.T) {
	b, svc := newTestBuilder(t)
	// Realistic window: effective max = 0.9*40000 = 36000, target = 25200.
	// reserve (8000) + current (small) leaves ~17k for history.
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 40_000})

	// 20 turns of ~1000-token messages (4000 chars) -> ~40k tokens of history,
	// well over the trigger (32400).
	for i := 0; i < 20; i++ {
		big := string(make([]byte, 4000)) // ~1000 tokens estimated
		if err := svc.RecordUsage("user", big, "", 1000); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordUsage("agent", big, "", 1000); err != nil {
			t.Fatal(err)
		}
	}

	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	history := b.buildHistory(session.Messages, "")
	trimmed := b.fitToBudget(session, history, []*genai.Content{genai.NewContentFromText("new", genai.RoleUser)}, &interfaces.ModelInfo{ContextWindow: 40_000})

	// The tail (last 2 turns) must survive verbatim.
	if len(trimmed) < 4 {
		t.Fatalf("trimmed history too small: %d", len(trimmed))
	}
	last := trimmed[len(trimmed)-1]
	if got := last.Parts[0].Text; len(got) != 4000 {
		t.Fatalf("last message truncated to %d chars, want 4000 verbatim", len(got))
	}
	// Surviving history must fit the target (0.7 * 36000 = 25200).
	target := int64(0.7 * 36000)
	if int64(util.EstimateContentsTokens(trimmed)) > target {
		t.Fatalf("trimmed history %d tokens exceeds target %d", util.EstimateContentsTokens(trimmed), target)
	}
	// History must have been reduced meaningfully (40k -> < 25k).
	if len(trimmed) >= 40 {
		t.Fatalf("snip did not evict old messages: %d kept", len(trimmed))
	}

	// Persisted session must show the older messages as out-of-context.
	session, err = svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session reload: %v %v", session, err)
	}
	sawOut := false
	for _, msg := range session.Messages {
		if !msg.InContext {
			sawOut = true
			break
		}
	}
	if !sawOut {
		t.Fatal("snip should have flipped some messages out-of-context")
	}
}

func TestStageATrimsToolResultsOnly(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q", "", 5); err != nil {
		t.Fatal(err)
	}
	// Manually append a tool transcript message.
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	session.AddMessageWithMeta("agent", "tool output here", "", 5000, sesspkg.MessageKindToolResult)
	session.AddMessageWithMeta("user", "q2", "", 5, sesspkg.MessageKindText)
	if err := svc.Store().Save(session); err != nil {
		t.Fatal(err)
	}

	session, _ = svc.GetActiveSession()
	history := b.buildHistory(session.Messages, "q2")
	trimmed := b.stageATrimToolResults(session, history, "q2")

	// Tool result removed; text messages kept.
	for _, c := range trimmed {
		if c.Parts[0].Text == "tool output here" {
			t.Fatalf("tool result still in history after stage a")
		}
	}
	foundQ := false
	for _, c := range trimmed {
		if c.Parts[0].Text == "q" {
			foundQ = true
		}
	}
	if !foundQ {
		t.Fatalf("text message wrongly trimmed by stage a")
	}
}

// TestSnipPreservesDedup verifies that when the deterministic snip rebuilds
// history, the current user message stays deduped (it is already in the run's
// contents). Without the dedup re-application this would duplicate the current
// turn inside the request.
func TestSnipPreservesDedup(t *testing.T) {
	b, svc := newTestBuilder(t)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 40_000})

	// Seed 20 turns of large messages so the snip is forced.
	for i := 0; i < 20; i++ {
		big := string(make([]byte, 4000))
		if err := svc.RecordUsage("user", big, "", 1000); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordUsage("agent", big, "", 1000); err != nil {
			t.Fatal(err)
		}
	}

	currentPrompt := "the current user message"
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	history := b.buildHistory(session.Messages, currentPrompt)
	trimmed := b.StageBSnip(session, history, currentPrompt, 1, 8000, int64(0.7*36000))

	for _, c := range trimmed {
		if c.Parts[0].Text == currentPrompt {
			t.Fatalf("current user message leaked back into history after snip")
		}
	}
}
