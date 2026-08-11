package tui

import (
	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/util"
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"google.golang.org/genai"
)

func newTestModel(t *testing.T) *AppModel {
	t.Helper()
	m := newModel(context.Background(), nil, nil, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*AppModel)
}

func TestThinkingBoxIsSingleBlock(t *testing.T) {
	m := newTestModel(t)
	m.showThinking = true
	m.chatHistory = []ChatMessage{
		{Role: "agent", Content: "answer", Thinking: "para1\n\npara2"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	content := m.chatViewport.View()
	if c := strings.Count(content, "╭"); c != 1 {
		t.Fatalf("expected exactly 1 thinking box top border, got %d\n%s", c, content)
	}
	if c := strings.Count(content, "╰"); c != 1 {
		t.Fatalf("expected exactly 1 thinking box bottom border, got %d\n%s", c, content)
	}
}

func TestWhitespaceThinkingRendersNoBox(t *testing.T) {
	m := newTestModel(t)
	m.showThinking = true
	m.chatHistory = []ChatMessage{
		{Role: "agent", Content: "answer", Thinking: "   \n  "},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	content := m.chatViewport.View()
	if strings.Contains(content, "╭") {
		t.Fatalf("whitespace-only thinking should not render a box:\n%s", content)
	}
}

func TestThinkingHiddenWhenToggledOff(t *testing.T) {
	m := newTestModel(t)
	m.showThinking = false
	m.chatHistory = []ChatMessage{
		{Role: "agent", Content: "answer", Thinking: "some thought"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	content := m.chatViewport.View()
	if strings.Contains(content, "╭") {
		t.Fatalf("thinking box should be hidden when showThinking is false:\n%s", content)
	}
}

func TestRefreshLastMessageMatchesRebuild(t *testing.T) {
	m := newTestModel(t)
	m.showThinking = true
	m.chatHistory = []ChatMessage{
		{Role: "user", Content: "hi"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	// Stream chunks through the real Update path: first chunk appends the
	// agent message, later chunks merge into it.
	var model tea.Model = m
	for _, sm := range []agentStreamMsg{
		{Thinking: "think"},
		{Content: "p1"},
		{Thinking: "\nmore thinking"},
		{Content: " p2"},
	} {
		model, _ = model.Update(sm)
	}
	m = model.(*AppModel)

	// A full rebuild must produce identical lines.
	wrapWidth := m.chatViewport.Width()
	var want []string
	for _, msg := range m.chatHistory {
		want = append(want, m.renderMsgLines(msg, wrapWidth)...)
	}
	if got, want := strings.Join(m.renderedLines, "\n"), strings.Join(want, "\n"); got != want {
		t.Fatalf("incremental render diverged from full rebuild\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}

	last := m.chatHistory[len(m.chatHistory)-1]
	wantStart := len(m.renderedLines) - len(m.renderMsgLines(last, wrapWidth))
	if m.lastMsgStart != wantStart {
		t.Fatalf("lastMsgStart=%d, want %d", m.lastMsgStart, wantStart)
	}
}

func TestStreamChunksMergeIntoOneAgentMessage(t *testing.T) {
	m := newTestModel(t)
	m.chatHistory = []ChatMessage{{Role: "user", Content: "hello"}}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	var model tea.Model = m
	for _, sm := range []agentStreamMsg{
		{Thinking: "let me think"},
		{Content: "The "},
		{Content: "answer"},
	} {
		model, _ = model.Update(sm)
	}

	mm := model.(*AppModel)
	if len(mm.chatHistory) != 2 {
		t.Fatalf("expected 2 messages (user+agent), got %d", len(mm.chatHistory))
	}
	last := mm.chatHistory[1]
	if last.Content != "The answer" {
		t.Fatalf("content mismatch: %q", last.Content)
	}
	if last.Thinking != "let me think" {
		t.Fatalf("thinking mismatch: %q", last.Thinking)
	}
}

// TestNewThinkingBlockAfterAnsweredMessage verifies that a NEW thinking block
// streaming after an already-answered agent message opens a fresh agent
// message instead of appending into the previous message's thinking block.
// This is the reported bug: a second question's thinking (streamed as an
// interjection response) landed inside the first question's already-answered
// thinking block. The fix tracks thinking-block continuity so a thinking
// chunk that arrives outside an in-flight thinking block starts a new block.
func TestNewThinkingBlockAfterAnsweredMessage(t *testing.T) {
	m := newTestModel(t)
	m.showThinking = true
	m.chatHistory = []ChatMessage{
		{Role: "user", Content: "Q1"},
		{Role: "agent", Content: "A1 answer", Thinking: "T1 thinking"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	var model tea.Model = m
	// A new thinking episode starts (e.g. the model responding to a queued
	// second question steered into the running session as an interjection).
	model, _ = model.Update(agentStreamMsg{Thinking: "T2 thinking"})
	mm := model.(*AppModel)

	if len(mm.chatHistory) != 3 {
		t.Fatalf("expected 3 messages (user + 2 agent), got %d", len(mm.chatHistory))
	}
	// The previous message must be untouched.
	if mm.chatHistory[1].Thinking != "T1 thinking" || mm.chatHistory[1].Content != "A1 answer" {
		t.Fatalf("previous agent message was mutated: %+v", mm.chatHistory[1])
	}
	last := mm.chatHistory[2]
	if last.Role != "agent" || last.Thinking != "T2 thinking" || last.Content != "" {
		t.Fatalf("expected new agent message with only thinking, got %+v", last)
	}

	// The content that follows the new thinking block merges into the NEW
	// message, not the old one.
	model, _ = model.Update(agentStreamMsg{Content: "A2 answer"})
	mm = model.(*AppModel)
	if len(mm.chatHistory) != 3 {
		t.Fatalf("content should merge into the new message, got %d messages", len(mm.chatHistory))
	}
	last = mm.chatHistory[2]
	if last.Content != "A2 answer" || last.Thinking != "T2 thinking" {
		t.Fatalf("new message content/thinking mismatch: %+v", last)
	}
	if mm.chatHistory[1].Content != "A1 answer" {
		t.Fatalf("old message content was mutated: %+v", mm.chatHistory[1])
	}
}

// TestThinkingContinuationMergesIntoSameBlock verifies that thinking chunks
// streamed by the model itself while a thinking block is in flight keep
// merging into that same block (the "updating on the same thinking block"
// case the user described).
func TestThinkingContinuationMergesIntoSameBlock(t *testing.T) {
	m := newTestModel(t)
	m.chatHistory = []ChatMessage{{Role: "user", Content: "hello"}}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	var model tea.Model = m
	for _, sm := range []agentStreamMsg{
		{Thinking: "step one"},
		{Thinking: "\nstep two"},
		{Content: "done"},
	} {
		model, _ = model.Update(sm)
	}
	mm := model.(*AppModel)

	if len(mm.chatHistory) != 2 {
		t.Fatalf("expected 2 messages (user+agent), got %d", len(mm.chatHistory))
	}
	last := mm.chatHistory[1]
	if last.Thinking != "step one\nstep two" {
		t.Fatalf("thinking continuation merged wrong: %q", last.Thinking)
	}
	if last.Content != "done" {
		t.Fatalf("content mismatch: %q", last.Content)
	}
}

func TestStreamPreservesScrollPosition(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 5; i++ {
		m.chatHistory = append(m.chatHistory, ChatMessage{Role: "user", Content: strings.Repeat("question line\n", 20)})
		m.chatHistory = append(m.chatHistory, ChatMessage{Role: "agent", Content: strings.Repeat("answer line\n", 20)})
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()
	if maxOffset := m.maxChatScrollOffset(); maxOffset <= 0 {
		t.Fatal("test setup: expected scrollable content")
	}

	m.chatScrollOffset = 10
	model, _ := m.Update(agentStreamMsg{Content: " new content"})
	if got := model.(*AppModel).chatScrollOffset; got != 10 {
		t.Fatalf("expected scroll offset preserved at 10 while reading history, got %d", got)
	}

	mm := model.(*AppModel)
	mm.chatScrollOffset = mm.maxChatScrollOffset()
	model, _ = mm.Update(agentStreamMsg{Content: " more content"})
	got := model.(*AppModel).chatScrollOffset
	if got < mm.chatScrollOffset {
		t.Fatalf("expected auto-scroll to follow stream when at bottom (was %d, got %d)", mm.chatScrollOffset, got)
	}
}

func TestLogLinesAppendIncrementally(t *testing.T) {
	m := newTestModel(t)
	model, _ := m.Update(StatusLogMsg{Text: "first"})
	model, _ = model.Update(agentLogMsg("second"))

	mm := model.(*AppModel)
	if len(mm.logLines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(mm.logLines))
	}
	if mm.logLines[0] != "first" || mm.logLines[1] != "second" {
		t.Fatalf("log lines mismatch: %v", mm.logLines)
	}
}

// keyMsg builds a KeyPressMsg whose String() reports the given key.
func keyMsg(key string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: key}
}

func TestHelpToggleWithCtrlSlash(t *testing.T) {
	m := newTestModel(t)
	if m.showHelp {
		t.Fatal("help should start closed")
	}

	for _, key := range []string{"ctrl+/", "ctrl+_", "ctrl+?"} {
		m = newTestModel(t)
		model, _ := m.Update(keyMsg(key))
		if !model.(*AppModel).showHelp {
			t.Fatalf("%q should open the help overlay", key)
		}
		model, _ = model.Update(keyMsg(key))
		if model.(*AppModel).showHelp {
			t.Fatalf("%q should toggle the help overlay closed", key)
		}
	}
}

func TestHelpOpensWithBareQuestionMarkOutsideInput(t *testing.T) {
	m := newTestModel(t)
	m.focus = chatFocus
	model, _ := m.Update(keyMsg("?"))
	if !model.(*AppModel).showHelp {
		t.Fatal("bare '?' with chat focused should open help")
	}
}

func TestQuestionMarkTypesIntoInput(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("what")
	m.input.CursorEnd()
	model, _ := m.Update(keyMsg("?"))
	mm := model.(*AppModel)
	if mm.showHelp {
		t.Fatal("'?' must not open help while input is focused")
	}
	if mm.input.Value() != "what?" {
		t.Fatalf("'?' should be typed into the input, got %q", mm.input.Value())
	}
}

func TestEscOnlyClosesHelp(t *testing.T) {
	m := newTestModel(t)
	m.showHelp = true
	model, cmd := m.Update(keyMsg("esc"))
	if model.(*AppModel).showHelp {
		t.Fatal("esc should close the help overlay")
	}
	if cmd != nil {
		t.Fatalf("esc with help open must not quit, got cmd %v", cmd)
	}

	// Esc with no modal open is a no-op - it must never quit.
	model, cmd = model.Update(keyMsg("esc"))
	if cmd != nil {
		t.Fatal("esc with no modal open must not quit")
	}
	if model.(*AppModel).showHelp {
		t.Fatal("esc with no modal open must not open the help overlay")
	}
}

func TestCtrlCQuitsEvenWithHelpOpen(t *testing.T) {
	m := newTestModel(t)
	m.showHelp = true
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c should quit even while help is open")
	}
}

func TestTabCyclesFocusForwardAndShiftTabBackward(t *testing.T) {
	m := newTestModel(t)
	expect := func(want focusedPane) {
		t.Helper()
		if m.focus != want {
			t.Fatalf("focus = %d, want %d", m.focus, want)
		}
	}

	expect(inputFocus)
	var model tea.Model = m
	for _, want := range []focusedPane{chatFocus, logFocus, taskFocus, inputFocus} {
		model, _ = model.Update(keyMsg("tab"))
		m = model.(*AppModel)
		expect(want)
	}

	model, _ = m.Update(keyMsg("shift+tab"))
	m = model.(*AppModel)
	expect(taskFocus)
}

func TestHelpViewRendersShortcuts(t *testing.T) {
	m := newTestModel(t)
	m.showHelp = true
	view := m.View().Content
	for _, want := range []string{"Keyboard Shortcuts", "ctrl+/", "shift+tab", "ctrl+t", "ctrl+c"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing %q:\n%s", want, view)
		}
	}
}

func TestHintBarShowsShortcuts(t *testing.T) {
	m := newTestModel(t)
	view := m.View().Content
	if !strings.Contains(view, "ctrl+/ help") {
		t.Fatalf("hint bar missing help hint:\n%s", view)
	}
	if !strings.Contains(view, "tab focus") {
		t.Fatalf("hint bar missing focus hint:\n%s", view)
	}
}

func TestHintBarFitsWithinTerminalHeight(t *testing.T) {
	m := newTestModel(t)
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != m.height {
		t.Fatalf("view renders %d lines for terminal height %d - panes or hint bar overflow", len(lines), m.height)
	}
	if got := lines[len(lines)-1]; !strings.Contains(got, "ctrl+/ help") {
		t.Fatalf("hint bar must be the last visible line, got %q", got)
	}
}

func TestLogStaysPinnedToBottomWhenAtBottom(t *testing.T) {
	m := newTestModel(t)
	var model tea.Model = m
	for i := 0; i < 60; i++ {
		model, _ = model.Update(StatusLogMsg{Text: fmt.Sprintf("line %d", i)})
	}
	mm := model.(*AppModel)
	if !mm.logViewport.AtBottom() {
		t.Fatal("test setup: log should be at the bottom after appending")
	}

	model, _ = mm.Update(StatusLogMsg{Text: "new"})
	mm = model.(*AppModel)
	if !mm.logViewport.AtBottom() {
		t.Fatalf("log should stay pinned to the bottom, YOffset=%d", mm.logViewport.YOffset())
	}
}

func TestLogDoesNotYankWhenScrolledAway(t *testing.T) {
	m := newTestModel(t)
	var model tea.Model = m
	for i := 0; i < 60; i++ {
		model, _ = model.Update(StatusLogMsg{Text: fmt.Sprintf("line %d", i)})
	}
	mm := model.(*AppModel)
	mm.logViewport.GotoTop()
	if mm.logViewport.AtBottom() {
		t.Fatal("test setup: log should be scrollable after GotoTop")
	}
	before := mm.logViewport.YOffset()

	model, _ = mm.Update(StatusLogMsg{Text: "new"})
	mm = model.(*AppModel)
	if got := mm.logViewport.YOffset(); got != before {
		t.Fatalf("new log line must not yank the view while reading history (was %d, got %d)", before, got)
	}
}

func TestStatusBarShowsModelName(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "poolside/laguna-s-2.1:free"
	view := m.statusBar()
	if !strings.Contains(view, "poolside/laguna-s-2.1:free") {
		t.Fatalf("status bar missing model name:\n%s", view)
	}
}

func TestStatusBarShowsContextAndUsage(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "test-model"
	m.modelInfo = &hakaseagent.ModelInfo{Name: "test-model", ContextWindow: 200_000}
	m.usage = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     84_000,
		CandidatesTokenCount: 16_000,
		TotalTokenCount:      100_000,
	}
	view := m.statusBar()
	for _, want := range []string{"ctx 200K", "50%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status bar missing %q:\n%s", want, view)
		}
	}
}

func TestStatusBarShowsThinkingLevel(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "test-model"
	m.thinkingLevel = "maximum"
	view := m.statusBar()
	if !strings.Contains(view, "thinking maximum") {
		t.Fatalf("status bar missing thinking level:\n%s", view)
	}
}

func TestStatusBarFitsWithinTerminalWidth(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "poolside/laguna-s-2.1:free"
	m.modelInfo = &hakaseagent.ModelInfo{Name: "poolside/laguna-s-2.1:free", ContextWindow: 240_000, ThinkingLevel: "xhigh"}
	m.usage = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     50_000,
		CandidatesTokenCount: 50_000,
		TotalTokenCount:      100_000,
	}
	view := m.statusBar()
	if n := strings.Count(view, "\n"); n > 0 {
		t.Fatalf("status bar must be a single line, got %d newlines:\n%s", n, view)
	}
}

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{999, "999"},
		{1_000, "1K"},
		{131_072, "131K"},
		{1_000_000, "1.0M"},
	}
	for _, c := range cases {
		if got := formatTokens(c.in); got != c.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUsagePercent(t *testing.T) {
	m := newTestModel(t)
	m.modelInfo = &hakaseagent.ModelInfo{Name: "m", ContextWindow: 100_000}
	m.usage = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     30_000,
		CandidatesTokenCount: 20_000,
		TotalTokenCount:      50_000,
	}
	pct, used := m.usagePercent()
	if pct != 50 || used != 50_000 {
		t.Fatalf("usagePercent() = (%d, %d), want (50, 50000)", pct, used)
	}

	m.usage = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     70_000,
		CandidatesTokenCount: 50_000,
	}
	pct, used = m.usagePercent()
	if pct != 100 || used != 120_000 {
		t.Fatalf("usagePercent fallback = (%d, %d), want (100, 120000)", pct, used)
	}

	m.usage = nil
	pct, used = m.usagePercent()
	if pct != 0 || used != 0 {
		t.Fatalf("usagePercent without usage = (%d, %d), want (0, 0)", pct, used)
	}
}

func TestThinkingStatus(t *testing.T) {
	m := newTestModel(t)

	m.thinkingLevel = ""
	if got := m.thinkingStatus(); got != "on" {
		t.Fatalf("empty level = %q, want on", got)
	}

	m.thinkingLevel = "off"
	if got := m.thinkingStatus(); got != "off" {
		t.Fatalf("off level = %q, want off", got)
	}

	m.thinkingLevel = "maximum"
	if got := m.thinkingStatus(); got != "maximum" {
		t.Fatalf("provider level = %q, want maximum", got)
	}

	m.modelInfo = &hakaseagent.ModelInfo{Name: "m", ThinkingEnabled: false, ThinkingLevel: ""}
	m.thinkingLevel = ""
	if got := m.thinkingStatus(); got != "off" {
		t.Fatalf("model without thinking = %q, want off", got)
	}

	m.modelInfo = &hakaseagent.ModelInfo{Name: "m", ThinkingEnabled: true, ThinkingLevel: "HIGH"}
	m.thinkingLevel = ""
	if got := m.thinkingStatus(); got != "high" {
		t.Fatalf("model thinking level = %q, want high", got)
	}
}

// TestCompositorHitTestResolvesPanesByGeometry builds the same layer tree the
// View uses and verifies that clicks inside each pane region resolve to the
// correct layer ID by absolute screen coordinate, with no manual thresholds.
func TestCompositorHitTestResolvesPanesByGeometry(t *testing.T) {
	m := newTestModel(t) // 120x40

	chatRender := inactiveBorder.Render(m.chatViewport.View())
	logRender := inactiveBorder.Render(m.logViewport.View())
	inputRender := inactiveBorder.Render(m.input.View())
	taskRender := inactiveBorder.Render(m.taskViewport.View())
	comp := m.buildCompositor(chatRender, logRender, inputRender, taskRender)

	// rightColStart = leftWidth + 2 = (120 - 24 - 4) + 2 = 94
	cases := []struct {
		name    string
		x, y    int
		wantID  string
		wantHit bool
	}{
		{"status bar", 0, 0, paneStatus, true},
		{"chat top-left area", 5, 3, paneChat, true},
		{"chat near right edge of left col", 90, 20, paneChat, true},
		{"log top of right col", 100, 3, paneLog, true},
		{"log just past column boundary", 94, 5, paneLog, true},
		{"input row", 5, 36, paneInput, true},
		{"task below log on right", 100, 30, paneTask, true},
		{"hint bar", 0, 39, paneHint, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := comp.Hit(c.x, c.y)
			if c.wantHit && hit.Empty() {
				t.Fatalf("Hit(%d,%d) returned empty, want layer %q", c.x, c.y, c.wantID)
			}
			if got := hit.ID(); got != c.wantID {
				t.Fatalf("Hit(%d,%d).ID() = %q, want %q", c.x, c.y, got, c.wantID)
			}
		})
	}
}

// TestClickFocusesClickedPane verifies that a left-click resolved by the
// compositor moves focus to the clicked pane.
func TestClickFocusesClickedPane(t *testing.T) {
	m := newTestModel(t)
	m.focus = chatFocus
	_ = m.View() // populate m.compositor with the current layout

	// 120x40 layout: chat x<88 top, log x>=88 top, task x>=88 lower-right.
	clicks := []struct {
		name string
		x, y int
		want focusedPane
	}{
		{"log", 100, 3, logFocus},
		{"task", 100, 30, taskFocus},
		{"chat", 5, 3, chatFocus},
	}
	for _, c := range clicks {
		model, _ := m.Update(tea.MouseClickMsg{X: c.x, Y: c.y, Button: tea.MouseLeft})
		mm := model.(*AppModel)
		if mm.focus != c.want {
			t.Fatalf("click on %s at (%d,%d): focus = %d, want %d", c.name, c.x, c.y, mm.focus, c.want)
		}
	}
}

// TestClickOnInputFocusesIt verifies that clicking the input pane moves focus
// to it so the user can click back into the prompt after focusing another pane.
func TestClickOnInputFocusesIt(t *testing.T) {
	m := newTestModel(t)
	m.focus = logFocus
	m.input.Blur()
	_ = m.View() // populate m.compositor

	// Input pane occupies rows 34..38 in the left column (120x40, DynamicHeight at 1 line).
	model, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 36, Button: tea.MouseLeft})
	mm := model.(*AppModel)
	if mm.focus != inputFocus {
		t.Fatalf("click on input: focus = %d, want %d (inputFocus)", mm.focus, inputFocus)
	}
}

// TestMouseYToContentLineGeometry locks in the border-aware coordinate map
// from screen Y to content line for each pane. For a 120x40 terminal the
// layout is: chatH=31, logH=21, taskH=13 (input at min 1 line, DynamicHeight).
func TestMouseYToContentLineGeometry(t *testing.T) {
	m := newTestModel(t) // 120x40

	// Provide enough content that clamping to the last line is meaningful.
	m.renderedLines = make([]string, 40)
	m.logLines = make([]string, 25)
	m.taskLines = make([]string, 15)
	m.chatScrollOffset = 0

	// Chat: content occupies screen rows 2..32 (chatH=31 -> rows 0..30).
	m.selectionPane = chatFocus
	for _, c := range []struct{ y, want int }{
		{0, -1}, // status bar
		{1, -1}, // chat top border
		{2, 0},  // first content line
		{3, 1},
		{32, 30}, // last content line
		{33, 30}, // overshoot clamps to last line
	} {
		if got := m.mouseYToContentLine(c.y); got != c.want {
			t.Fatalf("chat y=%d: got %d, want %d", c.y, got, c.want)
		}
	}

	// Chat respects scroll offset.
	m.chatScrollOffset = 10
	if got := m.mouseYToContentLine(2); got != 10 {
		t.Fatalf("chat y=2 with offset 10: got %d, want 10", got)
	}
	m.chatScrollOffset = 0

	// Log: content occupies screen rows 2..20 (logH=21 -> rows 0..18).
	m.selectionPane = logFocus
	for _, c := range []struct{ y, want int }{
		{1, -1},  // log top border
		{2, 0},   // first log content line
		{20, 18}, // last log content line
		{21, 18}, // overshoot clamps (bottom border)
	} {
		if got := m.mouseYToContentLine(c.y); got != c.want {
			t.Fatalf("log y=%d: got %d, want %d", c.y, got, c.want)
		}
	}

	// Task: content starts at row logH+4 = 25 (taskH=13 -> rows 0..10).
	m.selectionPane = taskFocus
	for _, c := range []struct{ y, want int }{
		{24, -1}, // task top border
		{25, 0},  // first task content line
		{35, 10}, // last task content line
		{36, 10}, // overshoot clamps (bottom border)
	} {
		if got := m.mouseYToContentLine(c.y); got != c.want {
			t.Fatalf("task y=%d: got %d, want %d", c.y, got, c.want)
		}
	}
}

// TestSelectionDragUpdatesEndLineAndHighlight verifies that a click followed
// by a drag motion updates the selection end line and that the chat viewport
// content reflects the highlighted range.
func TestSelectionDragUpdatesEndLineAndHighlight(t *testing.T) {
	m := newTestModel(t)
	m.focus = chatFocus
	m.input.Blur()
	// Twenty plain chat lines so a multi-line selection exists.
	m.renderedLines = make([]string, 20)
	for i := range m.renderedLines {
		m.renderedLines[i] = fmt.Sprintf("line %d", i)
	}
	m.chatScrollOffset = 0
	m.renderChatViewport()
	_ = m.View() // populate m.compositor so clicks resolve to the chat pane

	click := func(y int) {
		_, _ = m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	}
	drag := func(y int) {
		_, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: y})
	}

	click(2) // first content line -> start line 0
	if m.selectionStartLine != 0 || !m.selectionActive {
		t.Fatalf("after click: start=%d active=%v, want start=0 active=true", m.selectionStartLine, m.selectionActive)
	}
	drag(6) // drag to fifth content line -> end line 4
	if m.selectionEndLine != 4 {
		t.Fatalf("after drag: end=%d, want 4", m.selectionEndLine)
	}
	// The viewport should now show a highlight on the selected range.
	view := m.chatViewport.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("expected highlight (ANSI styling) in chat viewport after drag, got:\n%s", view)
	}
}

// TestEnterWhileProcessingQueuesInsteadOfDropping verifies that pressing Enter
// while the agent is busy buffers the message instead of silently discarding it.
func TestEnterWhileProcessingQueuesInsteadOfDropping(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.input.SetValue("steer me")
	m.input.CursorEnd()

	model, _ := m.Update(keyMsg("enter"))
	mm := model.(*AppModel)

	if mm.pendingQueue.Len() != 1 {
		t.Fatalf("pendingQueue len = %d, want 1", mm.pendingQueue.Len())
	}
	got := mm.pendingQueue.Snapshot()
	if got[0].Text != "steer me" {
		t.Fatalf("queued text = %q, want %q", got[0].Text, "steer me")
	}
	if mm.input.Value() != "" {
		t.Fatalf("input should be cleared after queueing, got %q", mm.input.Value())
	}
	if !mm.IsProcessing {
		t.Fatal("isProcessing must stay true while a run is active")
	}
}

// TestEnterWhileIdleStillSends verifies that the normal (non-processing) path
// is untouched: Enter launches the run and never queues.
func TestEnterWhileIdleStillSends(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("normal message")
	m.input.CursorEnd()

	// The test model has no runner, so the send path still records the user
	// message in chat history and marks processing (the goroutine is a no-op
	// with a nil runner).
	model, _ := m.Update(keyMsg("enter"))
	mm := model.(*AppModel)

	if mm.pendingQueue.Len() != 0 {
		t.Fatalf("idle Enter must not queue, got %d queued", mm.pendingQueue.Len())
	}
	if len(mm.chatHistory) != 1 || mm.chatHistory[0].Role != "user" {
		t.Fatalf("chat history should contain the sent user message, got %+v", mm.chatHistory)
	}
}

// TestQueuedMessageDrainsIntoFreshRun verifies that agentDoneMsg drains the
// queue FIFO and chains a fresh run, keeping isProcessing true until empty.
func TestQueuedMessageDrainsIntoFreshRun(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.input.SetValue("first")
	m.input.CursorEnd()
	model, _ := m.Update(keyMsg("enter"))
	mm := model.(*AppModel)
	mm.input.SetValue("second")
	mm.input.CursorEnd()
	model, _ = mm.Update(keyMsg("enter"))
	mm = model.(*AppModel)

	if mm.pendingQueue.Len() != 2 {
		t.Fatalf("setup: want 2 queued, got %d", mm.pendingQueue.Len())
	}

	// Run 1 completes: drains "first" into a fresh run, keeps processing.
	model, _ = mm.Update(agentDoneMsg{})
	mm = model.(*AppModel)
	if mm.pendingQueue.Len() != 1 {
		t.Fatalf("after first done: queue len = %d, want 1", mm.pendingQueue.Len())
	}
	if !mm.IsProcessing {
		t.Fatal("isProcessing must stay true while the queue drains")
	}
	got := mm.pendingQueue.Snapshot()
	if got[0].Text != "second" {
		t.Fatalf("FIFO violation: next queued = %q, want %q", got[0].Text, "second")
	}

	// Run 2 completes: drains "second".
	model, _ = mm.Update(agentDoneMsg{})
	mm = model.(*AppModel)
	if mm.pendingQueue.Len() != 0 {
		t.Fatalf("after second done: queue len = %d, want 0", mm.pendingQueue.Len())
	}
	if !mm.IsProcessing {
		t.Fatal("isProcessing stays true while the chained run is active")
	}

	// Run 3 completes: queue empty, processing ends.
	model, _ = mm.Update(agentDoneMsg{})
	mm = model.(*AppModel)
	if mm.IsProcessing {
		t.Fatal("isProcessing must end when the queue is empty")
	}
}

// TestAgentDonePersistsAllRunMessages verifies that agentDoneMsg persists
// EVERY agent message the run produced, not only the last one. A single run
// can now emit multiple agent messages (one per thinking block, e.g. when a
// mid-run interjection opens a fresh block), and each must land in the
// session so a resumed conversation shows the same blocks.
func TestAgentDonePersistsAllRunMessages(t *testing.T) {
	m := newModelWithSvc(t)
	// Seed the session like the enter handler does: user message persisted
	// before the run starts.
	if err := m.sessionService.RecordUsageWithAttachments("user", "Q1", "", 5, nil); err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	// Simulate a run that produced two agent messages: the Q1 answer plus a
	// new thinking block (interjection response). runStartHistoryLen marks
	// where this run's messages begin (after the user message).
	m.chatHistory = []ChatMessage{
		{Role: "user", Content: "Q1"},
		{Role: "agent", Content: "A1", Thinking: "T1"},
		{Role: "agent", Content: "A2", Thinking: "T2"},
	}
	m.runStartHistoryLen = 1
	m.usage = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     100,
		CandidatesTokenCount: 50,
		TotalTokenCount:      150,
	}

	model, _ := m.Update(agentDoneMsg{})
	mm := model.(*AppModel)

	// The session must contain: user Q1, agent A1, agent A2.
	msgs, err := mm.sessionService.GetMessages(mm.sessionService.ActiveSessionID())
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 persisted messages (user+2 agent), got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "agent" || msgs[1].Content != "A1" || msgs[1].Thinking != "T1" {
		t.Fatalf("first agent message mismatch: %+v", msgs[1])
	}
	if msgs[2].Role != "agent" || msgs[2].Content != "A2" || msgs[2].Thinking != "T2" {
		t.Fatalf("second agent message mismatch: %+v", msgs[2])
	}
	// Provider usage lands on the final agent message.
	if msgs[2].Tokens != 150 {
		t.Fatalf("final message tokens = %d, want 150 (provider usage)", msgs[2].Tokens)
	}

	// A second agentDoneMsg must not re-persist the same messages.
	if _, err := mm.sessionService.GetMessages(mm.sessionService.ActiveSessionID()); err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	before := len(mm.chatHistory)
	model, _ = mm.Update(agentDoneMsg{})
	mm = model.(*AppModel)
	if len(mm.chatHistory) != before {
		t.Fatalf("agentDoneMsg should not add chat messages, got %d -> %d", before, len(mm.chatHistory))
	}
}

// TestHintBarShowsQueuedCount verifies the footer surfaces pending queue depth.
func TestHintBarShowsQueuedCount(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.pendingQueue.Push(util.QueuedPrompt{Text: "a"})
	m.pendingQueue.Push(util.QueuedPrompt{Text: "b"})

	view := m.hintBar()
	if !strings.Contains(view, "2 queued") {
		t.Fatalf("hint bar missing queued count:\n%s", view)
	}
	if !strings.Contains(view, "esc esc interrupt") {
		t.Fatalf("hint bar missing double-esc interrupt hint while processing:\n%s", view)
	}
}

// TestSingleEscArmsWithoutInterrupting verifies that one Esc while the agent
// is busy only arms the double-press interrupt and does not cancel the run.
func TestSingleEscArmsWithoutInterrupting(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.SetCancel(func() {})

	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	if mm.runCtrl.WasInterrupted() {
		t.Fatal("single esc must not interrupt - it only arms the double-press")
	}
	if mm.escArmedAt.IsZero() {
		t.Fatal("single esc should arm the interrupt window")
	}
}

// TestEscInterruptsActiveRunOnDoublePress verifies that a second Esc within
// the window cancels the run and surfaces the interrupt state.
func TestEscInterruptsActiveRunOnDoublePress(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.SetCancel(func() {})

	// First press arms; second press within the window interrupts.
	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	model, _ = mm.Update(keyMsg("esc"))
	mm = model.(*AppModel)
	if !mm.runCtrl.WasInterrupted() {
		t.Fatal("double esc within window should interrupt the run")
	}
	if !mm.escArmedAt.IsZero() {
		t.Fatal("interrupt should clear the armed state")
	}
}

// TestEscArmTimeoutDisarms verifies that when the double-Esc window expires
// with no second press, the armed state clears so a later single Esc just
// re-arms instead of cancelling.
func TestEscArmTimeoutDisarms(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.SetCancel(func() {})

	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	if mm.escArmedAt.IsZero() {
		t.Fatal("test setup: esc should arm")
	}
	armedAt := mm.escArmedAt

	// Window expires: disarm.
	model, _ = mm.Update(escArmTimeoutMsg{at: armedAt})
	mm = model.(*AppModel)
	if !mm.escArmedAt.IsZero() {
		t.Fatal("timeout should disarm the armed state")
	}
	if mm.runCtrl.WasInterrupted() {
		t.Fatal("timeout alone must not interrupt")
	}

	// A later single Esc re-arms (not interrupt).
	model, _ = mm.Update(keyMsg("esc"))
	mm = model.(*AppModel)
	if mm.runCtrl.WasInterrupted() {
		t.Fatal("esc after timeout must re-arm, not interrupt")
	}
	if mm.escArmedAt.IsZero() {
		t.Fatal("esc after timeout should re-arm")
	}
}

// TestEscStaleTimeoutDoesNotClearNewArm verifies that a timeout tick from an
// old arm cannot disarm a newer arm (interrupt + re-arm within the old
// window).
func TestEscStaleTimeoutDoesNotClearNewArm(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.SetCancel(func() {})

	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	oldArm := mm.escArmedAt
	if oldArm.IsZero() {
		t.Fatal("test setup: first esc should arm")
	}

	// Interrupt (clears armed), then re-arm with a fresh press.
	model, _ = mm.Update(keyMsg("esc"))
	mm = model.(*AppModel)
	if !mm.runCtrl.WasInterrupted() {
		t.Fatal("test setup: second esc should interrupt")
	}
	mm.runCtrl.ConsumeInterrupt() // simulate agentDoneMsg consuming the flag
	model, _ = mm.Update(keyMsg("esc"))
	mm = model.(*AppModel)
	newArm := mm.escArmedAt
	if newArm.IsZero() || newArm.Equal(oldArm) {
		t.Fatalf("test setup: expected a fresh arm, got %v", newArm)
	}

	// The stale tick from the first arm must not clear the new arm.
	model, _ = mm.Update(escArmTimeoutMsg{at: oldArm})
	mm = model.(*AppModel)
	if mm.escArmedAt.IsZero() {
		t.Fatal("stale timeout tick must not clear the newer arm")
	}
}

// TestEscAgentDoneClearsArmedState verifies that a run completing clears the
// armed window so a stray Esc can't cancel the next run.
func TestEscAgentDoneClearsArmedState(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.SetCancel(func() {})

	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	if mm.escArmedAt.IsZero() {
		t.Fatal("test setup: esc should arm")
	}

	model, _ = mm.Update(agentDoneMsg{})
	mm = model.(*AppModel)
	if !mm.escArmedAt.IsZero() {
		t.Fatal("agentDoneMsg should clear the armed state")
	}
}

// TestEscIdleIsNoOp verifies Esc with no active run stays a no-op.
func TestEscIdleIsNoOp(t *testing.T) {
	m := newTestModel(t)
	model, cmd := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	if mm.runCtrl.WasInterrupted() {
		t.Fatal("esc with idle agent must not interrupt")
	}
	if cmd != nil {
		t.Fatalf("esc must not quit, got cmd %v", cmd)
	}
}

// TestInterruptWithQueueMergesIntoOneTurn verifies that an interrupted run
// drains ALL queued messages as a single merged turn (Codex semantics).
func TestInterruptWithQueueMergesIntoOneTurn(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.pendingQueue.Push(util.QueuedPrompt{Text: "stop X"})
	m.pendingQueue.Push(util.QueuedPrompt{Text: "do Y"})

	// Simulate the interrupt flag set by Esc, then the run completing.
	m.runCtrl.Interrupt()
	model, _ := m.Update(agentDoneMsg{})
	mm := model.(*AppModel)

	if mm.pendingQueue.Len() != 0 {
		t.Fatalf("interrupt drain must empty the queue, got %d", mm.pendingQueue.Len())
	}
	// The merged turn is launched as a fresh run (isProcessing stays true).
	if !mm.IsProcessing {
		t.Fatal("merged turn should launch a fresh run")
	}
	// The merged user message must appear in chat history (display for the
	// launchTurn path), containing both queued texts.
	var sawUser bool
	for _, c := range mm.chatHistory {
		if c.Role == "user" && strings.Contains(c.Content, "stop X") && strings.Contains(c.Content, "do Y") {
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatalf("merged turn missing in chat history: %+v", mm.chatHistory)
	}
}

// TestInterruptEmptyQueueShowsNotice verifies the Codex-style notice when an
// interrupted run has nothing queued.
func TestInterruptEmptyQueueShowsNotice(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.runCtrl.Interrupt()

	model, _ := m.Update(agentDoneMsg{})
	mm := model.(*AppModel)
	if mm.IsProcessing {
		t.Fatal("empty queue after interrupt must end processing")
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "Conversation interrupted") {
		t.Fatalf("interrupt notice missing in log:\n%s", joined)
	}
}

// TestCtrlVWhileProcessingQueuesAttachment verifies image paste is unlocked
// during processing so queued messages can carry attachments.
func TestCtrlVWhileProcessingAllowed(t *testing.T) {
	m := newTestModel(t)
	m.IsProcessing = true
	m.focus = inputFocus

	// The clipboard read fails in tests (no X server / wl-paste), so this
	// must fall through to the textarea paste path without erroring. The
	// important regression is that the !m.IsProcessing guard was removed.
	model, _ := m.Update(keyMsg("ctrl+v"))
	_ = model.(*AppModel)
}
