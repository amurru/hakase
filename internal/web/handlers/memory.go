// memory.go exposes agent-written auto-memory over the web API so the UI can
// inspect and prune the notes the agent saves across sessions. It reads and
// writes only the notes file (internal/memory); the agent runtime reads it
// independently at session start, and the cross-process flock keeps writes
// safe while the server runs.
package handlers

import (
	"net/http"
	"time"

	"amurru/hakase/internal/memory"

	"github.com/go-chi/chi/v5"
)

// MemoryRouter is the minimum interface needed by RegisterMemoryRoutes.
type MemoryRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
}

// MemoryAPI serves the auto-memory inspection/pruning endpoints.
type MemoryAPI struct {
	store *memory.Store
}

// RegisterMemoryRoutes registers memory routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterMemoryRoutes(r MemoryRouter, store *memory.Store) {
	api := &MemoryAPI{store: store}
	r.Get("/memory", api.List)
	r.Delete("/memory/{id}", api.Delete)
}

// MemoryDTO mirrors one memory note for the API.
type MemoryDTO struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// memoryListDTO is the GET /api/memory response.
type memoryListDTO struct {
	Notes     []MemoryDTO `json:"notes"`
	StorePath string      `json:"store_path"`
}

// List handles GET /api/memory - every note across projects (the panel shows
// the project column so the operator sees global vs project scoping).
func (api *MemoryAPI) List(w http.ResponseWriter, r *http.Request) {
	st := api.store.Get()
	dto := memoryListDTO{
		Notes:     make([]MemoryDTO, 0, len(st.Notes)),
		StorePath: api.store.Path(),
	}
	for _, n := range st.Notes {
		dto.Notes = append(dto.Notes, MemoryDTO{
			ID:        n.ID,
			Category:  n.Category,
			Content:   n.Content,
			Project:   n.Project,
			CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

// Delete handles DELETE /api/memory/{id}.
func (api *MemoryAPI) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	removed, err := api.store.Remove(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no note with id " + id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"forgotten": true})
}
