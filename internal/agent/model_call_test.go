package agent

import (
	"context"
	"testing"

	"amurru/hakase/internal/finops"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestPromptWithModelCapturesFullUsage(t *testing.T) {
	oldObserve := ObserveModelUsage
	oldLast := LastModelUsage
	defer func() { ObserveModelUsage = oldObserve; LastModelUsage = oldLast }()

	var observed *finops.UsageRecord
	ObserveModelUsage = func(rec finops.UsageRecord) { observed = &rec }
	LastModelUsage = nil

	stub := &stubProvider{name: "test-model", responses: []*model.LLMResponse{
		{
			Content: genai.NewContentFromText("hello", genai.RoleModel),
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:        100,
				CandidatesTokenCount:    40,
				CachedContentTokenCount: 60,
				ThoughtsTokenCount:      10,
				ToolUsePromptTokenCount: 20,
				TotalTokenCount:         170,
			},
		},
	}}

	out, err := promptWithModel(context.Background(), stub, "hi", finops.ReasonSummarize)
	if err != nil {
		t.Fatalf("promptWithModel: %v", err)
	}
	if out != "hello" {
		t.Fatalf("output = %q, want hello", out)
	}
	if LastModelUsage == nil {
		t.Fatal("expected LastModelUsage to be set")
	}
	got := *LastModelUsage
	if got.Prompt != 100 || got.Candidates != 40 || got.Cached != 60 ||
		got.Thoughts != 10 || got.ToolUse != 20 || got.Total != 170 {
		t.Fatalf("truncated usage record: %+v", got)
	}
	if got.Reason != finops.ReasonSummarize || got.Model != "test-model" {
		t.Fatalf("missing labels: %+v", got)
	}
	if observed == nil || *observed != got {
		t.Fatalf("ObserveModelUsage not called with record: %+v vs %+v", observed, got)
	}
}

func TestPromptWithModelDefaultsReason(t *testing.T) {
	oldLast := LastModelUsage
	defer func() { LastModelUsage = oldLast }()
	LastModelUsage = nil

	stub := &stubProvider{name: "m", responses: []*model.LLMResponse{
		{
			Content:       genai.NewContentFromText("x", genai.RoleModel),
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, TotalTokenCount: 5},
		},
	}}
	if _, err := promptWithModel(context.Background(), stub, "hi", ""); err != nil {
		t.Fatalf("promptWithModel: %v", err)
	}
	if LastModelUsage == nil || LastModelUsage.Reason != finops.ReasonSingle {
		t.Fatalf("expected default reason single, got %+v", LastModelUsage)
	}
}
