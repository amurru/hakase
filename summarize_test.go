package main

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSummarizePromptHasNineSections(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "q1", InContext: true, Kind: MessageKindText},
		{Role: "agent", Content: "a1", InContext: true, Kind: MessageKindText},
	}
	prompt := buildSummarizePrompt(msgs)
	for _, section := range []string{
		"1. PRIMARY INTENT",
		"2. KEY DECISIONS",
		"3. FILES/ARTIFACTS",
		"4. ERRORS/FIXES",
		"5. APPROACHES",
		"6. USER MESSAGES",
		"7. PENDING TASKS",
		"8. CURRENT WORK",
		"9. NEXT STEP",
	} {
		if !strings.Contains(prompt, section) {
			t.Fatalf("prompt missing section %q", section)
		}
	}
	if !strings.Contains(prompt, "USER: q1") {
		t.Fatalf("prompt missing transcript user message")
	}
	if !strings.Contains(prompt, "AGENT: a1") {
		t.Fatalf("prompt missing transcript agent message")
	}
}

func TestBuildSummarizePromptSkipsToolTranscripts(t *testing.T) {
	msgs := []Message{
		{Role: "agent", Content: "tool call data", InContext: true, Kind: MessageKindToolCall},
		{Role: "agent", Content: "huge tool output", InContext: true, Kind: MessageKindToolResult},
		{Role: "user", Content: "keep me", InContext: true, Kind: MessageKindText},
	}
	prompt := buildSummarizePrompt(msgs)
	if strings.Contains(prompt, "huge tool output") {
		t.Fatalf("tool output leaked into summary prompt")
	}
	if !strings.Contains(prompt, "keep me") {
		t.Fatalf("text message missing from summary prompt")
	}
}

func TestBuildSummarizePromptIncludesRunningSummary(t *testing.T) {
	msgs := []Message{
		{Role: "agent", Content: "OLD RUNNING SUMMARY", InContext: true, Kind: MessageKindSummary},
		{Role: "user", Content: "new question", InContext: true, Kind: MessageKindText},
	}
	prompt := buildSummarizePrompt(msgs)
	if !strings.Contains(prompt, "OLD RUNNING SUMMARY") {
		t.Fatalf("running summary must be merged into the prompt")
	}
	if !strings.Contains(prompt, "[previous summary]") {
		t.Fatalf("running summary marker missing")
	}
}

func TestBuildSummarizePromptCapsAtTail(t *testing.T) {
	// Many messages each over the cap: only the tail must survive.
	var msgs []Message
	for i := 0; i < 30; i++ {
		msgs = append(msgs, Message{
			Role: "user", Content: strings.Repeat("x", 10000), InContext: true, Kind: MessageKindText,
		})
	}
	// Make the last message distinct so we can check the tail is kept.
	msgs[len(msgs)-1].Content = "TAIL MARKER"

	prompt := buildSummarizePrompt(msgs)
	if !strings.Contains(prompt, "TAIL MARKER") {
		t.Fatalf("tail message must be kept in the prompt")
	}
	if len(prompt) > 90000 {
		t.Fatalf("prompt too large: %d chars", len(prompt))
	}
}

func TestScheduleSummarizeDedup(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", "q", "", 5); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()

	// First schedule marks in-flight.
	b.scheduleSummarize(id)
	summarizeMu.Lock()
	inFlight := summarizing[id]
	summarizeMu.Unlock()
	if !inFlight {
		t.Fatal("summarization should be marked in-flight after schedule")
	}

	// Second schedule for the same session must be a no-op (dedup).
	b.scheduleSummarize(id)
	summarizeMu.Lock()
	count := 0
	for sid, active := range summarizing {
		if sid == id && active {
			count++
		}
	}
	summarizeMu.Unlock()
	if count != 1 {
		t.Fatalf("summarization scheduled %d times for session, want 1", count)
	}

	// Cleanup: since currentModel is nil in tests, runSummarize fails fast
	// and the deferred cleanup removes the in-flight marker. Wait briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		summarizeMu.Lock()
		done := !summarizing[id]
		summarizeMu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("summarization marker never cleared")
}

func TestFallbackCompactionRunsWithoutModel(t *testing.T) {
	b, svc := newTestBuilder(t)
	if err := svc.RecordUsage("user", strings.Repeat("y", 4000), "", 1000); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUsage("agent", strings.Repeat("z", 4000), "", 1000); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()

	// No model available -> runSummarize errors and falls back to the
	// deterministic snip; session must remain loadable and consistent.
	if err := b.runSummarize(id); err == nil {
		t.Fatal("runSummarize should error when no model is available")
	}
	msgs, err := svc.GetMessages(id)
	if err != nil {
		t.Fatalf("GetMessages after failed summarize: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages after failed summarize = %d, want 2 (unchanged)", len(msgs))
	}
}
