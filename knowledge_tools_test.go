// knowledge_tools_test.go - tests for the save_knowledge enrichment: auto-
// derived summary/excerpt, auto-linked related notes, GitHub project metadata
// capture, and the model-backed enrichment path with deterministic fallback.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveKnowledgeEnriches verifies that save_knowledge derives a summary and
// excerpt, links related existing notes, and captures GitHub metadata.
func TestSaveKnowledgeEnriches(t *testing.T) {
	dir := t.TempDir()
	// A pre-existing note that shares keywords with the new note.
	writeNoteFile(t, dir, "podcast-tools", "---\ntitle: \"Podcast Tools\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nTerminal podcast clients overview.\n")

	tools, err := createKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("createKnowledgeTools: %v", err)
	}

	// Stub the GitHub API fetch (no network in tests).
	orig := fetchGitHubMetadata
	fetchGitHubMetadata = func(owner, repo string) (map[string]string, error) {
		return map[string]string{
			"github_owner":       owner,
			"github_repo":        repo,
			"github_url":         "https://github.com/" + owner + "/" + repo,
			"github_language":    "Go",
			"github_stars":       "12",
			"github_maintainers": "amurru, someone",
		}, nil
	}
	defer func() { fetchGitHubMetadata = orig }()

	// Stub the summarization-model enrichment (no real model in tests).
	// The model proposes github_language "Rust"; the GitHub API stub returns
	// "Go" and must win the merge.
	origEnr := enrichKnowledgeFn
	enrichKnowledgeFn = func(ctx context.Context, prompt string) (string, error) {
		return `{"summary": "Model-generated summary of the podcast client.", "excerpt": "Model excerpt.", "tags": ["podcast", "tui", "go"], "aliases": ["GC"], "related": ["podcast-tools"], "metadata": {"github_owner": "amurru", "github_language": "Rust"}}`, nil
	}
	defer func() { enrichKnowledgeFn = origEnr }()

	// tools[0] is save_knowledge.
	out, err := runTool(t, tools[0], map[string]any{
		"title":   "Gocaster",
		"content": "Gocaster is a terminal podcast client written in Go. It is hosted at github.com/amurru/gocaster.",
		"sources": []any{"https://github.com/amurru/gocaster"},
		"tags":    []any{"go", "podcast"},
	})
	if err != nil {
		t.Fatalf("save_knowledge: %v", err)
	}

	// Model-produced summary and excerpt win over the deterministic ones.
	summary, _ := out["summary"].(string)
	if summary != "Model-generated summary of the podcast client." {
		t.Errorf("save_knowledge: summary got %q", summary)
	}
	excerpt, _ := out["excerpt"].(string)
	if excerpt != "Model excerpt." {
		t.Errorf("save_knowledge: excerpt got %q", excerpt)
	}

	// Auto-linked related note (model-selected, resolved against the index).
	related, _ := out["related"].([]any)
	if len(related) == 0 {
		t.Errorf("save_knowledge: expected auto-linked related notes, got %v", related)
	}
	found := false
	for _, r := range related {
		if s, _ := r.(string); s == "podcast-tools" {
			found = true
		}
	}
	if !found {
		t.Errorf("save_knowledge: related=%v, want podcast-tools", related)
	}

	// GitHub metadata: API values override the model's guesses.
	meta, _ := out["metadata"].(map[string]any)
	if meta["github_maintainers"] != "amurru, someone" {
		t.Errorf("save_knowledge: metadata got %v", meta)
	}
	if meta["github_language"] != "Go" {
		t.Errorf("save_knowledge: github_language got %v, want API value Go (model said Rust)", meta["github_language"])
	}
	if meta["github_stars"] != "12" {
		t.Errorf("save_knowledge: metadata got %v", meta)
	}

	// The persisted note contains the model summary/aliases, the API metadata,
	// explicit tags (input wins over model tags), and a Related section.
	data, err := os.ReadFile(filepath.Join(dir, "gocaster.md"))
	if err != nil {
		t.Fatalf("read saved note: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `summary: "Model-generated summary of the podcast client."`) {
		t.Errorf("saved note: model summary missing:\n%s", content)
	}
	if !strings.Contains(content, "aliases:") || !strings.Contains(content, `"GC"`) {
		t.Errorf("saved note: model aliases missing:\n%s", content)
	}
	if strings.Contains(content, `- "tui"`) {
		t.Errorf("saved note: model tag leaked over explicit tags:\n%s", content)
	}
	if !strings.Contains(content, `github_language: "Go"`) {
		t.Errorf("saved note: API github_language missing:\n%s", content)
	}
	if strings.Contains(content, "Rust") {
		t.Errorf("saved note: model github_language should be overridden by the API:\n%s", content)
	}
	if !strings.Contains(content, "## Related") || !strings.Contains(content, "[[podcast-tools|Podcast Tools]]") {
		t.Errorf("saved note: missing Related section with wikilink:\n%s", content)
	}
}

// TestSaveKnowledgeSkipsEnrichment verifies the tool still works without any
// GitHub reference and without related notes, deriving a fallback summary.
func TestSaveKnowledgeSkipsEnrichment(t *testing.T) {
	dir := t.TempDir()

	orig := fetchGitHubMetadata
	fetchGitHubMetadata = func(owner, repo string) (map[string]string, error) {
		t.Fatalf("fetchGitHubMetadata should not be called without a repo reference")
		return nil, nil
	}
	defer func() { fetchGitHubMetadata = orig }()

	tools, err := createKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("createKnowledgeTools: %v", err)
	}

	out, err := runTool(t, tools[0], map[string]any{
		"title":   "Lonely Note",
		"content": "Just a standalone observation with no repo and no related knowledge.",
	})
	if err != nil {
		t.Fatalf("save_knowledge: %v", err)
	}
	summary, _ := out["summary"].(string)
	if !strings.Contains(summary, "standalone observation") {
		t.Errorf("save_knowledge: summary got %q", summary)
	}
	if related, _ := out["related"].([]any); len(related) != 0 {
		t.Errorf("save_knowledge: expected no related notes, got %v", related)
	}
	if meta, _ := out["metadata"].(map[string]any); len(meta) != 0 {
		t.Errorf("save_knowledge: expected no metadata, got %v", meta)
	}
}

// TestSaveKnowledgeProvidedSummary verifies an explicit summary input wins
// over the auto-derived one.
func TestSaveKnowledgeProvidedSummary(t *testing.T) {
	dir := t.TempDir()
	tools, err := createKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("createKnowledgeTools: %v", err)
	}

	out, err := runTool(t, tools[0], map[string]any{
		"title":   "Provided Summary",
		"content": "Body content that would produce a different summary.",
		"summary": "An explicit, human-written summary.",
	})
	if err != nil {
		t.Fatalf("save_knowledge: %v", err)
	}
	if got, _ := out["summary"].(string); got != "An explicit, human-written summary." {
		t.Errorf("save_knowledge: summary got %q, want the provided one", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "provided-summary.md"))
	if err != nil {
		t.Fatalf("read saved note: %v", err)
	}
	if !strings.Contains(string(data), "An explicit, human-written summary.") {
		t.Errorf("saved note: provided summary missing:\n%s", string(data))
	}
}

// ------------------- model-backed enrichment -----------------------------------

func TestBuildKnowledgeEnrichPrompt(t *testing.T) {
	candidates := []*KnowledgeNote{
		{Slug: "podcast-tools", Frontmatter: KnowledgeFrontmatter{Title: "Podcast Tools", Summary: "Terminal podcast clients."}},
	}
	p := buildKnowledgeEnrichPrompt("Gocaster", "A terminal podcast client.", []string{"go"}, candidates)
	if !strings.Contains(p, "TITLE: Gocaster") {
		t.Errorf("prompt: missing title")
	}
	if !strings.Contains(p, "A terminal podcast client.") {
		t.Errorf("prompt: missing content")
	}
	if !strings.Contains(p, "- Podcast Tools (podcast-tools) - Terminal podcast clients.") {
		t.Errorf("prompt: missing existing note candidate:\n%s", p)
	}
	if !strings.Contains(p, "TAGS PROVIDED: go") {
		t.Errorf("prompt: missing tags")
	}
}

func TestParseKnowledgeEnrichment(t *testing.T) {
	// Plain JSON.
	enr := parseKnowledgeEnrichment(`{"summary": "A podcast client", "excerpt": "excerpt here", "tags": ["go", "tui"], "aliases": ["GC"], "related": ["podcast-tools"], "metadata": {"github_owner": "amurru"}}`)
	if enr == nil {
		t.Fatal("plain JSON: expected enrichment")
	}
	if enr.Summary != "A podcast client" || enr.Excerpt != "excerpt here" {
		t.Errorf("plain JSON: got %+v", enr)
	}
	if len(enr.Tags) != 2 || enr.Tags[0] != "go" {
		t.Errorf("plain JSON: tags got %v", enr.Tags)
	}
	if enr.Metadata["github_owner"] != "amurru" {
		t.Errorf("plain JSON: metadata got %v", enr.Metadata)
	}

	// Fenced JSON.
	enr = parseKnowledgeEnrichment("```json\n{\"summary\": \"Fenced summary\", \"metadata\": {}}\n```")
	if enr == nil || enr.Summary != "Fenced summary" {
		t.Errorf("fenced: got %+v", enr)
	}

	// Tolerant extraction from surrounding prose.
	enr = parseKnowledgeEnrichment("Here you go: {\"summary\": \"Extracted\", \"metadata\": {}} thanks!")
	if enr == nil || enr.Summary != "Extracted" {
		t.Errorf("tolerant: got %+v", enr)
	}

	// Malformed -> nil.
	if enr := parseKnowledgeEnrichment("not json at all"); enr != nil {
		t.Errorf("malformed: expected nil, got %+v", enr)
	}
	// Missing summary -> nil.
	if enr := parseKnowledgeEnrichment(`{"tags": ["x"]}`); enr != nil {
		t.Errorf("no summary: expected nil, got %+v", enr)
	}
	// Duplicates and empty entries are removed.
	enr = parseKnowledgeEnrichment(`{"summary": "S", "tags": ["go", "go", "tui"], "aliases": ["", "GC", "GC"], "metadata": {}}`)
	if enr == nil || len(enr.Tags) != 2 || len(enr.Aliases) != 1 {
		t.Errorf("dedupe: got %+v", enr)
	}
}

func TestModelEnrichKnowledge(t *testing.T) {
	dir := t.TempDir()
	writeNoteFile(t, dir, "podcast-tools", "---\ntitle: \"Podcast Tools\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nTerminal podcast clients.\n")
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		t.Fatalf("BuildKnowledgeIndex: %v", err)
	}

	// No callback configured -> nil (deterministic fallback).
	if enr := modelEnrichKnowledge("T", "content", nil, idx); enr != nil {
		t.Errorf("nil callback: expected nil, got %+v", enr)
	}

	orig := enrichKnowledgeFn

	// Callback error -> nil.
	enrichKnowledgeFn = func(ctx context.Context, prompt string) (string, error) {
		return "", fmt.Errorf("boom")
	}
	if enr := modelEnrichKnowledge("T", "content", nil, idx); enr != nil {
		t.Errorf("error callback: expected nil, got %+v", enr)
	}

	// Success -> parsed enrichment; the prompt lists existing note candidates.
	enrichKnowledgeFn = func(ctx context.Context, prompt string) (string, error) {
		if !strings.Contains(prompt, "podcast-tools") {
			t.Errorf("prompt should list existing note candidates")
		}
		return `{"summary": "Model summary", "related": ["podcast-tools"], "metadata": {}}`, nil
	}
	enr := modelEnrichKnowledge("T", "content", nil, idx)
	if enr == nil || enr.Summary != "Model summary" {
		t.Fatalf("success callback: got %+v", enr)
	}
	if len(enr.Related) != 1 || enr.Related[0] != "podcast-tools" {
		t.Errorf("success callback: related got %v", enr.Related)
	}

	// Garbage response -> nil.
	enrichKnowledgeFn = func(ctx context.Context, prompt string) (string, error) {
		return "I cannot produce JSON", nil
	}
	if enr := modelEnrichKnowledge("T", "content", nil, idx); enr != nil {
		t.Errorf("garbage response: expected nil, got %+v", enr)
	}

	enrichKnowledgeFn = orig
}

func TestSaveKnowledgeFallsBackWhenModelFails(t *testing.T) {
	dir := t.TempDir()
	orig := enrichKnowledgeFn
	enrichKnowledgeFn = func(ctx context.Context, prompt string) (string, error) {
		return "I refuse to produce JSON", nil
	}
	defer func() { enrichKnowledgeFn = orig }()

	tools, err := createKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("createKnowledgeTools: %v", err)
	}

	out, err := runTool(t, tools[0], map[string]any{
		"title":   "Fallback Note",
		"content": "The fallback note contains a terminal podcast client sentence.",
	})
	if err != nil {
		t.Fatalf("save_knowledge: %v", err)
	}
	summary, _ := out["summary"].(string)
	if !strings.Contains(summary, "terminal podcast client") {
		t.Errorf("save_knowledge: deterministic fallback summary got %q", summary)
	}
}
