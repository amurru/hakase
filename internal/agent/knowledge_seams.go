// knowledge_seams.go - wires the knowledge package's model-backed
// callbacks to the live model stack (spec KS-002,
// docs/knowledge-seams/spec.md).
//
// Background: the phase0-wave3 DI migration split setup (which sets
// deps.* fields) from the knowledge tools (which read knowledge.*
// package vars) and nothing bridged them, so search expansion and
// save enrichment silently never fired for ~7 weeks. The single source
// of truth is the knowledge package vars, assigned here directly —
// the same pattern as the skill EvolveMutateFn bridge and the hybrid
// EmbedFn below it in SetupRunner. Never a Deps field: the deleted
// deps.Enrich/ExpandQueryFn fields were written once and read nowhere.
package agent

import (
	"amurru/hakase/internal/knowledge"
	"context"
	"fmt"
)

// buildExpandQueryFn returns the HyDE-lite query-expansion closure over
// explicit dependencies (body moved verbatim from the old SetupRunner
// inline closure). ModelPromptFn is a plain func, not a var, so it
// travels as the prompt parameter for tests to substitute a stub.
func buildExpandQueryFn(prompt func(ctx context.Context, query string) (string, error), buildPrompt func(query string) string, parse func(raw string) []string) func(ctx context.Context, query string) ([]string, error) {
	return func(ctx context.Context, query string) ([]string, error) {
		raw, err := prompt(ctx, buildPrompt(query))
		if err != nil {
			return nil, err
		}
		parsed := parse(raw)
		if parsed == nil {
			return nil, fmt.Errorf("query expansion response did not parse")
		}
		return parsed, nil
	}
}

// wireKnowledgeModelSeams assigns the knowledge package's model-backed
// callbacks: enrichment (save_knowledge auto-summary/tags/related) and
// query expansion (search_knowledge HyDE-lite phrasings). Called once
// from SetupRunner; directly unit-testable with stub functions.
func wireKnowledgeModelSeams(prompt func(ctx context.Context, query string) (string, error), buildPrompt func(query string) string, parse func(raw string) []string) {
	knowledge.EnrichKnowledgeFn = prompt
	knowledge.ExpandQueryFn = buildExpandQueryFn(prompt, buildPrompt, parse)
}
