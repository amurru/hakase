// knowledge_test.go - tests for the knowledge base parser, index, search, and
// persistence (knowledge.go, knowledge_tools.go).
//
// All tests use t.TempDir() and os.WriteFile(..., 0o644) so nothing is ever
// written outside the temp directory.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNoteFile writes <dir>/<slug>.md with the given content, creating the
// directory if needed.
func writeNoteFile(t *testing.T, dir, slug, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("writeNoteFile: mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeNoteFile: write %s: %v", path, err)
	}
}

// ------------------- ParseKnowledgeNote ---------------------------------------

func TestParseKnowledgeNoteFullValid(t *testing.T) {
	content := "---\n" +
		"title: \"Quantum Computing\"\n" +
		"aliases:\n" +
		"  - \"QC\"\n" +
		"  - \"Quantum\"\n" +
		"tags:\n" +
		"  - \"physics\"\n" +
		"  - \"computing\"\n" +
		"created: \"2024-01-01\"\n" +
		"updated: \"2024-01-02\"\n" +
		"status: \"draft\"\n" +
		"confidence: \"high\"\n" +
		"sources:\n" +
		"  - url: \"https://example.com\"\n" +
		"  - path: \"raw/paper.pdf\"\n" +
		"summary: \"A note about quantum computing\"\n" +
		"related:\n" +
		"  - \"Superposition\"\n" +
		"---\n\n" +
		"Body text with [[wikilinks]].\n"

	path := filepath.Join(t.TempDir(), "quantum-computing.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	note, err := ParseKnowledgeNote(path, data)
	if err != nil {
		t.Fatalf("ParseKnowledgeNote: unexpected error: %v", err)
	}

	if note.Frontmatter.Title != "Quantum Computing" {
		t.Errorf("Title: got %q, want %q", note.Frontmatter.Title, "Quantum Computing")
	}
	if len(note.Frontmatter.Aliases) != 2 || note.Frontmatter.Aliases[0] != "QC" || note.Frontmatter.Aliases[1] != "Quantum" {
		t.Errorf("Aliases: got %v", note.Frontmatter.Aliases)
	}
	if len(note.Frontmatter.Tags) != 2 || note.Frontmatter.Tags[0] != "physics" || note.Frontmatter.Tags[1] != "computing" {
		t.Errorf("Tags: got %v", note.Frontmatter.Tags)
	}
	if note.Frontmatter.Created != "2024-01-01" {
		t.Errorf("Created: got %q", note.Frontmatter.Created)
	}
	if note.Frontmatter.Updated != "2024-01-02" {
		t.Errorf("Updated: got %q", note.Frontmatter.Updated)
	}
	if note.Frontmatter.Status != "draft" {
		t.Errorf("Status: got %q", note.Frontmatter.Status)
	}
	if note.Frontmatter.Confidence != "high" {
		t.Errorf("Confidence: got %q", note.Frontmatter.Confidence)
	}
	if len(note.Frontmatter.Sources) != 2 {
		t.Fatalf("Sources: expected 2, got %d", len(note.Frontmatter.Sources))
	}
	if note.Frontmatter.Sources[0].URL != "https://example.com" {
		t.Errorf("Sources[0].URL: got %q", note.Frontmatter.Sources[0].URL)
	}
	if note.Frontmatter.Sources[1].Path != "raw/paper.pdf" {
		t.Errorf("Sources[1].Path: got %q", note.Frontmatter.Sources[1].Path)
	}
	if note.Frontmatter.Summary != "A note about quantum computing" {
		t.Errorf("Summary: got %q", note.Frontmatter.Summary)
	}
	if len(note.Frontmatter.Related) != 1 || note.Frontmatter.Related[0] != "Superposition" {
		t.Errorf("Related: got %v", note.Frontmatter.Related)
	}
	if !strings.Contains(note.Body, "Body text with [[wikilinks]]") {
		t.Errorf("Body: got %q", note.Body)
	}
	if note.Slug != "quantum-computing" {
		t.Errorf("Slug: got %q", note.Slug)
	}
}

func TestParseKnowledgeNoteBOM(t *testing.T) {
	content := "\xEF\xBB\xBF---\ntitle: \"BOM Note\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nBody\n"
	path := filepath.Join(t.TempDir(), "bom-note.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	note, err := ParseKnowledgeNote(path, data)
	if err != nil {
		t.Fatalf("BOM parse: unexpected error: %v", err)
	}
	if note.Frontmatter.Title != "BOM Note" {
		t.Errorf("Title: got %q, want %q", note.Frontmatter.Title, "BOM Note")
	}
	if !strings.Contains(note.Body, "Body") {
		t.Errorf("Body: got %q", note.Body)
	}
}

func TestParseKnowledgeNoteCRLF(t *testing.T) {
	content := "---\r\ntitle: \"CRLF Note\"\r\ncreated: \"2024-01-01\"\r\nupdated: \"2024-01-01\"\r\n---\r\n\r\nBody line\r\n"
	path := filepath.Join(t.TempDir(), "crlf-note.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	note, err := ParseKnowledgeNote(path, data)
	if err != nil {
		t.Fatalf("CRLF parse: unexpected error: %v", err)
	}
	if note.Frontmatter.Title != "CRLF Note" {
		t.Errorf("Title: got %q, want %q", note.Frontmatter.Title, "CRLF Note")
	}
	if !strings.Contains(note.Body, "Body line") {
		t.Errorf("Body: got %q", note.Body)
	}
}

func TestParseKnowledgeNoteMissingClosing(t *testing.T) {
	content := "---\ntitle: \"No Close\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nBody without closing delimiter\n"
	path := filepath.Join(t.TempDir(), "no-close.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	_, err := ParseKnowledgeNote(path, data)
	if err == nil {
		t.Fatal("expected error for missing closing ---")
	}
	if !strings.Contains(err.Error(), "missing closing ---") {
		t.Errorf("error: got %q, want substring %q", err, "missing closing ---")
	}
}

func TestParseKnowledgeNoteNotAtStart(t *testing.T) {
	content := "some text\n---\ntitle: \"Late\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\nBody\n"
	path := filepath.Join(t.TempDir(), "late.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	_, err := ParseKnowledgeNote(path, data)
	if err == nil {
		t.Fatal("expected error for frontmatter not at byte 0")
	}
	if !strings.Contains(err.Error(), "frontmatter must start with ---") {
		t.Errorf("error: got %q, want substring %q", err, "frontmatter must start with ---")
	}
}

func TestParseKnowledgeNoteTolerantFallback(t *testing.T) {
	// sources as a list of ints cannot unmarshal into []KnowledgeSource,
	// but the minimal Title/Summary fallback should succeed.
	content := "---\ntitle: \"Bad YAML Note\"\nsummary: \"Has bad sources\"\nsources: [1, 2]\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nBody\n"
	path := filepath.Join(t.TempDir(), "bad-yaml.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	note, err := ParseKnowledgeNote(path, data)
	if err != nil {
		t.Fatalf("tolerant fallback: unexpected error: %v", err)
	}
	if note.Frontmatter.Title != "Bad YAML Note" {
		t.Errorf("Title: got %q, want %q", note.Frontmatter.Title, "Bad YAML Note")
	}
	if note.Frontmatter.Summary != "Has bad sources" {
		t.Errorf("Summary: got %q, want %q", note.Frontmatter.Summary, "Has bad sources")
	}
	if !strings.Contains(note.Body, "Body") {
		t.Errorf("Body: got %q", note.Body)
	}
}

// ------------------- knowledgeDir ----------------------------------------------

func TestKnowledgeDirDefault(t *testing.T) {
	if got := knowledgeDir(""); got != "./knowledge" {
		t.Errorf("knowledgeDir(\"\"): expected %q, got %q", "./knowledge", got)
	}
	if got := knowledgeDir("custom/dir"); got != "custom/dir" {
		t.Errorf("knowledgeDir(custom): expected %q, got %q", "custom/dir", got)
	}
}

func TestKnowledgeDirTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := knowledgeDir("~/notes"); got != filepath.Join(home, "notes") {
		t.Errorf("knowledgeDir(~): expected %q, got %q", filepath.Join(home, "notes"), got)
	}
	if got := knowledgeDir("~"); got != home {
		t.Errorf("knowledgeDir(~ alone): expected %q, got %q", home, got)
	}
	// Absolute paths are passed through untouched.
	if got := knowledgeDir("/abs/path"); got != "/abs/path" {
		t.Errorf("knowledgeDir(abs): expected %q, got %q", "/abs/path", got)
	}
}

// ------------------- Slugify --------------------------------------------------

func TestSlugify(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Quantum Computing", "quantum-computing"},
		{"A B C", "a-b-c"},
		{"  hello world  ", "hello-world"},
		{"", "note"},
		{"!!!", "note"},
		{"---", "note"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)}, // 64 chars: valid
		{strings.Repeat("a", 65), "note"},                  // 65 chars: too long
		{"Hello, World!", "hello-world"},
		{"note", "note"},
	}
	for _, c := range cases {
		got := Slugify(c.input)
		if got != c.want {
			t.Errorf("Slugify(%q): got %q, want %q", c.input, got, c.want)
		}
	}
}

// ------------------- ExtractWikilinks ------------------------------------------

func TestExtractWikilinks(t *testing.T) {
	// Plain target.
	got := ExtractWikilinks("text [[target]] more")
	if len(got) != 1 || got[0] != "target" {
		t.Errorf("plain: got %v, want [target]", got)
	}

	// Target with label -> only target.
	got = ExtractWikilinks("[[target|label]]")
	if len(got) != 1 || got[0] != "target" {
		t.Errorf("label: got %v, want [target]", got)
	}

	// Target with heading -> only target.
	got = ExtractWikilinks("[[target#heading]]")
	if len(got) != 1 || got[0] != "target" {
		t.Errorf("heading: got %v, want [target]", got)
	}

	// Multiple links in order.
	got = ExtractWikilinks("[[alpha]] [[beta]] [[gamma]]")
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Errorf("multiple: got %v, want [alpha beta gamma]", got)
	}

	// Links in fenced code blocks are skipped.
	got = ExtractWikilinks("[[keep]]\n```\ncode [[skip]]\n```\nmore [[also-keep]]")
	if len(got) != 2 || got[0] != "keep" || got[1] != "also-keep" {
		t.Errorf("fenced: got %v, want [keep also-keep]", got)
	}

	// Dedupe preserving order.
	got = ExtractWikilinks("[[a]] [[a]] [[b]] [[a]]")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("dedupe: got %v, want [a b]", got)
	}
}

// ------------------- BuildKnowledgeIndex ---------------------------------------

func TestBuildKnowledgeIndex(t *testing.T) {
	dir := t.TempDir()

	writeNoteFile(t, dir, "alpha", "---\ntitle: \"Alpha\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nAlpha body\n")
	writeNoteFile(t, dir, "beta", "---\ntitle: \"Beta\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nBeta body\n")

	// index.md and log.md should be excluded even with valid frontmatter.
	writeNoteFile(t, dir, "index", "---\ntitle: \"Index\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nshould be excluded\n")
	writeNoteFile(t, dir, "log", "---\ntitle: \"Log\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nshould be excluded\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	if len(idx.BySlug) != 2 {
		t.Errorf("BySlug: expected 2 notes, got %d (%v)", len(idx.BySlug), idx.BySlug)
	}
	if _, ok := idx.BySlug["alpha"]; !ok {
		t.Errorf("BySlug missing alpha")
	}
	if _, ok := idx.BySlug["beta"]; !ok {
		t.Errorf("BySlug missing beta")
	}
	if _, ok := idx.BySlug["index"]; ok {
		t.Errorf("BySlug should not contain index")
	}
	if _, ok := idx.BySlug["log"]; ok {
		t.Errorf("BySlug should not contain log")
	}

	if _, ok := idx.ByBasename["alpha"]; !ok {
		t.Errorf("ByBasename missing alpha")
	}
	if _, ok := idx.ByBasename["beta"]; !ok {
		t.Errorf("ByBasename missing beta")
	}
}

func TestBuildKnowledgeIndexNotesSubdirPreference(t *testing.T) {
	dir := t.TempDir()

	// Same slug root at dir root and under notes/. The notes/ copy wins.
	writeNoteFile(t, dir, "quantum-computing", "---\ntitle: \"Root Copy\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nroot body\n")
	writeNoteFile(t, filepath.Join(dir, "notes"), "quantum-computing", "---\ntitle: \"Notes Copy\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nnotes body\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	note, ok := idx.BySlug["quantum-computing"]
	if !ok {
		t.Fatal("BySlug missing quantum-computing")
	}
	if !strings.Contains(note.Body, "notes body") {
		t.Errorf("expected notes/ copy to win, body got %q", note.Body)
	}
	if !strings.HasPrefix(note.Path, filepath.Join(dir, "notes")) {
		t.Errorf("expected path under notes/, got %q", note.Path)
	}
}

func TestBuildKnowledgeIndexMalformedSkipped(t *testing.T) {
	dir := t.TempDir()

	writeNoteFile(t, dir, "good", "---\ntitle: \"Good\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody\n")
	writeNoteFile(t, dir, "bad", "no frontmatter here\njust text")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}
	if len(idx.BySlug) != 1 {
		t.Errorf("expected 1 note (bad skipped), got %d", len(idx.BySlug))
	}
	if _, ok := idx.BySlug["good"]; !ok {
		t.Errorf("expected good note in index")
	}
}

// ------------------- ResolveTarget --------------------------------------------

func TestResolveTarget(t *testing.T) {
	dir := t.TempDir()

	writeNoteFile(t, dir, "quantum-computing", "---\ntitle: \"Quantum Computing\"\naliases:\n  - \"QC\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody\n")
	writeNoteFile(t, dir, "superposition", "---\ntitle: \"Superposition\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbody\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	// Exact slug.
	note, ok := ResolveTarget(idx, "quantum-computing")
	if !ok || note.Frontmatter.Title != "Quantum Computing" {
		t.Errorf("exact slug: got (%v, %q)", ok, note.Frontmatter.Title)
	}

	// Case-insensitive slug / basename ("Superposition" -> "superposition").
	note, ok = ResolveTarget(idx, "Superposition")
	if !ok || note.Slug != "superposition" {
		t.Errorf("case-insensitive: got (%v, %q)", ok, note.Slug)
	}

	// Alias case-insensitive ("qc" -> alias "QC").
	note, ok = ResolveTarget(idx, "qc")
	if !ok || note.Slug != "quantum-computing" {
		t.Errorf("alias case-insensitive: got (%v, %q)", ok, note.Slug)
	}

	// Unknown -> false.
	_, ok = ResolveTarget(idx, "nonexistent")
	if ok {
		t.Errorf("unknown target: expected false")
	}
}

func TestResolveTargetAmbiguousBasename(t *testing.T) {
	dir := t.TempDir()

	// Two notes with the same filename stem in different subdirs. Both
	// produce slug "dup" and basename stem "dup". ByBasename["dup"] will
	// contain two entries (the preference logic only deduplicates BySlug).
	writeNoteFile(t, filepath.Join(dir, "alpha"), "dup", "---\ntitle: \"Dup Alpha\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nalpha\n")
	writeNoteFile(t, filepath.Join(dir, "beta"), "dup", "---\ntitle: \"Dup Beta\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nbeta\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	// ByBasename should have 2 entries for "dup", documenting the ambiguity.
	if slugs, ok := idx.ByBasename["dup"]; !ok || len(slugs) != 2 {
		t.Errorf("ByBasename[dup]: expected 2 entries, got %v", slugs)
	}

	// ResolveTarget resolves via BySlug (the slug check comes first), so
	// the note is found regardless of the basename ambiguity.
	note, ok := ResolveTarget(idx, "dup")
	if !ok {
		t.Errorf("ResolveTarget(dup): expected to resolve via BySlug, got false")
	}
	if note == nil {
		t.Fatal("ResolveTarget(dup): note is nil")
	}
	if note.Slug != "dup" {
		t.Errorf("ResolveTarget(dup): slug got %q", note.Slug)
	}
}

// ------------------- SearchKnowledge -------------------------------------------

func TestSearchKnowledge(t *testing.T) {
	dir := t.TempDir()

	writeNoteFile(t, dir, "alpha", "---\ntitle: \"Alpha\"\ntags:\n  - \"x\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nstatus: \"draft\"\n---\n\ncontains quantum here\n")
	writeNoteFile(t, dir, "beta", "---\ntitle: \"Beta\"\ntags:\n  - \"x\"\n  - \"y\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nstatus: \"archived\"\n---\n\nother text\n")
	writeNoteFile(t, dir, "gamma", "---\ntitle: \"Gamma\"\ntags:\n  - \"y\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nstatus: \"draft\"\n---\n\nquantum mechanics\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	// Substring in body: "quantum" matches Alpha and Gamma.
	results := SearchKnowledge(idx, "quantum", nil, false)
	if len(results) != 2 {
		t.Fatalf("substring body: expected 2 results, got %d", len(results))
	}
	// Sorted by title: Alpha before Gamma.
	if results[0].Frontmatter.Title != "Alpha" || results[1].Frontmatter.Title != "Gamma" {
		t.Errorf("sorted: got %q, %q", results[0].Frontmatter.Title, results[1].Frontmatter.Title)
	}

	// Tag filter requires ALL tags: ["x"] -> only Alpha (Gamma has y, not x).
	results = SearchKnowledge(idx, "quantum", []string{"x"}, false)
	if len(results) != 1 || results[0].Frontmatter.Title != "Alpha" {
		t.Errorf("tag x: got %v", results)
	}

	// Tag filter ["y"] -> only Gamma.
	results = SearchKnowledge(idx, "quantum", []string{"y"}, false)
	if len(results) != 1 || results[0].Frontmatter.Title != "Gamma" {
		t.Errorf("tag y: got %v", results)
	}

	// Tag filter ["x", "y"] -> none (no single note has both and "quantum").
	results = SearchKnowledge(idx, "quantum", []string{"x", "y"}, false)
	if len(results) != 0 {
		t.Errorf("tags x+y: expected 0, got %d", len(results))
	}

	// Archived excluded by default: empty query, no tags -> Alpha and Gamma.
	results = SearchKnowledge(idx, "", nil, false)
	if len(results) != 2 {
		t.Errorf("no archived: expected 2, got %d", len(results))
	}

	// Archived included with includeArchived=true: Alpha, Beta, Gamma.
	results = SearchKnowledge(idx, "", nil, true)
	if len(results) != 3 {
		t.Fatalf("with archived: expected 3, got %d", len(results))
	}
	if results[0].Frontmatter.Title != "Alpha" || results[1].Frontmatter.Title != "Beta" || results[2].Frontmatter.Title != "Gamma" {
		t.Errorf("sorted with archived: got %q, %q, %q", results[0].Frontmatter.Title, results[1].Frontmatter.Title, results[2].Frontmatter.Title)
	}
}

// ------------------- Backlinks and Dangling ------------------------------------

func TestBacklinksAndDangling(t *testing.T) {
	dir := t.TempDir()

	// Note "a" links to [[b]] (exists) and [[missing]] (does not exist).
	writeNoteFile(t, dir, "a", "---\ntitle: \"A\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nLinks: [[b]] and [[missing]].\n")
	writeNoteFile(t, dir, "b", "---\ntitle: \"B\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nNo links.\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	// Backlinks["b"] should contain "a".
	backlinks := idx.Backlinks["b"]
	found := false
	for _, s := range backlinks {
		if s == "a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Backlinks[b]: expected to contain %q, got %v", "a", backlinks)
	}

	// Dangling["missing"] should contain "a".
	dangling := idx.Dangling["missing"]
	found = false
	for _, s := range dangling {
		if s == "a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Dangling[missing]: expected to contain %q, got %v", "a", dangling)
	}
}

// ------------------- SaveNote / UpdateNote -------------------------------------

func TestSaveUpdateAtomicity(t *testing.T) {
	dir := t.TempDir()

	note := &KnowledgeNote{
		Slug: "test-note",
		Frontmatter: KnowledgeFrontmatter{
			Title:   "Test Note",
			Created: "2024-01-01",
			Updated: "2024-01-01",
		},
		Body: "Original body.\n",
	}
	serializeNote(note)

	// SaveNote creates the file.
	if err := SaveNote(dir, note); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}

	// Duplicate SaveNote errors.
	if err := SaveNote(dir, note); err == nil {
		t.Errorf("duplicate SaveNote: expected error, got nil")
	}

	// UpdateNote overwrites.
	note.Body = "Updated body.\n"
	serializeNote(note)
	if err := UpdateNote(dir, note); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	// Content matches latest body.
	data, err := os.ReadFile(filepath.Join(dir, "test-note.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "Updated body.") {
		t.Errorf("file content: expected updated body, got:\n%s", string(data))
	}
	if strings.Contains(string(data), "Original body.") {
		t.Errorf("file content: original body should be gone, got:\n%s", string(data))
	}
}

// ------------------- AppendLog -------------------------------------------------

func TestAppendLog(t *testing.T) {
	dir := t.TempDir()

	// First call: creates log.md with header.
	if err := AppendLog(dir, "save", "First Note"); err != nil {
		t.Fatalf("AppendLog 1: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# Knowledge Log") {
		t.Errorf("first call: expected header, got:\n%s", content)
	}
	if !strings.Contains(content, "save | First Note") {
		t.Errorf("first call: expected entry, got:\n%s", content)
	}
	if !strings.Contains(content, "## [") {
		t.Errorf("first call: expected timestamp format, got:\n%s", content)
	}

	// Second call: appends, keeps prior entry.
	if err := AppendLog(dir, "update", "Second Note"); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}

	data, err = os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md 2: %v", err)
	}
	content = string(data)
	if !strings.Contains(content, "save | First Note") {
		t.Errorf("second call: first entry should persist, got:\n%s", content)
	}
	if !strings.Contains(content, "update | Second Note") {
		t.Errorf("second call: new entry missing, got:\n%s", content)
	}
}

// ------------------- serializeNote round-trip ----------------------------------

func TestSerializeNoteRoundTrip(t *testing.T) {
	original := &KnowledgeNote{
		Slug: "special-chars",
		Frontmatter: KnowledgeFrontmatter{
			Title:   `He said "hi" \ end`,
			Tags:    []string{"physics", "computing"},
			Created: "2024-01-01",
			Updated: "2024-01-02",
			Status:  "draft",
			Summary: "A note with special characters",
		},
		Body: "Body with [[wikilinks]] and special chars.\n",
	}

	raw := serializeNote(original)

	// Parse back.
	parsed, err := ParseKnowledgeNote("special-chars.md", raw)
	if err != nil {
		t.Fatalf("ParseKnowledgeNote after serializeNote: %v", err)
	}

	if parsed.Frontmatter.Title != original.Frontmatter.Title {
		t.Errorf("Title round-trip: got %q, want %q", parsed.Frontmatter.Title, original.Frontmatter.Title)
	}
	if parsed.Frontmatter.Summary != original.Frontmatter.Summary {
		t.Errorf("Summary round-trip: got %q, want %q", parsed.Frontmatter.Summary, original.Frontmatter.Summary)
	}
	if len(parsed.Frontmatter.Tags) != 2 || parsed.Frontmatter.Tags[0] != "physics" || parsed.Frontmatter.Tags[1] != "computing" {
		t.Errorf("Tags round-trip: got %v", parsed.Frontmatter.Tags)
	}
	if parsed.Frontmatter.Created != "2024-01-01" {
		t.Errorf("Created round-trip: got %q", parsed.Frontmatter.Created)
	}
	if parsed.Frontmatter.Updated != "2024-01-02" {
		t.Errorf("Updated round-trip: got %q", parsed.Frontmatter.Updated)
	}
	if parsed.Frontmatter.Status != "draft" {
		t.Errorf("Status round-trip: got %q", parsed.Frontmatter.Status)
	}
	if !strings.Contains(parsed.Body, "Body with [[wikilinks]]") {
		t.Errorf("Body round-trip: got %q", parsed.Body)
	}
}

// ------------------- enrichment helpers ---------------------------------------

func TestDeriveSummary(t *testing.T) {
	content := "# Gocaster\n\nGocaster is a terminal-based podcast client written in Go.\nIt enables users to browse feeds and play episodes.\n"
	got := deriveSummary(content, "Gocaster")
	if got == "" {
		t.Fatal("deriveSummary: empty summary")
	}
	if !strings.Contains(got, "Gocaster is a terminal-based podcast client written in Go") {
		t.Errorf("deriveSummary: got %q", got)
	}
	if len([]rune(got)) > 240 {
		t.Errorf("deriveSummary: too long (%d runes)", len([]rune(got)))
	}

	// Empty content falls back to the title.
	if got := deriveSummary("", "Fallback Title"); got != "Fallback Title" {
		t.Errorf("deriveSummary(empty): got %q, want fallback title", got)
	}

	// Fenced code blocks are excluded from the summary.
	code := "```go\nfunc main() {}\n```\n\nReal prose sentence here."
	got = deriveSummary(code, "T")
	if strings.Contains(got, "func main") {
		t.Errorf("deriveSummary: code leaked into summary: %q", got)
	}
	if !strings.Contains(got, "Real prose sentence here.") {
		t.Errorf("deriveSummary: prose not kept: %q", got)
	}
}

func TestDeriveExcerpt(t *testing.T) {
	if got := deriveExcerpt("Just a short note."); got != "Just a short note." {
		t.Errorf("deriveExcerpt(short): got %q", got)
	}
	long := deriveExcerpt(strings.Repeat("word ", 200)) // ~1000 chars
	if len([]rune(long)) != 303 {
		t.Errorf("deriveExcerpt(long): expected 303 runes (300 + ...), got %d", len([]rune(long)))
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("deriveExcerpt(long): expected truncation marker, got %q", long[len(long)-10:])
	}
	if got := deriveExcerpt(""); got != "" {
		t.Errorf("deriveExcerpt(empty): got %q, want empty", got)
	}
}

func TestFindRelatedNotes(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "podcast-clients", "---\ntitle: \"Podcast Clients\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nA comparison of podcast clients.\n")
	writeNoteFile(t, dir, "bubbletea", "---\ntitle: \"Bubbletea TUI\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nA TUI framework written in Go used for terminal interfaces.\n")
	writeNoteFile(t, dir, "unrelated", "---\ntitle: \"Cooking Recipes\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nHow to bake bread.\n")
	writeNoteFile(t, dir, "old", "---\ntitle: \"Old Go Notes\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nstatus: \"archived\"\n---\n\nTerminal podcast client in Go.\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	newNote := &KnowledgeNote{
		Slug: "gocaster",
		Frontmatter: KnowledgeFrontmatter{
			Title: "Gocaster",
			Tags:  []string{"go", "podcast", "tui"},
		},
		Body: "A terminal podcast client written in Go using bubbletea.\n",
	}

	related := findRelatedNotes(idx, newNote, 5)
	if len(related) < 2 {
		t.Fatalf("findRelatedNotes: expected at least 2 related notes, got %d (%v)", len(related), related)
	}
	// Podcast Clients shares title+body keywords and should rank first.
	if related[0].Slug != "podcast-clients" {
		t.Errorf("findRelatedNotes: first match got %q, want podcast-clients", related[0].Slug)
	}
	for _, r := range related {
		if r.Slug == "unrelated" {
			t.Errorf("findRelatedNotes: unrelated note was linked")
		}
		if r.Slug == "old" {
			t.Errorf("findRelatedNotes: archived note was linked")
		}
	}

	// maxResults cap.
	if got := findRelatedNotes(idx, newNote, 1); len(got) != 1 {
		t.Errorf("findRelatedNotes(cap 1): expected 1 result, got %d", len(got))
	}

	// No keywords -> no related notes.
	if got := findRelatedNotes(idx, &KnowledgeNote{Slug: "empty", Frontmatter: KnowledgeFrontmatter{Title: "x"}, Body: "a b"}, 5); len(got) != 0 {
		t.Errorf("findRelatedNotes(no keywords): expected 0, got %d", len(got))
	}
}

// containsPair reports whether pairs contains c.
func containsPair(pairs [][2]string, c [2]string) bool {
	for _, p := range pairs {
		if p == c {
			return true
		}
	}
	return false
}

func TestExtractGitHubRepos(t *testing.T) {
	// Explicit URLs plus bare mentions; "amurru/gocaster" in prose is a bare
	// duplicate of the URL candidate and is deduped. "land/bubbletea" inside
	// "charm.land/bubbletea" is a bare candidate (validated later by the API).
	content := "See https://github.com/amurru/gocaster. Also mentions amurru/gocaster and charm.land/bubbletea"
	explicit, bare := extractGitHubRepos(content, []string{"https://github.com/other/thing"})

	if len(explicit) != 2 {
		t.Fatalf("extractGitHubRepos: explicit got %v, want 2 entries", explicit)
	}
	if explicit[0] != [2]string{"amurru", "gocaster"} {
		t.Errorf("extractGitHubRepos: explicit[0] got %v", explicit[0])
	}
	if !containsPair(explicit, [2]string{"other", "thing"}) {
		t.Errorf("extractGitHubRepos: explicit missing source URL repo, got %v", explicit)
	}
	if len(bare) != 1 || bare[0] != [2]string{"land", "bubbletea"} {
		t.Errorf("extractGitHubRepos: bare got %v, want [land bubbletea]", bare)
	}

	// Bare-only mention.
	explicit, bare = extractGitHubRepos("Project amurru/gocaster is cool.", nil)
	if len(explicit) != 0 {
		t.Errorf("extractGitHubRepos bare-only: explicit got %v, want none", explicit)
	}
	if !containsPair(bare, [2]string{"amurru", "gocaster"}) {
		t.Errorf("extractGitHubRepos bare-only: bare got %v, want amurru/gocaster", bare)
	}

	// No repos at all.
	explicit, bare = extractGitHubRepos("Just some prose without repos.", nil)
	if len(explicit) != 0 || len(bare) != 0 {
		t.Errorf("extractGitHubRepos none: explicit=%v bare=%v, want both empty", explicit, bare)
	}
}

func TestEnrichGitHubMetadataFetchFailure(t *testing.T) {
	orig := fetchGitHubMetadata
	fetchGitHubMetadata = func(owner, repo string) (map[string]string, error) {
		return nil, fmt.Errorf("stubbed fetch failure")
	}
	defer func() { fetchGitHubMetadata = orig }()

	// Bare mention with a failed fetch -> no metadata recorded (not validated).
	if m := enrichGitHubMetadata("Project amurru/gocaster is a podcast client.", nil); m != nil {
		t.Errorf("enrichGitHubMetadata(bare+fail): expected nil, got %v", m)
	}

	// Explicit github.com URL with a failed fetch -> owner/repo/url still kept.
	m := enrichGitHubMetadata("See https://github.com/amurru/gocaster", nil)
	if m == nil {
		t.Fatal("enrichGitHubMetadata(explicit+fail): expected owner/repo metadata")
	}
	if m["github_owner"] != "amurru" || m["github_repo"] != "gocaster" {
		t.Errorf("enrichGitHubMetadata(explicit+fail): got %v", m)
	}
	if m["github_url"] != "https://github.com/amurru/gocaster" {
		t.Errorf("enrichGitHubMetadata(explicit+fail): url got %q", m["github_url"])
	}
}

func TestSerializeNoteMetadata(t *testing.T) {
	note := &KnowledgeNote{
		Slug: "m",
		Frontmatter: KnowledgeFrontmatter{
			Title:    "M",
			Created:  "2024-01-01",
			Updated:  "2024-01-01",
			Metadata: map[string]string{"github_stars": "12", "github_owner": "amurru"},
		},
		Body: "body\n",
	}
	raw := string(serializeNote(note))
	if !strings.Contains(raw, "metadata:") || !strings.Contains(raw, `github_owner: "amurru"`) {
		t.Errorf("serializeNote: metadata missing from frontmatter:\n%s", raw)
	}
	// Keys are sorted for determinism.
	ownerIdx := strings.Index(raw, "github_owner")
	starsIdx := strings.Index(raw, "github_stars")
	if ownerIdx == -1 || starsIdx == -1 || ownerIdx > starsIdx {
		t.Errorf("serializeNote: metadata keys not sorted (owner=%d stars=%d)", ownerIdx, starsIdx)
	}

	parsed, err := ParseKnowledgeNote("m.md", []byte(raw))
	if err != nil {
		t.Fatalf("ParseKnowledgeNote: %v", err)
	}
	if parsed.Frontmatter.Metadata["github_owner"] != "amurru" || parsed.Frontmatter.Metadata["github_stars"] != "12" {
		t.Errorf("ParseKnowledgeNote: metadata got %v", parsed.Frontmatter.Metadata)
	}
}

func TestSearchKnowledgeMetadata(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "gocaster", "---\ntitle: \"Gocaster\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\nmetadata:\n  github_maintainers: \"amurru\"\n  github_language: \"Go\"\n---\n\nBody text.\n")
	writeNoteFile(t, dir, "other", "---\ntitle: \"Other\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nBody text.\n")

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	results := SearchKnowledge(idx, "amurru", nil, false)
	if len(results) != 1 || results[0].Slug != "gocaster" {
		t.Errorf("SearchKnowledge(amurru): got %v", results)
	}
	results = SearchKnowledge(idx, "Go", nil, false)
	if len(results) != 1 || results[0].Slug != "gocaster" {
		t.Errorf("SearchKnowledge(Go): got %v", results)
	}
}
