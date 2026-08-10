// model_call.go - shared single-prompt model invocation for the model-backed
// helpers: knowledge enrichment, HyDE-lite query expansion, and the evolver
// mutator. All three use the same provider stack: the configured summary
// model when present, falling back to the primary model.
package main

import (
	hctx "amurru/hakase/internal/context"
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// modelPromptFn sends a single user prompt to the configured model (summary
// model preferred, primary fallback) and returns the accumulated response
// text. Returns an error when no model is available (CLI/tests) or the call
// fails - callers decide how to degrade.
func modelPromptFn(ctx context.Context, prompt string) (string, error) {
	llm := hctx.SummarizeModel
	if llm == nil && hctx.CurrentModelFunc != nil {
		llm = hctx.CurrentModelFunc()
	}
	if llm == nil {
		return "", fmt.Errorf("no model available")
	}
	req := &adkLLMRequest{
		Model:    llm.Name(),
		Contents: []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
	}
	var out strings.Builder
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", err
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part != nil && part.Text != "" && !part.Thought {
					out.WriteString(part.Text)
				}
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}
