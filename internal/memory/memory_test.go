package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/util"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory", "notes.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestAddRoundTrip(t *testing.T) {
	s := openTestStore(t)
	note, err := s.Add("user", "prefers terse answers", "/repo", 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(note.ID, "mem_") || len(note.ID) != len("mem_")+16 {
		t.Fatalf("unexpected id shape: %q", note.ID)
	}
	got := s.Get()
	if len(got.Notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(got.Notes))
	}
	n := got.Notes[0]
	if n.Category != "user" || n.Content != "prefers terse answers" || n.Project != "/repo" {
		t.Fatalf("round-trip mismatch: %+v", n)
	}
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", n)
	}
	if got.Version != Version {
		t.Fatalf("version = %d, want %d", got.Version, Version)
	}
}

func TestSaveFileAndDirPerms(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Add("lesson", "gofmt before commit", "", 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file perms = %o, want 600", fi.Mode().Perm())
	}
	dirFi, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perms = %o, want 700", dirFi.Mode().Perm())
	}
	for _, leftover := range []string{s.Path() + ".tmp"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Fatalf("leftover temp file %s", leftover)
		}
	}
}

func TestCrossStoreReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.json")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	if _, err := a.Add("project", "tests need pnpm not npm", "", 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// b holds a stale cache (both opened before the write); Get must reload.
	if got := b.Get(); len(got.Notes) != 1 {
		t.Fatalf("second store sees %d notes, want 1", len(got.Notes))
	}
}

func TestCorruptFileQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on corrupt file: %v", err)
	}
	if got := s.Get(); len(got.Notes) != 0 {
		t.Fatalf("corrupt file should start empty, got %d notes", len(got.Notes))
	}
	matches, _ := filepath.Glob(path + ".corrupt-*.json")
	if len(matches) != 1 {
		t.Fatalf("want exactly one quarantined sidecar, got %v", matches)
	}
	// The store must recover: a subsequent write rebuilds the file.
	if _, err := s.Add("user", "recovers after quarantine", "", 0); err != nil {
		t.Fatalf("Add after quarantine: %v", err)
	}
	if got := s.Get(); len(got.Notes) != 1 {
		t.Fatalf("store did not recover, got %d notes", len(got.Notes))
	}
}

func TestAddDedupesExactMatch(t *testing.T) {
	s := openTestStore(t)
	first, err := s.Add("feedback", "run gofmt before committing", "/repo", 0)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // UpdatedAt must move for the dedupe bump
	second, err := s.Add("feedback", "  run gofmt before committing  ", "/repo", 0)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedupe failed: %s vs %s", first.ID, second.ID)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("dedupe did not bump UpdatedAt")
	}
	if got := s.Get(); len(got.Notes) != 1 {
		t.Fatalf("want 1 deduped note, got %d", len(got.Notes))
	}
}

func TestAddSameContentDifferentScopeNotDeduped(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Add("user", "prefers terse answers", "/repo", 0); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if _, err := s.Add("user", "prefers terse answers", "", 0); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if got := s.Get(); len(got.Notes) != 2 {
		t.Fatalf("global and project copies should coexist, got %d", len(got.Notes))
	}
}

func TestAddValidation(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Add("vibes", "x", "", 0); err == nil || !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("want unknown-category error, got %v", err)
	} else if !strings.Contains(err.Error(), "user, feedback, project, lesson") {
		t.Fatalf("error should list categories, got %v", err)
	}
	if _, err := s.Add("user", "   ", "", 0); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty-content error, got %v", err)
	}
	if _, err := s.Add("user", strings.Repeat("x", MaxNoteChars+1), "", 0); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want over-cap error, got %v", err)
	}
}

func TestStoreFullRefusesWithoutEviction(t *testing.T) {
	s := openTestStore(t)
	for i := 0; i < 2; i++ {
		if _, err := s.Add("lesson", strings.Repeat("n", 10)+string(rune('a'+i)), "", 2); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	_, err := s.Add("lesson", "third", "", 2)
	if err == nil || !strings.Contains(err.Error(), "memory is full") || !strings.Contains(err.Error(), "forget_memory") {
		t.Fatalf("want full-store error naming the remedy, got %v", err)
	}
	if got := s.Get(); len(got.Notes) != 2 {
		t.Fatalf("full store must not evict, got %d", len(got.Notes))
	}
}

func TestRemoveAndTouch(t *testing.T) {
	s := openTestStore(t)
	note, err := s.Add("project", "uses make build", "/repo", 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	ok, err := s.Remove("mem_doesnotexist")
	if err != nil || ok {
		t.Fatalf("unknown Remove should be (false, nil), got (%v, %v)", ok, err)
	}
	time.Sleep(2 * time.Millisecond)
	updated, err := s.Touch(note.ID, "lesson", "uses make build-frontend first")
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if updated.Category != "lesson" || updated.Content != "uses make build-frontend first" {
		t.Fatalf("Touch did not apply: %+v", updated)
	}
	if !updated.UpdatedAt.After(note.UpdatedAt) {
		t.Fatalf("Touch did not bump UpdatedAt")
	}
	if ok, err := s.Remove(note.ID); err != nil || !ok {
		t.Fatalf("Remove existing: (%v, %v)", ok, err)
	}
	if got := s.Get(); len(got.Notes) != 0 {
		t.Fatalf("want empty after remove, got %d", len(got.Notes))
	}
	if _, err := s.Touch("mem_missing", "user", "x"); err == nil || !strings.Contains(err.Error(), "no note with id") {
		t.Fatalf("Touch unknown id should error, got %v", err)
	}
}

func TestSelectForProject(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	seed := []Note{
		{ID: "mem_g1", Category: "user", Content: "global pref", Project: "", CreatedAt: now, UpdatedAt: now.Add(500 * time.Millisecond)},
		{ID: "mem_a2", Category: "user", Content: "newer project pref", Project: "/a", CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "mem_a1", Category: "user", Content: "older project pref", Project: "/a", CreatedAt: now, UpdatedAt: now},
		{ID: "mem_b1", Category: "user", Content: "other project", Project: "/b", CreatedAt: now, UpdatedAt: now},
		{ID: "mem_l1", Category: "lesson", Content: "lesson for /a", Project: "/a", CreatedAt: now, UpdatedAt: now},
	}
	if err := s.Update(func(st *State) error { st.Notes = seed; return nil }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := SelectForProject(s.Get(), "/a")
	var ids []string
	for _, n := range got {
		ids = append(ids, n.ID)
	}
	want := []string{"mem_a2", "mem_g1", "mem_a1", "mem_l1"} // user group newest-first, then lesson; /b hidden
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("selection/order mismatch:\n got %v\nwant %v", ids, want)
	}
}

func TestRenderBlockEmpty(t *testing.T) {
	if got := RenderBlock(nil, 4000); got != "" {
		t.Fatalf("empty notes should render empty, got %q", got)
	}
}

func TestRenderBlockContents(t *testing.T) {
	now := time.Now().UTC()
	notes := []Note{
		{ID: "mem_u1", Category: "user", Content: "prefers terse answers", UpdatedAt: now},
		{ID: "mem_f1", Category: "feedback", Content: "multi\nline folds", UpdatedAt: now},
	}
	block := RenderBlock(notes, 4000)
	for _, want := range []string{"AUTO MEMORY", "[user]", "[feedback]", "(mem_u1) prefers terse answers", "(mem_f1) multi line folds", "remember{", "forget_memory"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	// Categories appear in enum order.
	if strings.Index(block, "[user]") > strings.Index(block, "[feedback]") {
		t.Fatalf("category groups out of order:\n%s", block)
	}
}

func TestRenderBlockBudgetCap(t *testing.T) {
	now := time.Now().UTC()
	var notes []Note
	for i := 0; i < 20; i++ {
		notes = append(notes, Note{ID: NewID(), Category: "lesson", Content: strings.Repeat("x", 80), UpdatedAt: now})
	}
	const budget = 500
	block := RenderBlock(notes, budget)
	// The truncation notice may nudge the block slightly over the cap by
	// design (the model must see it); the body must respect the budget.
	if len(block) > budget+150 {
		t.Fatalf("block over budget: %d > %d\n%s", len(block), budget, block)
	}
	if !strings.Contains(block, "omitted") {
		t.Fatalf("capped block should carry a truncation notice:\n%s", block)
	}
	if strings.Count(block, "(mem_") != strings.Count(block, "\n- ") {
		t.Fatalf("rendered lines and ids disagree")
	}
	// Generous budget renders everything, no notice.
	full := RenderBlock(notes, 1<<20)
	if strings.Contains(full, "omitted") {
		t.Fatalf("uncapped block should not truncate")
	}
}

func TestUpdateWaitsForCrossProcessFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.json")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	if _, err := a.Add("user", "seed note", "", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Hold the flock the way another process would (same semantics: flock is
	// per open file description, so this second open conflicts).
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lf.Close()
	if err := util.FlockExclusive(lf); err != nil {
		t.Fatalf("flock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := b.Add("user", "must wait for the lock", "", 0)
		done <- err
	}()
	// The load-mutate-save transaction must be serialized behind the held
	// flock, not just the final rename (the pre-fix code sailed straight
	// through here).
	select {
	case err := <-done:
		t.Fatalf("Update completed while another holder owned the flock (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := util.FlockUnlock(lf); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Add after unlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Update did not complete after the flock was released")
	}
	got := a.Get()
	if len(got.Notes) != 2 {
		t.Fatalf("want both notes after serialized writes, got %d", len(got.Notes))
	}
}

func TestSaveTightensExistingPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.json")
	// A pre-existing permissive store dir and tmp file (e.g. from an old
	// install or a manual copy) must be tightened by the next write.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Add("lesson", "perm tightening", "", 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	dirFi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perms = %o, want 700", dirFi.Mode().Perm())
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The renamed store inherits the tmp file's mode; without the explicit
	// re-chmod it would still be 644.
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("store perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestOpenDefaultFailsWithoutHome(t *testing.T) {
	t.Setenv("HAKASE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	s, err := OpenDefault()
	if err == nil {
		t.Fatal("OpenDefault must fail closed when no home directory resolves")
	}
	if !strings.Contains(err.Error(), "no home directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil store on failure")
	}
}

func TestGetReloadWaitsForCrossProcessFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Add("user", "seed note", "", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Another "process" holds the flock and swaps the file underneath us
	// (its in-progress transaction wrote a new store).
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lf.Close()
	if err := util.FlockExclusive(lf); err != nil {
		t.Fatalf("flock: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // make sure the mtime moves past s.mtime
	if err := os.WriteFile(path, []byte(`{"version":1,"notes":[{"id":"mem_ext1","category":"user","content":"written by the other process","created_at":"2026-09-20T00:00:00Z","updated_at":"2026-09-20T00:00:00Z"}]}`), 0o600); err != nil {
		t.Fatalf("external write: %v", err)
	}

	done := make(chan State, 1)
	go func() { done <- s.Get() }()
	// Get must detect the mtime/size change and block on the flock through
	// its reload (an unlocked reload could quarantine a valid file that a
	// concurrent Update just installed).
	select {
	case st := <-done:
		t.Fatalf("Get reload ran while another holder owned the flock (%d notes)", len(st.Notes))
	case <-time.After(150 * time.Millisecond):
	}
	if err := util.FlockUnlock(lf); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	select {
	case st := <-done:
		if len(st.Notes) != 1 || st.Notes[0].Content != "written by the other process" {
			t.Fatalf("Get did not see the externally written note: %+v", st.Notes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not complete after the flock was released")
	}
}
