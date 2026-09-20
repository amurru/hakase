package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/memory"
)

// TestMemoryCLI exercises add/list/forget against a redirected HAKASE_HOME
// (mirrors the channels CLI test).
func TestMemoryCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)

	// add (cwd is a temp dir outside any git repo: the note is stamped with
	// the abs cwd, exactly like the agent's FindRoot fallback).
	added := captureStdout(t, func() {
		if rc := RunMemoryCLI([]string{"add", "--category", "user", "prefers", "terse", "answers"}); rc != 0 {
			t.Fatalf("add rc = %d", rc)
		}
	})
	if !strings.Contains(added, "Saved mem_") {
		t.Fatalf("add output = %q", added)
	}

	store, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	notes := store.Get().Notes
	if len(notes) != 1 {
		t.Fatalf("want 1 note after add, got %d", len(notes))
	}
	if notes[0].Category != "user" || notes[0].Content != "prefers terse answers" {
		t.Fatalf("unexpected note: %+v", notes[0])
	}
	if notes[0].Project == "" {
		t.Fatalf("add should stamp the cwd-derived project root")
	}
	id := notes[0].ID

	// list shows the note with its id.
	listed := captureStdout(t, func() {
		if rc := RunMemoryCLI([]string{"list"}); rc != 0 {
			t.Fatalf("list rc = %d", rc)
		}
	})
	if !strings.Contains(listed, id) || !strings.Contains(listed, "prefers terse answers") {
		t.Fatalf("list output missing note:\n%s", listed)
	}

	// forget removes it; unknown ids fail with rc 1.
	captureStdout(t, func() {
		if rc := RunMemoryCLI([]string{"forget", id}); rc != 0 {
			t.Fatalf("forget rc = %d", rc)
		}
	})
	if rc := RunMemoryCLI([]string{"forget", "mem_missing"}); rc != 1 {
		t.Fatalf("forget unknown rc = %d, want 1", rc)
	}
	if got := store.Get(); len(got.Notes) != 0 {
		t.Fatalf("store not empty after forget: %+v", got.Notes)
	}

	// Usage errors: unknown subcommand and missing args exit 2.
	if rc := RunMemoryCLI([]string{"bogus"}); rc != 2 {
		t.Fatalf("bogus rc = %d, want 2", rc)
	}
	if rc := RunMemoryCLI([]string{"add", "--category", "user"}); rc != 2 {
		t.Fatalf("add without content rc = %d, want 2", rc)
	}
	if _, err := memory.Open(filepath.Join(home, "memory", "notes.json")); err != nil {
		t.Fatalf("reopen: %v", err)
	}
}
