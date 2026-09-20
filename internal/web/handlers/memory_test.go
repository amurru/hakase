package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"amurru/hakase/internal/memory"

	"github.com/go-chi/chi/v5"
)

func newTestMemoryAPI(t *testing.T) (*memory.Store, chi.Router) {
	t.Helper()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory", "notes.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		RegisterMemoryRoutes(r, store)
	})
	return store, r
}

func TestMemoryListAndDelete(t *testing.T) {
	store, r := newTestMemoryAPI(t)
	now := time.Now().UTC()
	if err := store.Update(func(st *memory.State) error {
		st.Notes = []memory.Note{
			{ID: "mem_aaaabbbbccccdddd", Category: "user", Content: "prefers terse answers", Project: "", CreatedAt: now, UpdatedAt: now},
			{ID: "mem_egh", Category: "project", Content: "uses pnpm", Project: "/repo", CreatedAt: now, UpdatedAt: now},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// List returns every note across projects.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/memory", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var list memoryListDTO
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Notes) != 2 {
		t.Fatalf("want 2 notes, got %d", len(list.Notes))
	}
	if list.Notes[0].ID != "mem_aaaabbbbccccdddd" || list.Notes[0].Category != "user" {
		t.Fatalf("unexpected first note: %+v", list.Notes[0])
	}
	if list.StorePath == "" {
		t.Fatalf("store path missing from response")
	}

	// Delete an existing note.
	del := httptest.NewRecorder()
	r.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/api/memory/mem_aaaabbbbccccdddd", nil))
	if del.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", del.Code, del.Body.String())
	}
	if got := store.Get(); len(got.Notes) != 1 {
		t.Fatalf("want 1 note after delete, got %d", len(got.Notes))
	}

	// Deleting an unknown id is a 404.
	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/api/memory/mem_missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d, want 404", missing.Code)
	}
}
