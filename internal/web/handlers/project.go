package handlers

import (
	"amurru/hakase/internal/registry"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ProjectDTO is the API response for one registered project.
type ProjectDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SourceURL string    `json:"source_url"`
	Ref       string    `json:"ref,omitempty"`
	Checkout  string    `json:"checkout,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Error carries the bounded stderr of a failed register/sync so the UI can
	// explain a sync_error state. Never persisted - response only.
	Error string `json:"error,omitempty"`
}

// ProjectRouter is the minimum interface needed by RegisterProjectRoutes.
type ProjectRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
}

// RegisterProjectRoutes registers the project registry API routes on the given
// router. Routes are relative to /api (the caller places them inside the /api
// group). The endpoints operate on registry.Current, the boot-configured
// project service (docs/git-tools/project-registry.md DP-6..DP-10).
func RegisterProjectRoutes(r ProjectRouter) {
	r.Get("/projects", projectList)
	r.Post("/projects", projectRegister)
	r.Delete("/projects/{id}", projectDelete)
	r.Post("/projects/{id}/sync", projectSync)
}

// registryService returns the boot-configured project service or writes a 503.
func registryService(w http.ResponseWriter) *registry.Service {
	if registry.Current == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "project registry is not available on this server"})
		return nil
	}
	return registry.Current
}

func projectDTO(p registry.Project, runErr error) ProjectDTO {
	dto := ProjectDTO{
		ID:        p.ID,
		Name:      p.Name,
		SourceURL: p.SourceURL,
		Ref:       p.Ref,
		Checkout:  p.Checkout,
		Status:    p.Status,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	if runErr != nil {
		dto.Error = runErr.Error()
	}
	return dto
}

// projectList handles GET /api/projects.
func projectList(w http.ResponseWriter, r *http.Request) {
	svc := registryService(w)
	if svc == nil {
		return
	}
	projects := svc.Store().List()
	dtos := make([]ProjectDTO, 0, len(projects))
	for _, p := range projects {
		dtos = append(dtos, projectDTO(p, nil))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// projectRegister handles POST /api/projects - registers and materializes a
// project synchronously (DP-6). Body: {"name","url","ref"?}. A clone failure
// leaves the entry in sync_error and is returned as a 201 entry with an error
// field so the UI can show both the entry and why it is not ready.
func projectRegister(w http.ResponseWriter, r *http.Request) {
	svc := registryService(w)
	if svc == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Ref  string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(req.Name)
	url := strings.TrimSpace(req.URL)
	if !registry.ValidName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid project name %q", name)})
		return
	}
	if !registry.ValidSourceURL(url) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported source URL %q (allowed: https://, http://, git://, ssh://, or file:// for a local bare remote)", url)})
		return
	}

	p, err := svc.Register(r.Context(), name, url, strings.TrimSpace(req.Ref))
	if err != nil {
		if p.ID == "" {
			// Entry was never created: duplicate name or another store error.
			if strings.Contains(err.Error(), "already exists") {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Entry created but materialization failed (sync_error): return it so
		// the UI can retry via sync or delete.
		writeJSON(w, http.StatusCreated, projectDTO(p, err))
		return
	}
	writeJSON(w, http.StatusCreated, projectDTO(p, nil))
}

// projectID extracts the {id} URL parameter from the request.
func projectID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// projectSync handles POST /api/projects/{id}/sync - fast-forwards the
// checkout from its remote (DP-9). A failed pull leaves the entry in
// sync_error and is returned with an error field.
func projectSync(w http.ResponseWriter, r *http.Request) {
	svc := registryService(w)
	if svc == nil {
		return
	}
	id := projectID(r)
	p, err := svc.Sync(r.Context(), id)
	if err != nil {
		if p.ID == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("project %q not found", id)})
			return
		}
		writeJSON(w, http.StatusOK, projectDTO(p, err))
		return
	}
	writeJSON(w, http.StatusOK, projectDTO(p, nil))
}

// projectDelete handles DELETE /api/projects/{id} - unregisters and removes
// the local checkout (DP-10). The remote is never touched.
func projectDelete(w http.ResponseWriter, r *http.Request) {
	svc := registryService(w)
	if svc == nil {
		return
	}
	id := projectID(r)
	if _, err := svc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("project %q not found", id)})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
