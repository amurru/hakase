// Package handlers provides HTTP handlers for the hakase web API.
// approval.go implements the web-based approval gate that bridges the agent's
// approval channel to HTTP endpoints, allowing the web UI to approve/deny
// tool invocations.
package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// WebApprovalGate implements interfaces.ApprovalGate for the web UI.
// It emits SSE approval prompts and waits for HTTP responses on a channel
// keyed by approval ID.
type WebApprovalGate struct {
	mu        sync.RWMutex
	bridge    *sse.EventBridge
	sessionID string
	cfg       interfaces.ApprovalConfig
	pending   map[string]chan bool // approvalID -> response channel
}

// NewWebApprovalGate creates a new web-based approval gate.
func NewWebApprovalGate(bridge *sse.EventBridge, sessionID string, cfg interfaces.ApprovalConfig) *WebApprovalGate {
	return &WebApprovalGate{
		bridge:    bridge,
		sessionID: sessionID,
		cfg:       cfg,
		pending:   make(map[string]chan bool),
	}
}

// AskApproval blocks until the user approves/denies or the expiry deadline
// is reached. Emits an SSE approval prompt, registers a response channel,
// and waits. On timeout, emits approval_timeout SSE event and returns false
// (fail-closed).
func (g *WebApprovalGate) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
	approvalID := "appr_" + uuid.New().String()
	resp := make(chan bool, 1)

	g.mu.Lock()
	g.pending[approvalID] = resp
	g.mu.Unlock()

	// Emit SSE approval prompt.
	g.bridge.SendApprovalPrompt(
		g.sessionID,
		approvalID,
		req.Tool,
		req.Risk,
		req.Reason,
		req.Command,
	)

	// Wait for response or timeout.
	expiry := g.ApprovalExpiry()
	select {
	case approved := <-resp:
		// Clean up the pending entry.
		g.mu.Lock()
		delete(g.pending, approvalID)
		g.mu.Unlock()
		return approved, nil
	case <-time.After(expiry):
		// Timeout: emit approval_timeout SSE event, fail-closed (deny).
		g.mu.Lock()
		delete(g.pending, approvalID)
		g.mu.Unlock()
		g.sendApprovalTimeout(approvalID)
		return false, nil
	}
}

// ApprovalConfig returns the runtime approval configuration.
func (g *WebApprovalGate) ApprovalConfig() interfaces.ApprovalConfig {
	return g.cfg
}

// ApprovalExpiry returns the configured expiry as a time.Duration.
// Default 60s when ExpirySeconds <= 0.
func (g *WebApprovalGate) ApprovalExpiry() time.Duration {
	if g.cfg.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(g.cfg.ExpirySeconds) * time.Second
}

// RespondApproval sends a response to a pending approval request.
// Returns true if the response was delivered, false if the ID is unknown/expired.
func (g *WebApprovalGate) RespondApproval(approvalID string, approved bool) bool {
	g.mu.RLock()
	ch, ok := g.pending[approvalID]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	// Non-blocking send: if the channel is full or closed, the request already timed out.
	select {
	case ch <- approved:
		return true
	default:
		return false
	}
}

// sendApprovalTimeout emits an approval_timeout SSE event.
func (g *WebApprovalGate) sendApprovalTimeout(approvalID string) {
	g.bridge.SendApprovalTimeout(g.sessionID, approvalID)
}

// ApprovalAPI handles approval response endpoints.
type ApprovalAPI struct {
	gate *WebApprovalGate
}

// ApprovalRouter is the minimum interface needed by RegisterApprovalRoutes.
type ApprovalRouter interface {
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterApprovalRoutes registers approval response routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterApprovalRoutes(r ApprovalRouter, gate *WebApprovalGate) {
	api := &ApprovalAPI{gate: gate}
	r.Post("/approvals/{id}/respond", api.RespondApproval)
}

// RespondApproval handles POST /api/approvals/{id}/respond.
// Accepts {approved: bool}. Sends the response to the pending approval channel.
// Returns 200 on success, 404 if the approval ID is unknown/expired.
func (api *ApprovalAPI) RespondApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := chi.URLParam(r, "id")
	if approvalID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing approval id"})
		return
	}

	var req struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if api.gate.RespondApproval(approvalID, req.Approved) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "approval not found or expired"})
	}
}
