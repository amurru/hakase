package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubEmbedder maps marker substrings to fixed 2D vectors and counts calls.
type stubEmbedder struct {
	calls int
	fn    func(text string) []float32
}

func (s *stubEmbedder) embed(_ context.Context, texts []string) ([][]float32, error) {
	s.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = s.fn(t)
	}
	return out, nil
}

// markerVecs classifies any text by marker word: "mrrp" -> [1,0],
// "woof" -> [0,1], anything else -> neutral [0.7,0.7].
func markerVecs(text string) []float32 {
	switch {
	case strings.Contains(text, "mrrp"):
		return []float32{1, 0}
	case strings.Contains(text, "woof"):
		return []float32{0, 1}
	default:
		return []float32{0.7, 0.7}
	}
}

func withStubEmbed(t *testing.T, s *stubEmbedder) {
	t.Helper()
	old := EmbedFn
	EmbedFn = s.embed
	t.Cleanup(func() { EmbedFn = old })
}

func seedHybridNotes(t *testing.T, dir string) {
	t.Helper()
	// "cat" body shares NO token with the query "kitty" (the lexical gap);
	// the mrrp marker drives the stub dense similarity.
	writeNote(t, dir, "Cat", "mrrp felines sleep all day")
	// "dog" body contains the query token "kitty" (BM25 hit) plus a
	// contrasting marker so the stub ranks it dense-distant.
	writeNote(t, dir, "Dog", "woof kitty chaser")
}

func slugsOf(scored []ScoredKnowledgeNote) []string {
	out := make([]string, len(scored))
	for i, s := range scored {
		out[i] = s.Note.Slug
	}
	return out
}

func TestHybridParaphraseRecall(t *testing.T) {
	stub := &stubEmbedder{fn: func(text string) []float32 {
		if v := markerVecs(text); v[0] != 0.7 {
			return v // marker wins: dog body holds both "kitty" and "woof"
		}
		if strings.Contains(text, "kitty") {
			return []float32{1, 0} // query close to the mrrp note
		}
		return []float32{0.7, 0.7}
	}}
	withStubEmbed(t, stub)
	dir := t.TempDir()
	seedHybridNotes(t, dir)
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	// "kitty" is a substring of the dog body only: BM25 alone finds dog.
	if got := slugsOf(SearchKnowledgeScored(idx, "kitty", nil, false)); len(got) != 1 || got[0] != "dog" {
		t.Fatalf("BM25 baseline = %v, want [dog]", got)
	}
	got := slugsOf(HybridSearch(context.Background(), nil, dir, idx, "kitty", nil, false, true, "stub-model"))
	if len(got) != 2 {
		t.Fatalf("hybrid = %v, want both notes (dense recalls cat)", got)
	}
}

func TestHybridFusionOrder(t *testing.T) {
	stub := &stubEmbedder{fn: func(text string) []float32 {
		if v := markerVecs(text); v[0] != 0.7 {
			return v
		}
		if text == "kitty" {
			return []float32{1, 0}
		}
		return []float32{0.7, 0.7}
	}}
	withStubEmbed(t, stub)
	dir := t.TempDir()
	seedHybridNotes(t, dir)
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	// BM25 ranks [dog]; dense ranks [cat, dog]-ish (cat cosine 1.0, dog
	// cosine 0). Fused: dog appears in both branches so it must stay first.
	got := slugsOf(HybridSearch(context.Background(), nil, dir, idx, "kitty", nil, false, true, "stub-model"))
	if len(got) != 2 || got[0] != "dog" || got[1] != "cat" {
		t.Errorf("fused order = %v, want [dog cat]", got)
	}
}

func TestHybridDegradesToBM25(t *testing.T) {
	dir := t.TempDir()
	seedHybridNotes(t, dir)
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	want := SearchKnowledgeScored(idx, "kitty", nil, false)

	// hybrid=false with EmbedFn set: byte-identical.
	stub := &stubEmbedder{fn: markerVecs}
	withStubEmbed(t, stub)
	if got := HybridSearch(context.Background(), nil, dir, idx, "kitty", nil, false, false, "stub-model"); !equalScored(got, want) {
		t.Errorf("hybrid=false differs from BM25: %v vs %v", slugsOf(got), slugsOf(want))
	}
	if stub.calls != 0 {
		t.Errorf("hybrid=false made %d embedding calls, want 0", stub.calls)
	}

	// hybrid=true with nil EmbedFn: byte-identical, no panic.
	EmbedFn = nil
	if got := HybridSearch(context.Background(), nil, dir, idx, "kitty", nil, false, true, "stub-model"); !equalScored(got, want) {
		t.Errorf("nil EmbedFn differs from BM25: %v vs %v", slugsOf(got), slugsOf(want))
	}

	// EmbedFn error: fail open to BM25.
	EmbedFn = func(context.Context, []string) ([][]float32, error) {
		return nil, context.DeadlineExceeded
	}
	var warnings []string
	if got := HybridSearch(context.Background(), func(m string) { warnings = append(warnings, m) }, dir, idx, "kitty", nil, false, true, "stub-model"); !equalScored(got, want) {
		t.Errorf("error EmbedFn differs from BM25: %v vs %v", slugsOf(got), slugsOf(want))
	}
	if len(warnings) == 0 {
		t.Error("error EmbedFn: want a degrade warning logged")
	}
}

func equalScored(a, b []ScoredKnowledgeNote) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Note.Slug != b[i].Note.Slug || a[i].Score != b[i].Score {
			return false
		}
	}
	return true
}

func TestEnsureVectorsValidity(t *testing.T) {
	stub := &stubEmbedder{fn: func(string) []float32 { return []float32{1, 0} }}
	withStubEmbed(t, stub)
	dir := t.TempDir()
	writeNote(t, dir, "Alpha", "first body here")
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	ctx := context.Background()

	if _, _, err := ensureVectors(ctx, dir, idx, "m1", 0); err != nil {
		t.Fatalf("ensureVectors: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("backfill calls = %d, want 1", stub.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "vectors", "alpha.vec.json")); err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}

	// Warm: no new embedding calls.
	if _, _, err := ensureVectors(ctx, dir, idx, "m1", 0); err != nil {
		t.Fatalf("ensureVectors warm: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("warm calls = %d, want 1 (cache/sidecar hit)", stub.calls)
	}

	// Cold memory cache (restart): sidecars trusted, still no calls.
	invalidateVectorCache(dir)
	if _, _, err := ensureVectors(ctx, dir, idx, "m1", 0); err != nil {
		t.Fatalf("ensureVectors cold: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("cold-cache calls = %d, want 1 (dim adopted from sidecar)", stub.calls)
	}

	// New note: only it is embedded (one batched call); alpha stays warm.
	writeNote(t, dir, "Beta", "second body")
	idx, err = BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	before := stub.calls
	if _, _, err := ensureVectors(ctx, dir, idx, "m1", 2); err != nil {
		t.Fatalf("ensureVectors new note: %v", err)
	}
	if stub.calls != before+1 {
		t.Errorf("new-note calls = %d, want one batched call", stub.calls-before)
	}

	// Model change: full re-embed.
	before = stub.calls
	if _, _, err := ensureVectors(ctx, dir, idx, "m2", 2); err != nil {
		t.Fatalf("ensureVectors model change: %v", err)
	}
	if stub.calls != before+1 {
		t.Errorf("model-change calls = %d, want one batched call", stub.calls-before)
	}

	// Corrupt sidecar: re-embed, no error.
	bad, _ := vectorPath(dir, "alpha")
	if err := os.WriteFile(bad, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	invalidateVectorCache(dir)
	before = stub.calls
	if _, _, err := ensureVectors(ctx, dir, idx, "m2", 2); err != nil {
		t.Fatalf("ensureVectors corrupt sidecar: %v", err)
	}
	if stub.calls == before {
		t.Error("corrupt sidecar: want re-embed, got no embedding call")
	}
}

func TestDocTextTruncation(t *testing.T) {
	n := &KnowledgeNote{Body: strings.Repeat("x", 5000)}
	n.Frontmatter.Title = "T"
	if got := docText(n); len([]rune(got)) != docTextRunes {
		t.Errorf("truncated runes = %d, want %d", len([]rune(got)), docTextRunes)
	}
	short := &KnowledgeNote{Body: "hi"}
	short.Frontmatter.Title = "T"
	if !strings.Contains(docText(short), "hi") {
		t.Error("short docText must be untruncated")
	}
}
