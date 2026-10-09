package finops

import (
	"testing"

	"google.golang.org/genai"
)

func TestFromGenaiPreservesAllFields(t *testing.T) {
	rec := FromGenai(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        100,
		CandidatesTokenCount:    40,
		CachedContentTokenCount: 60,
		ThoughtsTokenCount:      10,
		ToolUsePromptTokenCount: 20,
		TotalTokenCount:         170,
	})
	if rec.Prompt != 100 || rec.Candidates != 40 || rec.Cached != 60 ||
		rec.Thoughts != 10 || rec.ToolUse != 20 || rec.Total != 170 {
		t.Fatalf("fields not preserved: %+v", rec)
	}
	if got := rec.TotalTokens(); got != 170 {
		t.Fatalf("TotalTokens = %d, want 170", got)
	}
}

func TestFromGenaiNilSafe(t *testing.T) {
	rec := FromGenai(nil)
	if !rec.IsZero() {
		t.Fatalf("nil input should yield zero record, got %+v", rec)
	}
	if got := rec.TotalTokens(); got != 0 {
		t.Fatalf("nil TotalTokens = %d, want 0", got)
	}
}

func TestTotalTokensFallback(t *testing.T) {
	rec := UsageRecord{Prompt: 70, Candidates: 50}
	if got := rec.TotalTokens(); got != 120 {
		t.Fatalf("fallback TotalTokens = %d, want 120", got)
	}
	// Cached/thoughts/tool-use must not inflate the fallback: they are
	// already inside the prompt count per the genai docs.
	rec = UsageRecord{Prompt: 70, Candidates: 50, Cached: 60, Thoughts: 10, ToolUse: 20}
	if got := rec.TotalTokens(); got != 120 {
		t.Fatalf("fallback with extras = %d, want 120", got)
	}
}

func TestSubFloorsAtZero(t *testing.T) {
	now := UsageRecord{Prompt: 100, Candidates: 40, Cached: 60, Thoughts: 10, ToolUse: 20, Total: 170}
	prev := UsageRecord{Prompt: 60, Candidates: 40, Cached: 70, Thoughts: 4, ToolUse: 20, Total: 150}
	d := now.Sub(prev)
	if d.Prompt != 40 || d.Candidates != 0 || d.Cached != 0 ||
		d.Thoughts != 6 || d.ToolUse != 0 || d.Total != 20 {
		t.Fatalf("bad delta: %+v", d)
	}
}

func TestSubFirstToolCall(t *testing.T) {
	now := UsageRecord{Prompt: 100, Candidates: 5, Total: 105}
	d := now.Sub(UsageRecord{})
	if d.Prompt != 100 || d.Candidates != 5 || d.Total != 105 {
		t.Fatalf("delta from zero: %+v", d)
	}
}
