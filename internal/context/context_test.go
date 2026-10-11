package context

import (
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// testCallbackContext embeds agent.ContextMock (full interface) and overrides
// the methods the callback reads: UserContent, SessionID, and State (the
// frozen-turn invocation slot, PC-002).
type testCallbackContext struct {
	agent.ContextMock
	userContent *genai.Content
	sessionID   string
	state       *fakeInvocationState
}

func (c *testCallbackContext) UserContent() *genai.Content { return c.userContent }
func (c *testCallbackContext) SessionID() string           { return c.sessionID }
func (c *testCallbackContext) State() adksession.State {
	if c.state == nil {
		return nil
	}
	return c.state
}

// fakeInvocationState is an in-memory adksession.State backing the
// frozen-turn slot in tests. One instance models one turn: callbacks
// sharing it are calls of the same turn, a fresh instance is a new turn.
type fakeInvocationState struct {
	mu   sync.Mutex
	vals map[string]any
}

func newFakeInvocationState() *fakeInvocationState {
	return &fakeInvocationState{vals: make(map[string]any)}
}

func (s *fakeInvocationState) Get(key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *fakeInvocationState) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vals == nil {
		s.vals = make(map[string]any)
	}
	s.vals[key] = value
	return nil
}

func (s *fakeInvocationState) All() iter.Seq2[string, any] {
	s.mu.Lock()
	snapshot := make(map[string]any, len(s.vals))
	for k, v := range s.vals {
		snapshot[k] = v
	}
	s.mu.Unlock()
	return func(yield func(string, any) bool) {
		for k, v := range snapshot {
			if !yield(k, v) {
				return
			}
		}
	}
}

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

	// Render-from-store (PC-001): every in-context message renders,
	// including the current user message — the live copy is dropped by
	// splitLiveTail when it matches, so dedup lives at assembly, not here.
	history := b.buildHistory(session.Messages)
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3 (q1,a1,q2)", len(history))
	}
	if history[0].Role != genai.RoleUser {
		t.Fatalf("history[0].Role = %q, want user", history[0].Role)
	}
	if history[1].Role != genai.RoleModel {
		t.Fatalf("history[1].Role = %q, want model", history[1].Role)
	}
	if want := WrapUntrustedData("q1"); history[0].Parts[0].Text != want {
		t.Fatalf("history[0] text = %q, want %q", history[0].Parts[0].Text, want)
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
	history := b.buildHistory(session.Messages)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if want := WrapUntrustedData("SUMMARY TEXT"); history[0].Parts[0].Text != want {
		t.Fatalf("summary must come first, got %q, want %q", history[0].Parts[0].Text, want)
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
	cctx := &testCallbackContext{userContent: genai.NewContentFromText("q2", genai.RoleUser), state: newFakeInvocationState()}

	resp, err := b.BeforeModelCallback(cctx, req)
	if err != nil {
		t.Fatalf("callback error: %v", err)
	}
	if resp != nil {
		t.Fatalf("callback must return nil response to proceed with the real call")
	}
	// History (q1,a1) rendered from the store in front of the unmatched
	// live (q2 was never persisted, so it is kept as the tail); q1 wrapped.
	if len(req.Contents) != 3 {
		t.Fatalf("contents len = %d, want 3", len(req.Contents))
	}
	if req.Contents[0].Parts[0].Text != WrapUntrustedData("q1") || req.Contents[2].Parts[0].Text != "q2" {
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
	var lines []string
	b.SetLogFunc(func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	history := b.buildHistory(session.Messages)
	before := len(history)
	history = b.fitToBudget(session, history, []*genai.Content{genai.NewContentFromText("new", genai.RoleUser)}, &interfaces.ModelInfo{ContextWindow: 1_000_000})
	if len(history) != before {
		t.Fatalf("history truncated under trigger: %d -> %d", before, len(history))
	}
	for _, l := range lines {
		if strings.Contains(l, "cache: rewrite") {
			t.Fatalf("no compaction must mean no rewrite log line: %q", l)
		}
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
	history := b.buildHistory(session.Messages)
	trimmed := b.fitToBudget(session, history, []*genai.Content{genai.NewContentFromText("new", genai.RoleUser)}, &interfaces.ModelInfo{ContextWindow: 40_000})

	// The tail (last 2 turns) must survive verbatim.
	if len(trimmed) < 4 {
		t.Fatalf("trimmed history too small: %d", len(trimmed))
	}
	last := trimmed[len(trimmed)-1]
	// Account for <UNTRUSTED_DATA> wrapping overhead.
	wantMin := 4000
	wantMax := 4050
	if got := len(last.Parts[0].Text); got < wantMin || got > wantMax {
		t.Fatalf("last message text length %d, want %d..%d", got, wantMin, wantMax)
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
	history := b.buildHistory(session.Messages)
	trimmed, changed := b.stageATrimToolResults(session, history)
	if !changed {
		t.Fatal("stage a must report a trim when a tool result was in context")
	}

	// Tool result removed; text messages kept.
	unwanted := WrapUntrustedData("tool output here")
	for _, c := range trimmed {
		if c.Parts[0].Text == unwanted {
			t.Fatalf("tool result still in history after stage a")
		}
	}
	foundQ := false
	wantQ := WrapUntrustedData("q")
	for _, c := range trimmed {
		if c.Parts[0].Text == wantQ {
			foundQ = true
		}
	}
	if !foundQ {
		t.Fatalf("text message wrongly trimmed by stage a")
	}
}

// TestSnipKeepsTailUserMessage verifies that when the deterministic snip
// rebuilds history, the recent persisted user messages survive (they render
// from the store; the live copy is dropped at assembly by splitLiveTail,
// so the prompt appears exactly once — see TestAssembledRequestSingleUser).
func TestSnipKeepsTailUserMessage(t *testing.T) {
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

	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	// Mark the last persisted user message so we can find it after the snip.
	lastIdx := -1
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" {
			lastIdx = i
			break
		}
	}
	if lastIdx == -1 {
		t.Fatal("no persisted user message")
	}
	session.Messages[lastIdx].Content = "the current user message"
	if err := svc.Store().Save(session); err != nil {
		t.Fatal(err)
	}
	session, _ = svc.GetActiveSession()

	history := b.buildHistory(session.Messages)
	trimmed := b.StageBSnip(session, history, 1, 8000, int64(0.7*36000))

	found := false
	for _, c := range trimmed {
		if strings.Contains(ContentText(c), "the current user message") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tail user message evicted by snip (last 2 turns must survive)")
	}
}

// TestBeforeModelCallbackPerRunSession pins the parallel-run history rule:
// a run registered in the task→session registry (every agentrun-driven run)
// injects its OWN session's history, never another run's — two Telegram
// threads running concurrently used to share whichever session was last
// activated. Unregistered contexts (the TUI loop) fall back to the active
// session, which they own exclusively.
func TestBeforeModelCallbackPerRunSession(t *testing.T) {
	b, svc := newTestBuilder(t)

	sessA, err := svc.CreateSession("A")
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := svc.CreateSession("B")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sessA.ID, "user", "question-A", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsageInSession(sessB.ID, "user", "question-B", "", 5, nil); err != nil {
		t.Fatal(err)
	}

	interfaces.RegisterTaskSession("task_A", sessA.ID)
	defer interfaces.UnregisterTask("task_A")
	interfaces.RegisterTaskSession("task_B", sessB.ID)
	defer interfaces.UnregisterTask("task_B")

	run := func(sessionID, current string) []string {
		req := &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText(current, genai.RoleUser)},
		}
		cctx := &testCallbackContext{
			userContent: genai.NewContentFromText(current, genai.RoleUser),
			sessionID:   sessionID,
			state:       newFakeInvocationState(),
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

	// Run A: history from session A only.
	textsA := run("task_A", "follow-up-A")
	if !containsText(textsA, "question-A") {
		t.Errorf("run A history missing question-A: %v", textsA)
	}
	if containsText(textsA, "question-B") {
		t.Errorf("run A leaked session B history: %v", textsA)
	}

	// Run B: the mirror image.
	textsB := run("task_B", "follow-up-B")
	if !containsText(textsB, "question-B") || containsText(textsB, "question-A") {
		t.Errorf("run B history = %v, want question-B without question-A", textsB)
	}

	// Unregistered context (sessionID "") falls back to the active session.
	if err := svc.SetActiveSession(sessA.ID); err != nil {
		t.Fatal(err)
	}
	textsFallback := run("", "follow-up-fallback")
	if !containsText(textsFallback, "question-A") {
		t.Errorf("fallback history = %v, want the active session's question-A", textsFallback)
	}
}

func containsText(texts []string, needle string) bool {
	for _, t := range texts {
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}

func TestDurablePinSurvivalReconstruction(t *testing.T) {
	b, svc := newTestBuilder(t)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 40_000})

	session, err := svc.CreateSession("Test Durable Pin")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Add a durable pin constraint early in conversation
	session.AddMessageWithMeta("user", "[CONSTRAINT] NEVER-DO: modify production DB directly. RULED-OUT: Option B.", "", 10, sesspkg.MessageKindDurablePin)
	if err := svc.Store().Save(session); err != nil {
		t.Fatalf("Save session: %v", err)
	}

	// Add many large turns to force StageBSnip compaction
	for i := 0; i < 20; i++ {
		big := string(make([]byte, 4000))
		if err := svc.RecordUsage("user", big, "", 1000); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordUsage("agent", big, "", 1000); err != nil {
			t.Fatal(err)
		}
	}

	session, _ = svc.GetActiveSession()
	history := b.buildHistory(session.Messages)
	trimmed := b.StageBSnip(session, history, 100, 8000, int64(0.7*36000))

	// Verify that the durable pin survived in trimmed history
	foundPin := false
	for _, c := range trimmed {
		if strings.Contains(c.Parts[0].Text, "NEVER-DO") && strings.Contains(c.Parts[0].Text, "RULED-OUT") {
			foundPin = true
			break
		}
	}
	if !foundPin {
		t.Errorf("durable pin constraint was wrongly evicted during compaction")
	}
}

// TestFitToBudgetLogsOneRewriteLine pins PC-006/D7: every compaction logs
// exactly one rewrite line carrying direction, tokens before/after, and
// reason (plus the stages that ran).
func TestFitToBudgetLogsOneRewriteLine(t *testing.T) {
	b, svc := newTestBuilder(t)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 40_000})
	var lines []string
	b.SetLogFunc(func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

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
	history := b.buildHistory(session.Messages)
	_ = b.fitToBudget(session, history, []*genai.Content{genai.NewContentFromText("new", genai.RoleUser)}, &interfaces.ModelInfo{ContextWindow: 40_000})

	rewrites := 0
	for _, l := range lines {
		if !strings.Contains(l, "cache: rewrite") {
			continue
		}
		rewrites++
		for _, want := range []string{"direction=shrink", "tokens ", "reason=compaction", "stages=snip"} {
			if !strings.Contains(l, want) {
				t.Fatalf("rewrite line missing %q: %q", want, l)
			}
		}
	}
	if rewrites != 1 {
		t.Fatalf("rewrite lines = %d, want exactly 1\nlogs: %v", rewrites, lines)
	}
}

// TestSummaryReplayCarriesExactlyOne pins PC-006's replay half: after two
// compactions, the rendered history contains only the newest summary.
func TestSummaryReplayCarriesExactlyOne(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 5); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()
	if err := svc.AppendSummary(id, "SUMMARY ONE"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendSummary(id, "SUMMARY TWO"); err != nil {
		t.Fatal(err)
	}

	session, err := svc.Store().Load(id)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(contentTexts(b.buildHistory(session.Messages)), "\x00")
	if strings.Contains(joined, "SUMMARY ONE") {
		t.Fatalf("prior summary replayed:\n%s", joined)
	}
	if n := strings.Count(joined, "SUMMARY TWO"); n != 1 {
		t.Fatalf("newest summary occurrences = %d, want exactly 1:\n%s", n, joined)
	}
}
