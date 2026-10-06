package agent

import (
	"context"
	"testing"

	"amurru/hakase/internal/knowledge"
)

func stubPrompt(reply string, err error) func(ctx context.Context, query string) (string, error) {
	return func(context.Context, string) (string, error) { return reply, err }
}

func TestBuildExpandQueryFnSuccess(t *testing.T) {
	fn := buildExpandQueryFn(
		stubPrompt(`["alpha","beta","gamma"]`, nil),
		knowledge.BuildQueryExpansionPrompt,
		knowledge.ParseQueryExpansions,
	)
	got, err := fn(context.Background(), "q")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 3 || got[0] != "alpha" {
		t.Errorf("phrasings = %v, want [alpha beta gamma]", got)
	}
}

func TestBuildExpandQueryFnFailure(t *testing.T) {
	// Garbage reply: closure errors so the consumer falls back to plain
	// search (fail-open preserved).
	fn := buildExpandQueryFn(
		stubPrompt("not json", nil),
		knowledge.BuildQueryExpansionPrompt,
		knowledge.ParseQueryExpansions,
	)
	if _, err := fn(context.Background(), "q"); err == nil {
		t.Error("garbage reply: expected error, got nil")
	}
	// Prompt error propagates.
	fn = buildExpandQueryFn(
		stubPrompt("", context.DeadlineExceeded),
		knowledge.BuildQueryExpansionPrompt,
		knowledge.ParseQueryExpansions,
	)
	if _, err := fn(context.Background(), "q"); err == nil {
		t.Error("prompt error: expected error, got nil")
	}
}

func TestWireKnowledgeModelSeams(t *testing.T) {
	// Bridge regression test: the production wiring path (helper →
	// package var → consumer) must actually expand. No existing test
	// covers this — the suite stubs the package vars directly, which is
	// how the ~7-week outage went unnoticed.
	oldExpand, oldEnrich := knowledge.ExpandQueryFn, knowledge.EnrichKnowledgeFn
	t.Cleanup(func() {
		knowledge.ExpandQueryFn, knowledge.EnrichKnowledgeFn = oldExpand, oldEnrich
	})
	wireKnowledgeModelSeams(
		stubPrompt(`["alpha","beta"]`, nil),
		knowledge.BuildQueryExpansionPrompt,
		knowledge.ParseQueryExpansions,
	)
	if knowledge.EnrichKnowledgeFn == nil {
		t.Error("EnrichKnowledgeFn must be assigned by wiring")
	}
	if knowledge.ExpandQueryFn == nil {
		t.Fatal("ExpandQueryFn must be assigned by wiring")
	}
	got := knowledge.ExpandSearchQuery(context.Background(), "q")
	if len(got) != 3 || got[0] != "q" || got[1] != "alpha" {
		t.Errorf("ExpandSearchQuery after wiring = %v, want [q alpha beta]", got)
	}
}
