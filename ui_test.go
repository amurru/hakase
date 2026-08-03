package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"google.golang.org/genai"
)

func newTestModel(t *testing.T) *appModel {
	t.Helper()
	m := newModel(context.Background(), nil, nil, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*appModel)
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
	m = model.(*appModel)

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

	mm := model.(*appModel)
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
	if got := model.(*appModel).chatScrollOffset; got != 10 {
		t.Fatalf("expected scroll offset preserved at 10 while reading history, got %d", got)
	}

	mm := model.(*appModel)
	mm.chatScrollOffset = mm.maxChatScrollOffset()
	model, _ = mm.Update(agentStreamMsg{Content: " more content"})
	got := model.(*appModel).chatScrollOffset
	if got < mm.chatScrollOffset {
		t.Fatalf("expected auto-scroll to follow stream when at bottom (was %d, got %d)", mm.chatScrollOffset, got)
	}
}

func TestLogLinesAppendIncrementally(t *testing.T) {
	m := newTestModel(t)
	model, _ := m.Update(StatusLogMsg{Text: "first"})
	model, _ = model.Update(agentLogMsg("second"))

	mm := model.(*appModel)
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
		if !model.(*appModel).showHelp {
			t.Fatalf("%q should open the help overlay", key)
		}
		model, _ = model.Update(keyMsg(key))
		if model.(*appModel).showHelp {
			t.Fatalf("%q should toggle the help overlay closed", key)
		}
	}
}

func TestHelpOpensWithBareQuestionMarkOutsideInput(t *testing.T) {
	m := newTestModel(t)
	m.focus = chatFocus
	model, _ := m.Update(keyMsg("?"))
	if !model.(*appModel).showHelp {
		t.Fatal("bare '?' with chat focused should open help")
	}
}

func TestQuestionMarkTypesIntoInput(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("what")
	m.input.CursorEnd()
	model, _ := m.Update(keyMsg("?"))
	mm := model.(*appModel)
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
	if model.(*appModel).showHelp {
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
	if model.(*appModel).showHelp {
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
		m = model.(*appModel)
		expect(want)
	}

	model, _ = m.Update(keyMsg("shift+tab"))
	m = model.(*appModel)
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
	mm := model.(*appModel)
	if !mm.logViewport.AtBottom() {
		t.Fatal("test setup: log should be at the bottom after appending")
	}

	model, _ = mm.Update(StatusLogMsg{Text: "new"})
	mm = model.(*appModel)
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
	mm := model.(*appModel)
	mm.logViewport.GotoTop()
	if mm.logViewport.AtBottom() {
		t.Fatal("test setup: log should be scrollable after GotoTop")
	}
	before := mm.logViewport.YOffset()

	model, _ = mm.Update(StatusLogMsg{Text: "new"})
	mm = model.(*appModel)
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
	m.modelInfo = &ModelInfo{Name: "test-model", ContextWindow: 200_000}
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
	m.modelInfo = &ModelInfo{Name: "poolside/laguna-s-2.1:free", ContextWindow: 240_000, ThinkingLevel: "xhigh"}
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
	m.modelInfo = &ModelInfo{Name: "m", ContextWindow: 100_000}
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

	m.modelInfo = &ModelInfo{Name: "m", ThinkingEnabled: false, ThinkingLevel: ""}
	m.thinkingLevel = ""
	if got := m.thinkingStatus(); got != "off" {
		t.Fatalf("model without thinking = %q, want off", got)
	}

	m.modelInfo = &ModelInfo{Name: "m", ThinkingEnabled: true, ThinkingLevel: "HIGH"}
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
		mm := model.(*appModel)
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
	mm := model.(*appModel)
	if mm.focus != inputFocus {
		t.Fatalf("click on input: focus = %d, want %d (inputFocus)", mm.focus, inputFocus)
	}
}

// TestMouseYToContentLineGeometry locks in the border-aware coordinate map
// from screen Y to content line for each pane. For a 120x40 terminal the
// layout is: chatH=31, logH=21, taskH=14 (input at min 1 line, DynamicHeight).
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

	// Task: content starts at row logH+4 = 25 (taskH=14 -> rows 0..11).
	m.selectionPane = taskFocus
	for _, c := range []struct{ y, want int }{
		{24, -1}, // task top border
		{25, 0},  // first task content line
		{36, 11}, // last task content line
		{37, 11}, // overshoot clamps (bottom border)
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
