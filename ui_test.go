package main

import (
	"context"
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
