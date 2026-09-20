package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveIsAtomicAndLoadable verifies a Save lands a valid 0600 JSON file
// that Load round-trips (kill mid-save cannot tear thanks to tmp+rename).
func TestSaveIsAtomicAndLoadable(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	sess := NewSession("atomic test")
	sess.AddMessage("user", "hello", "")
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(store.sessionsDir, sess.ID+FileExt)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved file is not valid JSON (torn write): %v", err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "atomic test" || len(loaded.Messages) != 1 {
		t.Fatalf("round-trip = %+v, want title+1 message", loaded)
	}
	// No temp debris visible to List.
	entries, _ := os.ReadDir(store.sessionsDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("tmp debris left behind: %s", e.Name())
		}
	}
}

// TestListUsesIndex verifies listings are served from index.json: after
// saves the index exists, List matches it, and List does not need to
// full-parse transcripts (index deletion forces a rebuild, proving the
// fallback).
func TestListUsesIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		s := NewSession(title)
		s.AddMessage("user", strings.Repeat("x", 5000), "")
		if err := store.Save(s); err != nil {
			t.Fatalf("Save: %v", err)
		}
		ids = append(ids, s.ID)
	}
	idxPath := filepath.Join(dir, indexFileName)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("index.json missing after saves: %v", err)
	}
	var idx sessionIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("index unmarshal: %v", err)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("index entries = %d, want 3", len(idx.Entries))
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("List = %d, want 3", len(summaries))
	}
	// Archive one, List/ListArchived must reflect the index update.
	if err := store.Archive(ids[0]); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	active, _ := store.List()
	arch, _ := store.ListArchived()
	if len(active) != 2 || len(arch) != 1 {
		t.Fatalf("after archive active=%d archived=%d, want 2/1", len(active), len(arch))
	}
	// Delete drops the index entry.
	if err := store.Delete(ids[1]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	active, _ = store.List()
	if len(active) != 1 {
		t.Fatalf("after delete active=%d, want 1", len(active))
	}
	// Removing the index forces a full-scan rebuild on next List.
	if err := os.Remove(idxPath); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	rebuilt, err := store.List()
	if err != nil {
		t.Fatalf("List after index loss: %v", err)
	}
	if len(rebuilt) != 1 {
		t.Fatalf("rebuilt List = %d, want 1", len(rebuilt))
	}
	if _, err := os.Stat(idxPath); err != nil {
		t.Fatalf("index not rebuilt: %v", err)
	}
}

// TestLoadQuarantinesCorrupt verifies a torn file is preserved aside and
// reported, and List skips it instead of wedging.
func TestLoadQuarantinesCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	s := NewSession("good")
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	badID := "sess_torn_123"
	badPath := filepath.Join(dir, badID+FileExt)
	if err := os.WriteFile(badPath, []byte(`{"id": "truncated...`), 0600); err != nil {
		t.Fatalf("write torn: %v", err)
	}
	if _, err := store.Load(badID); err == nil {
		t.Fatal("expected corruption error for torn file")
	} else if !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("error should mention quarantine, got: %v", err)
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatal("torn file should be moved aside")
	}
	// A .corrupt-* sidecar must exist for forensics.
	found := false
	for _, e := range mustReadDir(t, dir) {
		if strings.HasPrefix(e.Name(), badID+FileExt+".corrupt-") {
			found = true
		}
	}
	if !found {
		t.Fatal("quarantined sidecar missing")
	}
	// List still serves the good session.
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != s.ID {
		t.Fatalf("List = %+v, want only good session", summaries)
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	return entries
}
