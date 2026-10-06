// clarify.go implements the web-based clarify gate that bridges the agent's
// clarify channel to HTTP endpoints, allowing the web UI to answer mid-task
// questions with choices or free text.
package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// WebClarifyGate implements interfaces.ClarifyGate for the web UI.
// It emits SSE clarify prompts and waits for HTTP responses on a channel
// keyed by clarify ID.
type WebClarifyGate struct {
	mu        sync.RWMutex
	bridge    *sse.EventBridge
	sessionID string
	cfg       interfaces.ClarifyConfig
	pending   map[string]chan interfaces.ClarifyResponse // clarifyID -> response channel
	// resurrected maps re-emitted prompt IDs (durable-resume Phase 7)
	// to pause IDs. Live prompts resolve through pending; prompts
	// re-emitted after a restart have no blocked handler and resolve
	// through the resume backend instead.
	resurrected map[string]string // promptID -> pauseID
	resume      ResumeBackend     // nil when durable resume is unwired
}

// TrackResurrected records that promptID re-emits the gate prompt for
// pauseID after a restart.
func (g *WebClarifyGate) TrackResurrected(promptID, pauseID string) {
	g.mu.Lock()
	if g.resurrected == nil {
		g.resurrected = make(map[string]string)
	}
	g.resurrected[promptID] = pauseID
	g.mu.Unlock()
}

// ResurrectedPause returns the pause ID a re-emitted prompt answers.
func (g *WebClarifyGate) ResurrectedPause(promptID string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	id, ok := g.resurrected[promptID]
	return id, ok
}

// DropResurrected forgets a re-emitted prompt (answered, expired, gone).
func (g *WebClarifyGate) DropResurrected(promptID string) {
	g.mu.Lock()
	delete(g.resurrected, promptID)
	g.mu.Unlock()
}

// SetResumeBackend wires the durable-resume answer path (nil disables).
func (g *WebClarifyGate) SetResumeBackend(b ResumeBackend) {
	g.mu.Lock()
	g.resume = b
	g.mu.Unlock()
}

// ResumeBackend returns the wired resume path, or nil.
func (g *WebClarifyGate) ResumeBackend() ResumeBackend {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.resume
}

// NewWebClarifyGate creates a new web-based clarify gate.
func NewWebClarifyGate(bridge *sse.EventBridge, sessionID string, cfg interfaces.ClarifyConfig) *WebClarifyGate {
	return &WebClarifyGate{
		bridge:    bridge,
		sessionID: sessionID,
		cfg:       cfg,
		pending:   make(map[string]chan interfaces.ClarifyResponse),
	}
}

// promptSession resolves the session a prompt should advertise: the asking
// request's session when known, the gate's fixed session otherwise.
func (g *WebClarifyGate) promptSession(reqSession string) string {
	if reqSession != "" {
		return reqSession
	}
	return g.sessionID
}

// AskClarify blocks until the user answers, the context is canceled
// (e.g. /stop), or the expiry deadline is reached. Emits an SSE clarify
// prompt, registers a response channel, and waits. On timeout, emits
// clarify_timeout SSE event and returns ClarifyResponse{TimedOut: true}.
func (g *WebClarifyGate) AskClarify(ctx context.Context, req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
	clarifyID := "clar_" + uuid.New().String()
	resp := make(chan interfaces.ClarifyResponse, 1)

	g.mu.Lock()
	g.pending[clarifyID] = resp
	g.mu.Unlock()

	// Emit SSE clarify prompt on the gate's routing topic; the payload
	// carries the asking run's session (request wins over the gate's fixed
	// session) so channel transports can route it to the bound conversation.
	g.bridge.SendClarifyPrompt(
		g.sessionID,
		g.promptSession(req.SessionID),
		clarifyID,
		req.Question,
		req.Choices,
		req.MultiSelect,
	)

	// Wait for response, context cancellation, or timeout.
	expiry := g.ClarifyExpiry()
	select {
	case response := <-resp:
		// Clean up the pending entry.
		g.mu.Lock()
		delete(g.pending, clarifyID)
		g.mu.Unlock()
		return response, nil
	case <-ctx.Done():
		// Context canceled (e.g. /stop): clean up and return.
		g.mu.Lock()
		delete(g.pending, clarifyID)
		g.mu.Unlock()
		return interfaces.ClarifyResponse{Canceled: true}, ctx.Err()
	case <-time.After(expiry):
		// Timeout: emit clarify_timeout SSE event, return TimedOut response.
		g.mu.Lock()
		delete(g.pending, clarifyID)
		g.mu.Unlock()
		g.sendClarifyTimeout(clarifyID)
		return interfaces.ClarifyResponse{TimedOut: true}, nil
	}
}

// ClarifyConfig returns the runtime clarify configuration.
func (g *WebClarifyGate) ClarifyConfig() interfaces.ClarifyConfig {
	return g.cfg
}

// ClarifyExpiry returns the configured expiry as a time.Duration.
// Default 120s when ExpirySeconds <= 0.
func (g *WebClarifyGate) ClarifyExpiry() time.Duration {
	if g.cfg.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(g.cfg.ExpirySeconds) * time.Second
}

// RespondClarify sends a response to a pending clarify request.
// Returns true if the response was delivered, false if the ID is unknown/expired.
func (g *WebClarifyGate) RespondClarify(clarifyID string, response interfaces.ClarifyResponse) bool {
	g.mu.RLock()
	ch, ok := g.pending[clarifyID]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	// Non-blocking send: if the channel is full or closed, the request already timed out.
	select {
	case ch <- response:
		return true
	default:
		return false
	}
}

// sendClarifyTimeout emits a clarify_timeout SSE event.
func (g *WebClarifyGate) sendClarifyTimeout(clarifyID string) {
	g.bridge.SendClarifyTimeout(g.sessionID, clarifyID)
}

// ClarifyAPI handles clarify response endpoints.
type ClarifyAPI struct {
	gate *WebClarifyGate
}

// ClarifyRouter is the minimum interface needed by RegisterClarifyRoutes.
type ClarifyRouter interface {
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterClarifyRoutes registers clarify response routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterClarifyRoutes(r ClarifyRouter, gate *WebClarifyGate) {
	api := &ClarifyAPI{gate: gate}
	r.Post("/clarifications/{id}/respond", api.RespondClarify)
}

// RespondClarify handles POST /api/clarifications/{id}/respond.
// Accepts {choices: []string} or {answer: string}. Sends the response to the
// pending clarify channel. Returns 200 on success, 404 if unknown/expired.
func (api *ClarifyAPI) RespondClarify(w http.ResponseWriter, r *http.Request) {
	clarifyID := chi.URLParam(r, "id")
	if clarifyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing clarify id"})
		return
	}

	var req struct {
		Choices []string `json:"choices,omitempty"`
		Answer  string   `json:"answer,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Build the ClarifyResponse: choices take precedence over free text.
	var response interfaces.ClarifyResponse
	if len(req.Choices) > 0 {
		response.Answer = req.Choices
	} else if req.Answer != "" {
		response.Answer = []string{req.Answer}
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choices or answer is required"})
		return
	}

	if api.gate.RespondClarify(clarifyID, response) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	// No live prompt: the ID may re-emit an interrupted pause after a
	// restart (durable-resume Phase 7). Without a wired backend it is
	// simply unknown.
	if b := api.gate.ResumeBackend(); b != nil {
		status, body := b.AnswerResurrectedClarify(r.Context(), clarifyID, response)
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "clarification not found or expired"})
}
