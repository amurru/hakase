package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionStoreRejectsPathSemanticsIDs pins the store-boundary ID
// validation: IDs arrive from request path values, so anything carrying path
// semantics (separators, empty, oversized) must be refused before it is ever
// joined into the sessions directory.
func TestSessionStoreRejectsPathSemanticsIDs(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	bad := []string{
		"",
		"../escape",
		"..",
		"sub/dir",
		`back\slash`,
		"id with spaces",
		"id%2Fescape",
		"task_" + strings.Repeat("x", 200),
	}
	for _, id := range bad {
		if _, err := store.Load(id); err == nil || !strings.Contains(err.Error(), "invalid session id") {
			t.Errorf("Load(%q): expected invalid-session-id error, got %v", id, err)
		}
		if err := store.Delete(id); err == nil || !strings.Contains(err.Error(), "invalid session id") {
			t.Errorf("Delete(%q): expected invalid-session-id error, got %v", id, err)
		}
		sess := NewSession("bad id")
		sess.ID = id
		if err := store.Save(sess); err == nil || !strings.Contains(err.Error(), "invalid session id") {
			t.Errorf("Save(id=%q): expected invalid-session-id error, got %v", id, err)
		}
	}

	// The generated form (task_<uuid>) and plain test-style ids stay valid.
	sess := NewSession("valid")
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save(valid): %v", err)
	}
	if _, err := store.Load(sess.ID); err != nil {
		t.Fatalf("Load(valid): %v", err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete(valid): %v", err)
	}
}
