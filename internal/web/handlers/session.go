package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/session"
	"github.com/go-chi/chi/v5"
)

// SessionSummaryDTO is the API response for a session in the list endpoint.
type SessionSummaryDTO struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// SessionDetailDTO is the API response for a single session with messages.
type SessionDetailDTO struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Archived  bool         `json:"archived"`
	Messages  []MessageDTO `json:"messages"`
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
			UpdatedAt:    s.UpdatedAt,
			MessageCount: s.MessageCount,
		})
	}

	writeJSON(w, http.StatusOK, dtos)
}

// CreateSession handles POST /sessions - creates a new session.
// Accepts JSON body: {"title": "My new session"}
func (api *SessionAPI) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Untitled session"
	}

	api.mu.Lock()
	defer api.mu.Unlock()

	sess, err := api.svc.CreateSession(title)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, SessionDetailDTO{
		ID:        sess.ID,
		Title:     sess.Title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Archived:  sess.Archived,
		Messages:  messagesToDTO(sess.Messages),
	})
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

	writeJSON(w, http.StatusOK, SessionDetailDTO{
		ID:        sess.ID,
		Title:     sess.Title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Archived:  sess.Archived,
		Messages:  messagesToDTO(sess.Messages),
	})
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

	writeJSON(w, http.StatusOK, SessionDetailDTO{
		ID:        sess.ID,
		Title:     sess.Title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Archived:  sess.Archived,
		Messages:  messagesToDTO(sess.Messages),
	})
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
