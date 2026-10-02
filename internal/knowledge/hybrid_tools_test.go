package knowledge

import (
	"strings"
	"testing"
)

// resultSlugs extracts slugs from a runTool search_knowledge result map.
func resultSlugs(t *testing.T, res map[string]any) []string {
	t.Helper()
	raw, ok := res["results"].([]any)
	if !ok {
		t.Fatalf("results shape: %T", res["results"])
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("result shape: %T", r)
		}
		s, _ := m["slug"].(string)
		out = append(out, s)
	}
	return out
}

func TestSearchToolHybridEndToEnd(t *testing.T) {
	stub := &stubEmbedder{fn: func(text string) []float32 {
		if v := markerVecs(text); v[0] != 0.7 {
			return v
		}
		if strings.Contains(text, "kitty") {
			return []float32{1, 0}
		}
		return []float32{0.7, 0.7}
	}}
	withStubEmbed(t, stub)
	dir := t.TempDir()
	seedHybridNotes(t, dir)

	// BM25-only baseline through the tool: dog only.
	plain, err := CreateKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("CreateKnowledgeTools: %v", err)
	}
	res, err := runTool(t, plain[2], map[string]any{"query": "kitty"})
	if err != nil {
		t.Fatalf("plain search: %v", err)
	}
	if got := resultSlugs(t, res); len(got) != 1 || got[0] != "dog" {
		t.Fatalf("plain tool = %v, want [dog]", got)
	}

	// Hybrid through the tool: fused set, sidecar written to disk.
	hyb, err := CreateKnowledgeToolsWithOptions(func(string) {}, dir, SearchOptions{Hybrid: true, EmbedModel: "stub-model"})
	if err != nil {
		t.Fatalf("CreateKnowledgeToolsWithOptions: %v", err)
	}
	res, err = runTool(t, hyb[2], map[string]any{"query": "kitty"})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	got := resultSlugs(t, res)
	if len(got) != 2 || got[0] != "dog" || got[1] != "cat" {
		t.Errorf("hybrid tool = %v, want [dog cat]", got)
	}
	if stub.calls == 0 {
		t.Error("hybrid tool made no embedding calls")
	}

	// Hybrid with nil EmbedFn degrades to the BM25 baseline, no error.
	EmbedFn = nil
	res, err = runTool(t, hyb[2], map[string]any{"query": "kitty"})
	if err != nil {
		t.Fatalf("degraded hybrid search: %v", err)
	}
	if got := resultSlugs(t, res); len(got) != 1 || got[0] != "dog" {
		t.Errorf("degraded hybrid tool = %v, want [dog]", got)
	}
}
