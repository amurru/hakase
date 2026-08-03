// knowledge_cli_test.go - tests for the `hakase knowledge` CLI (knowledge_cli.go).
//
// All tests drive runKnowledgeCLI directly (exit codes, not os.Exit) and always
// pass --dir with t.TempDir() so nothing is ever written outside the temp
// directory.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout is defined in skill_cli_test.go and reused here.

func TestRunKnowledgeCLIUsage(t *testing.T) {
	// No args -> help -> exit 0.
	if code := runKnowledgeCLI(nil); code != 0 {
		t.Errorf("no args: expected exit code 0, got %d", code)
	}

	// Unknown subcommand -> exit 2.
	if code := runKnowledgeCLI([]string{"bogus"}); code != 2 {
		t.Errorf("unknown subcommand: expected exit code 2, got %d", code)
	}
}

func TestRunKnowledgeCreate(t *testing.T) {
	dir := t.TempDir()

	// create "Test Topic" --dir <temp> --tags a,b -> 0.
	if code := runKnowledgeCLI([]string{"create", "Test Topic", "--dir", dir, "--tags", "a,b"}); code != 0 {
		t.Fatalf("create: expected exit code 0, got %d", code)
	}

	// File exists with valid frontmatter.
	path := filepath.Join(dir, "test-topic.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "title: 'Test Topic'") {
		t.Errorf("file: expected single-quoted title, got:\n%s", content)
	}

	// Parse the written file and verify tags round-trip.
	note, err := ParseKnowledgeNote(path, data)
	if err != nil {
		t.Fatalf("ParseKnowledgeNote: %v", err)
	}
	if note.Frontmatter.Title != "Test Topic" {
		t.Errorf("Title: got %q, want %q", note.Frontmatter.Title, "Test Topic")
	}
	if len(note.Frontmatter.Tags) != 2 || note.Frontmatter.Tags[0] != "a" || note.Frontmatter.Tags[1] != "b" {
		t.Errorf("Tags: got %v, want [a b]", note.Frontmatter.Tags)
	}

	// Duplicate create -> 1.
	if code := runKnowledgeCLI([]string{"create", "Test Topic", "--dir", dir}); code != 1 {
		t.Errorf("duplicate create: expected exit code 1, got %d", code)
	}

	// No title -> 2.
	if code := runKnowledgeCLI([]string{"create", "--dir", dir}); code != 2 {
		t.Errorf("no title: expected exit code 2, got %d", code)
	}
}

func TestRunKnowledgeCreateInterleavedFlags(t *testing.T) {
	dir := t.TempDir()

	// Flags AFTER positional: create "Test Topic" --dir <temp> --content "body".
	if code := runKnowledgeCLI([]string{"create", "Test Topic", "--dir", dir, "--content", "custom body text"}); code != 0 {
		t.Fatalf("interleaved create: expected exit code 0, got %d", code)
	}

	data, err := os.ReadFile(filepath.Join(dir, "test-topic.md"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), "custom body text") {
		t.Errorf("file: expected body content, got:\n%s", string(data))
	}
}

func TestRunKnowledgeList(t *testing.T) {
	dir := t.TempDir()

	// Create two notes.
	if code := runKnowledgeCLI([]string{"create", "Alpha", "--dir", dir}); code != 0 {
		t.Fatalf("create Alpha: expected 0, got %d", code)
	}
	if code := runKnowledgeCLI([]string{"create", "Beta", "--dir", dir}); code != 0 {
		t.Fatalf("create Beta: expected 0, got %d", code)
	}

	// list --dir <temp> -> 0.
	out := captureStdout(t, func() {
		if code := runKnowledgeCLI([]string{"list", "--dir", dir}); code != 0 {
			t.Errorf("list: expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Alpha") || !strings.Contains(out, "Beta") {
		t.Errorf("list output missing note names, got:\n%s", out)
	}
}

func TestRunKnowledgeRead(t *testing.T) {
	dir := t.TempDir()

	// Create a note to read.
	if code := runKnowledgeCLI([]string{"create", "Quantum", "--dir", dir, "--content", "Quantum body text"}); code != 0 {
		t.Fatalf("create: expected 0, got %d", code)
	}

	// read <slug> --dir <temp> -> 0 for existing.
	if code := runKnowledgeCLI([]string{"read", "quantum", "--dir", dir}); code != 0 {
		t.Errorf("read existing: expected exit code 0, got %d", code)
	}

	// read unknown -> 1.
	if code := runKnowledgeCLI([]string{"read", "nonexistent", "--dir", dir}); code != 1 {
		t.Errorf("read unknown: expected exit code 1, got %d", code)
	}

	// read with 2 positionals -> 2.
	if code := runKnowledgeCLI([]string{"read", "quantum", "extra", "--dir", dir}); code != 2 {
		t.Errorf("read 2 positionals: expected exit code 2, got %d", code)
	}
}

func TestRunKnowledgeSearch(t *testing.T) {
	dir := t.TempDir()

	// Create a note containing "quantum".
	if code := runKnowledgeCLI([]string{"create", "Quantum", "--dir", dir, "--content", "quantum mechanics overview"}); code != 0 {
		t.Fatalf("create: expected 0, got %d", code)
	}

	// search "quantum" --dir <temp> -> 0.
	if code := runKnowledgeCLI([]string{"search", "quantum", "--dir", dir}); code != 0 {
		t.Errorf("search: expected exit code 0, got %d", code)
	}

	// No query -> 2.
	if code := runKnowledgeCLI([]string{"search", "--dir", dir}); code != 2 {
		t.Errorf("search no query: expected exit code 2, got %d", code)
	}
}

func TestRunKnowledgeLint(t *testing.T) {
	dir := t.TempDir()

	// lint on empty dir -> 0.
	if code := runKnowledgeCLI([]string{"lint", "--dir", dir}); code != 0 {
		t.Errorf("lint empty: expected exit code 0, got %d", code)
	}

	// Create a note with a dangling [[wikilink]].
	writeNoteFile(t, dir, "dangling-note", "---\ntitle: \"Dangling\"\ncreated: \"2024-01-01\"\nupdated: \"2024-01-01\"\n---\n\nLinks to [[missing-target]].\n")

	// lint with dangling link -> 0 (lint reports issues but does not fail).
	out := captureStdout(t, func() {
		if code := runKnowledgeCLI([]string{"lint", "--dir", dir}); code != 0 {
			t.Errorf("lint with dangling: expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "missing-target") {
		t.Errorf("lint output should mention dangling target, got:\n%s", out)
	}
}

func TestRunKnowledgeLink(t *testing.T) {
	dir := t.TempDir()

	// Create notes A and B.
	if code := runKnowledgeCLI([]string{"create", "A", "--dir", dir, "--content", "A body"}); code != 0 {
		t.Fatalf("create A: expected 0, got %d", code)
	}
	if code := runKnowledgeCLI([]string{"create", "B", "--dir", dir, "--content", "B body"}); code != 0 {
		t.Fatalf("create B: expected 0, got %d", code)
	}

	// link A B --dir <temp> -> 0.
	if code := runKnowledgeCLI([]string{"link", "a", "b", "--dir", dir}); code != 0 {
		t.Fatalf("link a b: expected exit code 0, got %d", code)
	}

	// A body contains [[b|...]].
	data, err := os.ReadFile(filepath.Join(dir, "a.md"))
	if err != nil {
		t.Fatalf("read a.md: %v", err)
	}
	if !strings.Contains(string(data), "[[b|") {
		t.Errorf("a.md: expected [[b|...]] link, got:\n%s", string(data))
	}

	// link A to missing target -> 1.
	if code := runKnowledgeCLI([]string{"link", "a", "missing", "--dir", dir}); code != 1 {
		t.Errorf("link to missing: expected exit code 1, got %d", code)
	}
}
