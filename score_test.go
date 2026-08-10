// score_test.go - tests for the Phase 3d-1 BM25 relevance ranking and the
// Phase 3d-2 index cache / 3d-4 query expansion helpers.
package main

import (
	hctx "amurru/hakase/internal/context"
	"context"
	"os"
	"strings"
	"testing"
)

// ---------- tokenize ----------

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Quantum Computing", []string{"quantum", "computing"}},
		{"the quick brown fox", []string{"quick", "brown", "fox"}},
		{"  spaced,out--tokens  ", []string{"spaced", "out", "tokens"}},
		{"a b c", nil},             // all stopwords/single-rune
		{"", nil},                  // empty
		{"C++ & Go!", []string{"go"}}, // single-rune tokens are dropped by design
	}
	for _, c := range cases {
		got := hctx.Tokenize(c.in)
		if len(got) != len(c.want) {
			t.Errorf("hctx.Tokenize(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("hctx.Tokenize(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestTokenCounts(t *testing.T) {
	counts := hctx.TokenCounts("quantum quantum mechanics")
	if counts["quantum"] != 2 || counts["mechanics"] != 1 {
		t.Errorf("tokenCounts = %v", counts)
	}
}

// ---------- scoring ----------

func TestSearchKnowledgeScored_TitleBoost(t *testing.T) {
	dir := t.TempDir()
	// Note A matches "quantum" only in the body; note B matches it in the
	// TITLE. Title carries a 3x boost, so B must rank first even though it
	// comes after A alphabetically.
	writeNoteFile(t, dir, "alpha-note", "---\ntitle: \"Alpha note\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nsome quantum theory details here\n")
	writeNoteFile(t, dir, "quantum-notes", "---\ntitle: \"Quantum notes\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nunrelated body\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	scored := SearchKnowledgeScored(idx, "quantum", nil, false)
	if len(scored) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(scored))
	}
	if scored[0].Note.Frontmatter.Title != "Quantum notes" {
		t.Errorf("title match should rank first, got %q", scored[0].Note.Frontmatter.Title)
	}
	if scored[0].Score <= scored[1].Score {
		t.Errorf("expected title-boosted score to exceed body-only score: %v vs %v", scored[0].Score, scored[1].Score)
	}
}

func TestSearchKnowledgeScored_LengthNormalization(t *testing.T) {
	dir := t.TempDir()
	// Both match "quantum" in the body; the shorter document should win on
	// length normalization (higher BM25 tfNorm for the same tf).
	writeNoteFile(t, dir, "short", "---\ntitle: \"Short\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nquantum core\n")
	writeNoteFile(t, dir, "long", "---\ntitle: \"Long\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nquantum plus a very long rambling discussion that should dilute the term frequency signal substantially across many words\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	scored := SearchKnowledgeScored(idx, "quantum", nil, false)
	if len(scored) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(scored))
	}
	if scored[0].Note.Frontmatter.Title != "Short" {
		t.Errorf("shorter doc should rank first, got %q", scored[0].Note.Frontmatter.Title)
	}
}

func TestSearchKnowledge_ResultSetUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "a", "---\ntitle: \"Alpha\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nquantum body\n")
	writeNoteFile(t, dir, "b", "---\ntitle: \"Beta\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nno match here\n")
	writeNoteFile(t, dir, "c", "---\ntitle: \"Gamma\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nquantum also here\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	results := SearchKnowledge(idx, "quantum", nil, false)
	if len(results) != 2 {
		t.Fatalf("expected same result set (2), got %d", len(results))
	}
	titles := []string{results[0].Frontmatter.Title, results[1].Frontmatter.Title}
	joined := strings.Join(titles, ",")
	if !strings.Contains(joined, "Alpha") || !strings.Contains(joined, "Gamma") {
		t.Errorf("result set changed: %v", titles)
	}
}

// ---------- RRF fusion ----------

func TestFuseRRF(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "a", "---\ntitle: \"A\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody a\n")
	writeNoteFile(t, dir, "b", "---\ntitle: \"B\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody b\n")
	writeNoteFile(t, dir, "c", "---\ntitle: \"C\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody c\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	// Set 1 ranks [c, a, b]; set 2 also ranks [c, b, a]. C is rank 1 in BOTH
	// sets, so after RRF it must clearly come out on top.
	set1 := []ScoredKnowledgeNote{
		{Note: *idx.BySlug["c"], Score: 1},
		{Note: *idx.BySlug["a"], Score: 0.9},
		{Note: *idx.BySlug["b"], Score: 0.8},
	}
	set2 := []ScoredKnowledgeNote{
		{Note: *idx.BySlug["c"], Score: 1},
		{Note: *idx.BySlug["b"], Score: 0.9},
		{Note: *idx.BySlug["a"], Score: 0.8},
	}
	fused := fuseRRF([][]ScoredKnowledgeNote{set1, set2})
	if len(fused) != 3 {
		t.Fatalf("fused length = %d, want 3", len(fused))
	}
	if fused[0].Note.Slug != "c" {
		t.Errorf("RRF fusion should rank c first, got %q", fused[0].Note.Slug)
	}
}

// ---------- query expansion (3d-4) ----------

func TestParseQueryExpansions(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`["quantum computing basics", "what is quantum computation"]`, 2},
		{"```json\n[\"a\",\"b\",\"c\"]\n```", 3},
		{"Here are the phrasings: [\"x\", \"y\"] thanks!", 2},
		{"no array here", 0},
		{"[]", 0},
	}
	for _, c := range cases {
		got := parseQueryExpansions(c.raw)
		if len(got) != c.want {
			t.Errorf("parseQueryExpansions(%q) = %d expansions, want %d", c.raw, len(got), c.want)
		}
	}
}

func TestExpandSearchQuery_Fallback(t *testing.T) {
	orig := expandQueryFn
	defer func() { expandQueryFn = orig }()

	// nil callback: only the original query.
	expandQueryFn = nil
	if got := expandSearchQuery(context.Background(), "foo bar"); len(got) != 1 || got[0] != "foo bar" {
		t.Errorf("nil callback: got %v", got)
	}

	// failing callback: falls back to the original query only.
	expandQueryFn = func(ctx context.Context, q string) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	if got := expandSearchQuery(context.Background(), "foo"); len(got) != 1 {
		t.Errorf("failing callback: got %v", got)
	}

	// working callback: original + deduped expansions.
	expandQueryFn = func(ctx context.Context, q string) ([]string, error) {
		return []string{"alt one", "foo", "alt two"}, nil
	}
	got := expandSearchQuery(context.Background(), "foo")
	if len(got) != 3 {
		t.Errorf("working callback: got %v", got)
	}
}

// ---------- index cache (3d-2) ----------

func TestKnowledgeIndexCache(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "one", "---\ntitle: \"One\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody one\n")

	idx1, err := getKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("getKnowledgeIndex: %v", err)
	}
	// Second call must hit the cache and return the SAME pointer.
	idx2, err := getKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("getKnowledgeIndex: %v", err)
	}
	if idx1 != idx2 {
		t.Error("second getKnowledgeIndex did not hit the cache")
	}

	// Mutate the tree: the fingerprint changes, so the next call rebuilds.
	writeNoteFile(t, dir, "two", "---\ntitle: \"Two\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody two\n")
	idx3, err := getKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("getKnowledgeIndex: %v", err)
	}
	if idx3 == idx1 {
		t.Error("getKnowledgeIndex did not rebuild after a note was added")
	}
	if _, ok := idx3.BySlug["two"]; !ok {
		t.Error("rebuilt index missing the new note")
	}

	// Explicit invalidation forces a rebuild even without changes.
	invalidateKnowledgeCache(dir)
	idx4, err := getKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("getKnowledgeIndex: %v", err)
	}
	if idx4 == idx3 {
		t.Error("invalidateKnowledgeCache did not force a rebuild")
	}
}

// ---------- bench helpers (3d-5) ----------

func TestLoadBenchSet(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bench.json"
	content := `{"queries":[{"query":"quantum","expected":["quantum-notes"],"k":3}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	set, err := loadBenchSet(path)
	if err != nil {
		t.Fatalf("loadBenchSet: %v", err)
	}
	if len(set.Queries) != 1 || set.Queries[0].Query != "quantum" || set.Queries[0].K != 3 {
		t.Errorf("loadBenchSet parsed wrong: %+v", set)
	}

	// Empty queries must fail.
	if err := os.WriteFile(dir+"/empty.json", []byte(`{"queries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBenchSet(dir + "/empty.json"); err == nil {
		t.Error("loadBenchSet accepted an empty set")
	}
}
