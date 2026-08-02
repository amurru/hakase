package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestModel(t *testing.T) *appModel {
	t.Helper()
	m := newModel(context.Background(), nil, 100, true)
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
