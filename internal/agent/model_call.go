// model_call.go - shared single-prompt model invocation for the model-backed
// helpers: knowledge enrichment, HyDE-lite query expansion, and the evolver
// mutator. All three use the same provider stack: the configured summary
// model when present, falling back to the primary model.
package agent

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/finops"
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ObserveModelUsage reports one single-prompt call's usage record. Set by
// surfaces that aggregate FinOps data; nil by default (capture still lands
// in LastModelUsage for tests and debugging).
var ObserveModelUsage func(finops.UsageRecord)

// LastModelUsage is the most recent single-prompt call's full usage record
// (FO-001). It makes the summarize/HyDE/sleep/sidekick paths visible without
// changing the (string, error) caller contract.
var LastModelUsage *finops.UsageRecord

// ModelPromptFn sends a single user prompt to the configured model (summary
// model preferred, primary fallback) and returns the accumulated response
// text. Returns an error when no model is available (CLI/tests) or the call
// fails - callers decide how to degrade.
func ModelPromptFn(ctx context.Context, prompt string) (string, error) {
	return ModelPromptWithReason(ctx, prompt, finops.ReasonSingle)
}

// ModelPromptWithReason is ModelPromptFn with an explicit FinOps reason label
// (summarize, hyde, sleep, sidekick, evolve). Callers on a labeled path
// should prefer it so usage can be attributed; the record is published via
// ObserveModelUsage and LastModelUsage either way.
func ModelPromptWithReason(ctx context.Context, prompt, reason string) (string, error) {
	llm := hctx.SummarizeModel
	if llm == nil && hctx.CurrentModelFunc != nil {
		llm = hctx.CurrentModelFunc()
	}
	if llm == nil {
		return "", fmt.Errorf("no model available")
	}
	return promptWithModel(ctx, llm, prompt, reason)
}

// promptWithModel runs one prompt against an explicit model and captures the
// full usage record with the given reason label.
func promptWithModel(ctx context.Context, llm model.LLM, prompt, reason string) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("no model available")
	}
	if reason == "" {
		reason = finops.ReasonSingle
	}
	req := &adkLLMRequest{
		Model:    llm.Name(),
		Contents: []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
	}
	var out strings.Builder
	var last finops.UsageRecord
	haveUsage := false
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", err
		}
		if resp == nil {
			continue
		}
		if resp.UsageMetadata != nil {
			rec := finops.FromGenai(resp.UsageMetadata)
			rec.Model = req.Model
			rec.Reason = reason
			last = rec
			haveUsage = true
		}
		if resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part != nil && part.Text != "" && !part.Thought {
					out.WriteString(part.Text)
				}
			}
		}
	}
	if haveUsage {
		rec := last
		LastModelUsage = &rec
		if ObserveModelUsage != nil {
			ObserveModelUsage(rec)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// PromptModelCaller returns a ModelPromptFn-style single-prompt caller over
// an explicit model. Used where a SECOND model identity is required - the
// sleep judge seam (plan H2), which must be independent of the
// SummarizeModel/CurrentModel pairing ModelPromptFn serves.
func PromptModelCaller(llm model.LLM) func(ctx context.Context, prompt string) (string, error) {
	return PromptModelCallerWithReason(llm, finops.ReasonSingle)
}

// PromptModelCallerWithReason is PromptModelCaller with an explicit FinOps
// reason label for the returned closure's calls.
func PromptModelCallerWithReason(llm model.LLM, reason string) func(ctx context.Context, prompt string) (string, error) {
	if reason == "" {
		reason = finops.ReasonSingle
	}
	return func(ctx context.Context, prompt string) (string, error) {
		return promptWithModel(ctx, llm, prompt, reason)
	}
}
