package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("fresh store lists %d projects, want 0", len(got))
	}

	p, err := s.Create("hakase-web", "https://github.com/amurru/hakase.git", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Status != StatusCloning {
		t.Errorf("status = %q, want cloning", p.Status)
	}
	if p.ID == "" || p.Name != "hakase-web" {
		t.Errorf("created project = %+v", p)
	}

	// Name uniqueness is enforced case-insensitively.
	if _, err := s.Create("Hakase-Web", "https://example.com/other.git", ""); err == nil {
		t.Error("duplicate name accepted")
	}
	// Validation rejects bad names and unsupported sources. file:// is allowed:
	// it is a local bare remote for CLI operation (the D9 sandbox gate applies
	// at materialization, not at entry creation).
	if _, err := s.Create("../evil", "https://example.com/x.git", ""); err == nil {
		t.Error("invalid name accepted")
	}
	if !ValidSourceURL("file:///tmp/seed") {
		t.Error("file:// source rejected for a registered project")
	}
	if ValidSourceURL("/tmp/seed") {
		t.Error("scheme-less local path accepted for a registered project")
	}
	if ValidSourceURL("ftp://example.com/seed.git") {
		t.Error("non-git scheme accepted for a registered project")
	}
	if ValidSourceURL("http://example.com/seed.git") {
		t.Error("plain-http source accepted for a registered project")
	}

	// Update transitions status and keeps CreatedAt stable.
	p.Status = StatusReady
	p.Checkout = filepath.Join(t.TempDir(), "proj")
	if err := s.Update(p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusReady || got.Checkout != p.Checkout {
		t.Errorf("updated project = %+v", got)
	}
	if !got.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("CreatedAt changed across Update: %v -> %v", p.CreatedAt, got.CreatedAt)
	}

	// Reload from disk: persistence round-trip.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}
	if err := s2.Update(got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s2.Get(p.ID)
	if err != nil {
		t.Fatalf("reload Get: %v", err)
	}
	if reloaded.Name != "hakase-web" || reloaded.Status != StatusReady {
		t.Errorf("reloaded project = %+v", reloaded)
	}

	// Delete removes; double delete errors.
	if err := s2.Delete(p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s2.Delete(p.ID); err == nil {
		t.Error("second delete did not error")
	}
	if got := s2.List(); len(got) != 0 {
		t.Errorf("list after delete = %+v, want empty", got)
	}
}

func TestStoreMultipleSortedByName(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if _, err := s.Create(n, "https://example.com/"+n+".git", ""); err != nil {
			t.Fatal(err)
		}
	}
	list := s.List()
	if len(list) != 3 || list[0].Name != "alpha" || list[1].Name != "mid" || list[2].Name != "zeta" {
		t.Errorf("list not sorted by name: %+v", list)
	}
}

func TestDefaultPathHonorsHakaseHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	if got := DefaultPath(); got != filepath.Join(home, "projects.json") {
		t.Errorf("DefaultPath = %q, want %q", got, filepath.Join(home, "projects.json"))
	}
}

func TestStoreEmptyAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")

	// Empty file loads as an empty registry.
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := NewStore(path); err != nil || len(s.List()) != 0 {
		t.Errorf("empty file: store=%v err=%v", s, err)
	}

	// Corrupt file is an error, not a silent wipe.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Error("corrupt registry file loaded without error")
	}
}
