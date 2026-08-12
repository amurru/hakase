package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSessionDirPermissions verifies the sessions directory is created with
// 0700 permissions (user read/write/execute only).
func TestSessionDirPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := NewSessionStore(dir); err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat sessions dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("sessions dir mode = %o, want 0700", got)
	}
}

// TestSessionFilePermissions verifies newly created session files are written
// with 0600 permissions (user read/write only).
func TestSessionFilePermissions(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	sess := NewSession("perm test")
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(store.sessionsDir, sess.ID+FileExt)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("session file mode = %o, want 0600", got)
	}
}

// TestSessionMigrationChmodsExistingFiles verifies that store initialization
// migrates session files written before the 0600/0700 hardening: an existing
// 0644 session file is chmod'd to 0600, and a 0755 sessions dir is tightened
// to 0700.
func TestSessionMigrationChmodsExistingFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Pre-create a session file with the old 0644 mode.
	legacyID := "sess_legacy_0644"
	path := filepath.Join(dir, legacyID+FileExt)
	if err := os.WriteFile(path, []byte(`{"id":"sess_legacy_0644"}`), 0644); err != nil {
		t.Fatalf("WriteFile legacy session: %v", err)
	}

	// A 0600 file must be left untouched.
	okID := "sess_ok_0600"
	okPath := filepath.Join(dir, okID+FileExt)
	if err := os.WriteFile(okPath, []byte(`{"id":"sess_ok_0600"}`), 0600); err != nil {
		t.Fatalf("WriteFile ok session: %v", err)
	}

	// Store init runs the migration.
	if _, err := NewSessionStore(dir); err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat legacy session file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("legacy session file mode = %o, want 0600", got)
	}

	info, err = os.Stat(okPath)
	if err != nil {
		t.Fatalf("stat ok session file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("already-0600 session file mode = %o, want 0600 unchanged", got)
	}

	info, err = os.Stat(dir)
	if err != nil {
		t.Fatalf("stat sessions dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("sessions dir mode = %o, want 0700", got)
	}
}
