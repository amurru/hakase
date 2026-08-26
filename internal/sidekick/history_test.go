package sidekick

import (
	"strings"
	"testing"

	hakasesession "amurru/hakase/internal/session"
)

// text builds an in-context plain chat turn.
func text(role, content string) hakasesession.Message {
	return hakasesession.Message{Role: role, Content: content, Kind: hakasesession.MessageKindText, InContext: true}
}

func TestRecentTranscriptFiltersAndLabels(t *testing.T) {
	tool := hakasesession.Message{
		Role: "agent", Kind: hakasesession.MessageKindToolCall,
		Content: "big tool blob", InContext: true, // in-context yet still filtered by kind
	}
	evicted := text("user", "evicted turn")
	evicted.InContext = false

	s := hakasesession.NewSession("t")
	s.Messages = append(s.Messages,
		text("user", "hello"),
		text("agent", "here is the answer about X is 42"),
		tool,
		evicted,
		hakasesession.Message{Role: Role, Kind: hakasesession.MessageKindSidekick, Content: "earlier I said 42", InContext: true},
		text("user", "what's your take?"),
	)

	got := RecentTranscript(s, 0)
	for _, want := range []string{
		"user: hello",
		"assistant: here is the answer",
		"sidekick (you): earlier I said 42",
		"user: what's your take?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "big tool blob") || strings.Contains(got, "evicted") {
		t.Error("tool/out-of-context content leaked into transcript:\n" + got)
	}
	// Oldest-first ordering.
	if strings.Index(got, "hello") > strings.Index(got, "take?") {
		t.Error("transcript not in chronological order")
	}
}

func TestRecentTranscriptEmptyAndNil(t *testing.T) {
	if RecentTranscript(nil, 0) != "" {
		t.Error("nil session must render empty")
	}
	s := hakasesession.NewSession("t")
	if RecentTranscript(s, 0) != "" {
		t.Error("message-less session must render empty")
	}
}

func TestRecentTranscriptTailBias(t *testing.T) {
	s := hakasesession.NewSession("t")
	first := strings.Repeat("a", 100)
	last := strings.Repeat("b", 100)
	s.Messages = append(s.Messages, text("user", first))
	for i := 0; i < 48; i++ {
		s.Messages = append(s.Messages, text("user", strings.Repeat("m", 100)))
	}
	s.Messages = append(s.Messages, text("user", last))

	const budget = 250
	got := RecentTranscript(s, budget)

	if got == "" {
		t.Fatal("expected non-empty transcript")
	}
	if len(got) > budget+1 { // +1 tolerance for join edge
		t.Fatalf("transcript %d chars exceeds budget %d", len(got), budget)
	}
	if !strings.Contains(got, last) {
		t.Error("newest message was trimmed away (expected tail bias)")
	}
	if strings.Contains(got, first) {
		t.Error("oldest message should have been dropped under budget")
	}
}

func TestBuildAskPrompt(t *testing.T) {
	// No history: question passes through unchanged (cold-ask behavior).
	if got := BuildAskPrompt("", "why?"); got != "why?" {
		t.Fatalf("cold ask mutated: %q", got)
	}

	got := BuildAskPrompt("assistant: X is 42", "what's your take?")
	for _, want := range []string{"[CONVERSATION SO FAR]", "assistant: X is 42", "[YOUR TASK]", "what's your take?"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\ngot: %s", want, got)
		}
	}
	if strings.Index(got, "X is 42") > strings.Index(got, "what's your take?") {
		t.Error("history must precede the task line")
	}
}
