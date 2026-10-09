// Package handlers provides HTTP handlers for the hakase web API.
// approval.go implements the web-based approval gate that bridges the agent's
// approval channel to HTTP endpoints, allowing the web UI to approve/deny
// tool invocations.
package handlers

import (
	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"context"
	"encoding/json"
	"fmt"
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
	pending   map[string]*pendingPrompt // approvalID -> prompt
	// resurrected maps re-emitted prompt IDs (durable-resume Phase 7)
	// to pause IDs. Live prompts resolve through pending; prompts
	// re-emitted after a restart have no blocked handler and resolve
	// through the resume backend instead.
	resurrected map[string]string // promptID -> pauseID
	resume      ResumeBackend     // nil when durable resume is unwired
}

// pendingPrompt is one live gate prompt: the response channel plus the
// metadata the pending queue (PM-003) serves to approvers.
type pendingPrompt struct {
	ch    chan bool
	req   interfaces.ApprovalRequest
	since time.Time
}

// TrackResurrected records that promptID re-emits the gate prompt for
// pauseID after a restart.
func (g *WebApprovalGate) TrackResurrected(promptID, pauseID string) {
	g.mu.Lock()
	if g.resurrected == nil {
		g.resurrected = make(map[string]string)
	}
	g.resurrected[promptID] = pauseID
	g.mu.Unlock()
}

// ResurrectedPause returns the pause ID a re-emitted prompt answers.
func (g *WebApprovalGate) ResurrectedPause(promptID string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	id, ok := g.resurrected[promptID]
	return id, ok
}

// DropResurrected forgets a re-emitted prompt (answered, expired, gone).
func (g *WebApprovalGate) DropResurrected(promptID string) {
	g.mu.Lock()
	delete(g.resurrected, promptID)
	g.mu.Unlock()
}

// SetResumeBackend wires the durable-resume answer path (nil disables).
func (g *WebApprovalGate) SetResumeBackend(b ResumeBackend) {
	g.mu.Lock()
	g.resume = b
	g.mu.Unlock()
}

// ResumeBackend returns the wired resume path, or nil.
func (g *WebApprovalGate) ResumeBackend() ResumeBackend {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.resume
}

// NewWebApprovalGate creates a new web-based approval gate.
func NewWebApprovalGate(bridge *sse.EventBridge, sessionID string, cfg interfaces.ApprovalConfig) *WebApprovalGate {
	return &WebApprovalGate{
		bridge:    bridge,
		sessionID: sessionID,
		cfg:       cfg,
		pending:   make(map[string]*pendingPrompt),
	}
}

// promptSession resolves the session a prompt should advertise: the asking
// request's session when known, the gate's fixed session otherwise.
func (g *WebApprovalGate) promptSession(reqSession string) string {
	if reqSession != "" {
		return reqSession
	}
	return g.sessionID
}

// AskApproval blocks until the user approves/denies, the context is
// canceled (e.g. /stop), or the expiry deadline is reached. Emits an
// SSE approval prompt, registers a response channel, and waits. On
// timeout, emits approval_timeout SSE event and returns false
// (fail-closed).
func (g *WebApprovalGate) AskApproval(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) {
	approvalID := "appr_" + uuid.New().String()
	p := &pendingPrompt{ch: make(chan bool, 1), req: req, since: time.Now().UTC()}

	g.mu.Lock()
	g.pending[approvalID] = p
	g.mu.Unlock()

	// Emit SSE approval prompt on the gate's routing topic; the payload
	// carries the asking run's session (request wins over the gate's fixed
	// session) so channel transports can route it to the bound conversation.
	g.bridge.SendApprovalPrompt(
		g.sessionID,
		g.promptSession(req.SessionID),
		approvalID,
		req.Tool,
		req.Risk,
		req.Reason,
		req.Command,
	)

	// Wait for response, context cancellation, or timeout.
	expiry := g.ApprovalExpiry()
	select {
	case approved := <-p.ch:
		// Clean up the pending entry.
		g.mu.Lock()
		delete(g.pending, approvalID)
		g.mu.Unlock()
		return approved, nil
	case <-ctx.Done():
		// Context canceled (e.g. /stop): clean up and return.
		g.mu.Lock()
		delete(g.pending, approvalID)
		g.mu.Unlock()
		return false, ctx.Err()
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
	p, ok := g.pending[approvalID]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	// Non-blocking send: if the channel is full or closed, the request already timed out.
	select {
	case p.ch <- approved:
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
	gate  *WebApprovalGate
	roles RoleMap // nil = open (today's single-user behavior)
}

// PendingPromptView is one queue entry for GET /api/approvals/pending.
type PendingPromptView struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Risk      string `json:"risk"`
	Reason    string `json:"reason"`
	Command   string `json:"command"`
	SessionID string `json:"session_id"`
	Since     string `json:"since"`
	Resurrect bool   `json:"resurrected"`
	PauseID   string `json:"pause_id,omitempty"`
}

// PendingApprovals snapshots live prompts plus re-emitted (resurrected)
// ones, oldest first. Resurrected entries carry no tool metadata (the
// blocked handler is gone) and answer through the resume backend.
func (g *WebApprovalGate) PendingApprovals() []PendingPromptView {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []PendingPromptView
	for id, p := range g.pending {
		out = append(out, PendingPromptView{
			ID: id, Tool: p.req.Tool, Risk: p.req.Risk, Reason: p.req.Reason,
			Command: p.req.Command, SessionID: g.promptSession(p.req.SessionID),
			Since: p.since.Format(time.RFC3339),
		})
	}
	for id, pauseID := range g.resurrected {
		if _, live := g.pending[id]; live {
			continue
		}
		out = append(out, PendingPromptView{ID: id, Resurrect: true, PauseID: pauseID})
	}
	sortPending(out)
	return out
}

func sortPending(v []PendingPromptView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].Since < v[j-1].Since; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// Role is a web RBAC tier: viewer reads the queue, approver answers,
// admin answers plus future audit administration.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleApprover Role = "approver"
	RoleAdmin    Role = "admin"
)

// RoleMap maps usernames (JWT subject, the allowlist IDs) to roles.
// A nil/empty map is fully open (today's single-user behavior); with
// entries, unlisted users read as viewer.
type RoleMap map[string]Role

// ParseRoleMap validates a raw username -> role mapping.
func ParseRoleMap(raw map[string]string) (RoleMap, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(RoleMap, len(raw))
	for user, r := range raw {
		switch Role(r) {
		case RoleViewer, RoleApprover, RoleAdmin:
			out[user] = Role(r)
		default:
			return nil, fmt.Errorf("web role for %q: invalid %q (want viewer|approver|admin)", user, r)
		}
	}
	return out, nil
}

// rank orders tiers for minimum-role checks.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleApprover:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// RoleFor resolves a user's tier: open admin on an empty map, the mapped
// tier when listed, viewer otherwise (see prompts, answer nothing).
func (m RoleMap) RoleFor(user string) Role {
	if len(m) == 0 {
		return RoleAdmin
	}
	if r, ok := m[user]; ok {
		return r
	}
	return RoleViewer
}

// requireRole rejects below-minimum tiers with 403 (unauthenticated
// requests never reach here: AuthMiddleware runs first).
func (api *ApprovalAPI) requireRole(min Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if api.roles.RoleFor(r.Header.Get("X-Hakase-User")).rank() < min.rank() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: approver role required"})
			return
		}
		next(w, r)
	}
}

// ApprovalRouteRegistrar is the route surface approval registration needs.
type ApprovalRouteRegistrar interface {
	Post(pattern string, handlerFn http.HandlerFunc)
	Get(pattern string, handlerFn http.HandlerFunc)
}

// RegisterApprovalRoutes registers approval response routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
// Role enforcement is open (today's behavior); use
// RegisterApprovalRoutesWithRoles for RBAC.
func RegisterApprovalRoutes(r ApprovalRouteRegistrar, gate *WebApprovalGate) {
	RegisterApprovalRoutesWithRoles(r, gate, nil)
}

// RegisterApprovalRoutesWithRoles registers the pending queue, the single
// respond endpoint, and the batch respond endpoint with RBAC: the queue
// needs viewer, answering needs approver. Batch answers loop over
// RespondApproval so web + phone keep first-responder-wins.
func RegisterApprovalRoutesWithRoles(r ApprovalRouteRegistrar, gate *WebApprovalGate, roles RoleMap) {
	api := &ApprovalAPI{gate: gate, roles: roles}
	r.Get("/approvals/pending", api.requireRole(RoleViewer, api.PendingApprovals))
	r.Post("/approvals/{id}/respond", api.requireRole(RoleApprover, api.RespondApproval))
	r.Post("/approvals/respond", api.requireRole(RoleApprover, api.RespondBatch))
}

// PendingApprovals handles GET /api/approvals/pending.
func (api *ApprovalAPI) PendingApprovals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.gate.PendingApprovals())
}

// RespondBatch handles POST /api/approvals/respond with
// {ids: [...], approved: bool}. Each ID answers independently through
// RespondApproval (first response wins per prompt); results report
// per-ID delivery so a partially-answered batch is visible.
func (api *ApprovalAPI) RespondBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []string `json:"ids"`
		Approved bool     `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no approval ids"})
		return
	}
	results := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if id == "" {
			continue
		}
		delivered := api.gate.RespondApproval(id, req.Approved)
		if delivered {
			api.auditAnswer(id, req.Approved, r)
		}
		results[id] = delivered
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// auditAnswer records who answered a prompt (actor = web username).
// Only delivered answers are recorded: lost races resolve elsewhere
// and their winner's entry is the audit truth.
func (api *ApprovalAPI) auditAnswer(approvalID string, approved bool, r *http.Request) {
	tool := ""
	for _, pv := range api.gate.PendingApprovals() {
		if pv.ID == approvalID {
			tool = pv.Tool
			break
		}
	}
	// PendingApprovals still lists the prompt (cleanup happens on the
	// blocked handler's return); the tool lookup above best-effort
	// enriches the entry, "" falls back to "approval".
	hakaseagent.AuditApprovalAnswer(approvalID, tool, approved, r.Header.Get("X-Hakase-User"), "web")
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
		api.auditAnswer(approvalID, req.Approved, r)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	// No live prompt: the ID may re-emit an interrupted pause after a
	// restart (durable-resume Phase 7). Without a wired backend it is
	// simply unknown.
	if b := api.gate.ResumeBackend(); b != nil {
		status, body := b.AnswerResurrectedApproval(r.Context(), approvalID, req.Approved)
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "approval not found or expired"})
}
