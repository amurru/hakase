package handlers

import (
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/session"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// SessionSummaryDTO is the API response for a session in the list endpoint.
type SessionSummaryDTO struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ProjectID    string    `json:"project_id,omitempty"`
	ProjectName  string    `json:"project_name,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// SessionDetailDTO is the API response for a single session with messages.
type SessionDetailDTO struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	ProjectID   string       `json:"project_id,omitempty"`
	ProjectName string       `json:"project_name,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Archived    bool         `json:"archived"`
	Messages    []MessageDTO `json:"messages"`
}

// MessageDTO is the API response for a single chat message.
type MessageDTO struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Thinking  string    `json:"thinking,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Tokens    int       `json:"tokens,omitempty"`
	Sequence  int64     `json:"sequence,omitempty"`
	Kind      string    `json:"kind,omitempty"`
}

// SessionAPI wraps the SessionService with concurrency-safe access
// for the web API layer.
type SessionAPI struct {
	svc *session.SessionService
	mu  sync.RWMutex
}

// SessionsRouter is the minimum interface needed by RegisterSessionRoutes.
type SessionsRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
}

// RegisterSessionRoutes registers all session API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterSessionRoutes(r SessionsRouter, svc *session.SessionService) {
	api := &SessionAPI{svc: svc}

	r.Get("/sessions", api.ListSessions)
	r.Post("/sessions", api.CreateSession)
	r.Get("/sessions/active", api.GetActiveSession)
	r.Get("/sessions/{id}", api.GetSession)
	r.Delete("/sessions/{id}", api.DeleteSession)
	r.Post("/sessions/{id}/archive", api.ArchiveSession)
	r.Post("/sessions/{id}/activate", api.ActivateSession)
}

// sessionID extracts the {id} URL parameter from the request.
func sessionID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// ListSessions handles GET /sessions - returns all non-archived sessions.
func (api *SessionAPI) ListSessions(w http.ResponseWriter, r *http.Request) {
	api.mu.RLock()
	defer api.mu.RUnlock()

	summaries, err := api.svc.ListSessions()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dtos := make([]SessionSummaryDTO, 0, len(summaries))
	for _, s := range summaries {
		dtos = append(dtos, SessionSummaryDTO{
			ID:           s.ID,
			Title:        s.Title,
			ProjectID:    s.ProjectID,
			ProjectName:  s.ProjectName,
			UpdatedAt:    s.UpdatedAt,
			MessageCount: s.MessageCount,
		})
	}

	writeJSON(w, http.StatusOK, dtos)
}

// CreateSession handles POST /sessions - creates a new session.
// Accepts JSON body: {"title": "My new session", "project_id": "proj_..."}
// project_id (optional) binds the session to a ready registered project.
func (api *SessionAPI) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title     string `json:"title"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Untitled session"
	}
	projectID := strings.TrimSpace(req.ProjectID)

	// Validate the project binding BEFORE creating anything: an unknown or
	// unready project must fail without persisting/activating an orphan
	// session (the registry store is concurrency-safe, so this needs no lock).
	var bind *registry.Project
	if projectID != "" {
		if registry.Current == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "project registry is not available on this server"})
			return
		}
		p, err := registry.Current.Store().Get(projectID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown project %q", projectID)})
			return
		}
		if p.Status != registry.StatusReady {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("project %q is not ready (status %q); sync it first", p.Name, p.Status)})
			return
		}
		bind = &p
	}

	api.mu.Lock()
	defer api.mu.Unlock()

	sess, err := api.svc.CreateSession(title)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if bind != nil {
		if err := api.svc.BindProject(sess.ID, bind.ID, bind.Name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		sess.ProjectID = bind.ID
		sess.ProjectName = bind.Name
	}

	writeJSON(w, http.StatusCreated, sessionDetailDTO(sess))
}

// GetSession handles GET /sessions/{id} - returns session detail with messages.
func (api *SessionAPI) GetSession(w http.ResponseWriter, r *http.Request) {
	id := sessionID(r)

	api.mu.RLock()
	defer api.mu.RUnlock()

	// GetMessages loads the full session from the store.
	msgs, err := api.svc.GetMessages(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if msgs == nil {
		// GetMessages returns (nil, nil) when id is empty, but we have an id here.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	// Load the session directly to get metadata (title, timestamps, etc.).
	store := api.svc.Store()
	sess, err := store.Load(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, sessionDetailDTO(sess))
}

// DeleteSession handles DELETE /sessions/{id} - removes a session.
func (api *SessionAPI) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id := sessionID(r)

	api.mu.Lock()
	defer api.mu.Unlock()

	if err := api.svc.DeleteSession(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ArchiveSession handles POST /sessions/{id}/archive - archives a session.
func (api *SessionAPI) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	id := sessionID(r)

	api.mu.Lock()
	defer api.mu.Unlock()

	if err := api.svc.ArchiveSession(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ActivateSession handles POST /sessions/{id}/activate - sets a session as active.
func (api *SessionAPI) ActivateSession(w http.ResponseWriter, r *http.Request) {
	id := sessionID(r)

	api.mu.Lock()
	defer api.mu.Unlock()

	if err := api.svc.SetActiveSession(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetActiveSession handles GET /sessions/active - returns the active session.
func (api *SessionAPI) GetActiveSession(w http.ResponseWriter, r *http.Request) {
	api.mu.RLock()
	defer api.mu.RUnlock()

	sess, err := api.svc.GetActiveSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if sess == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"active": false})
		return
	}

	writeJSON(w, http.StatusOK, sessionDetailDTO(sess))
}

// messagesToDTO converts internal Message slice to DTO slice.
func messagesToDTO(msgs []session.Message) []MessageDTO {
	dtos := make([]MessageDTO, 0, len(msgs))
	for _, m := range msgs {
		dtos = append(dtos, MessageDTO{
			Role:      m.Role,
			Content:   m.Content,
			Thinking:  m.Thinking,
			Timestamp: m.Timestamp,
			Tokens:    m.Tokens,
			Sequence:  m.Sequence,
			Kind:      m.Kind,
		})
	}
	return dtos
}

// sessionDetailDTO maps a session to its API response shape.
func sessionDetailDTO(s *session.Session) SessionDetailDTO {
	return SessionDetailDTO{
		ID:          s.ID,
		Title:       s.Title,
		ProjectID:   s.ProjectID,
		ProjectName: s.ProjectName,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
		Archived:    s.Archived,
		Messages:    messagesToDTO(s.Messages),
	}
}
