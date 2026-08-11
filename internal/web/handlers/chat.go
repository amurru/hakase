// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/web/sse"
	"github.com/go-chi/chi/v5"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// ChatAPI handles chat message endpoints and SSE streaming.
type ChatAPI struct {
	bridge     *sse.EventBridge
	sessionSvc *hakasesession.SessionService
	runner     *runner.Runner
	runtime    *hakaseagent.Runtime
}

// ChatRouter is the minimum interface needed by RegisterChatRoutes.
type ChatRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterChatRoutes registers chat-related API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterChatRoutes(r ChatRouter, bridge *sse.EventBridge, sessionSvc *hakasesession.SessionService, runner *runner.Runner, runtime *hakaseagent.Runtime) {
	api := &ChatAPI{
		bridge:     bridge,
		sessionSvc: sessionSvc,
		runner:     runner,
		runtime:    runtime,
	}

	r.Post("/sessions/{id}/messages", api.PostMessage)
	r.Get("/sessions/{id}/stream", api.StreamSSE)
}

// chatSessionID extracts the {id} URL parameter from the request.
func chatSessionID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// PostMessage handles POST /api/sessions/{id}/messages.
// Accepts {content, attachments?}. Saves the user message to the session,
// starts the agent run in a goroutine, and returns 202 Accepted.
func (api *ChatAPI) PostMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chatSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}

	var req struct {
		Content     string                   `json:"content"`
		Attachments []map[string]interface{} `json:"attachments,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}

	// Save the user message to the session.
	if api.sessionSvc != nil {
		// Ensure this session is active so AddMessage targets it.
		_ = api.sessionSvc.SetActiveSession(sessionID)
		if err := api.sessionSvc.AddMessage("user", req.Content, ""); err != nil {
			log.Printf("chat: warning: failed to save user message: %v", err)
		}
	}

	// Start the agent run in a goroutine. The run must NOT be tied to the
	// request context: net/http cancels r.Context() when the handler returns,
	// which would kill the agent run the moment the 202 is written. Use a
	// detached context so the run survives the request lifecycle (the browser
	// may refresh or reconnect its SSE stream mid-run).
	if api.runner != nil {
		content := genai.NewContentFromParts(
			[]*genai.Part{{Text: req.Content}},
			genai.RoleUser,
		)
		go api.runAgentTask(context.Background(), sessionID, content)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "accepted",
		"session_id": sessionID,
	})
}

// runAgentTask runs the agent in a goroutine, streaming all events through
// the SSE bridge. This mirrors internal/tui/ui.go:runAgentTask but writes
// to SSE channels instead of tea.Msg. The agent's final answer is also
// persisted to the session store (unlike the ADK in-memory session, which
// is created fresh per Run call), so the web UI shows agent replies after
// a reload and the HistoryBuilder can feed them back into context on the
// next message.
func (api *ChatAPI) runAgentTask(ctx context.Context, sessionID string, content *genai.Content) {
	var contentBuf, thinkBuf strings.Builder
	var lastUsage *genai.GenerateContentResponseUsageMetadata

	defer func() {
		if r := recover(); r != nil {
			log.Printf("chat: panic in agent run for session %s: %v", sessionID, r)
			api.persistAgentResponse(sessionID, contentBuf.String(), thinkBuf.String(), lastUsage)
			api.bridge.SendDone(sessionID)
		}
	}()

	runCtx := ctx
	for ev, err := range api.runner.Run(runCtx, "user-1", hakasesession.GenerateTaskID(), content, adkagent.RunConfig{}) {
		if err != nil {
			log.Printf("chat: agent error for session %s: %v", sessionID, err)
			api.bridge.SendLog(sessionID, fmt.Sprintf("Error: %v", err))
			break
		}
		if ev == nil {
			continue
		}
		// Send usage update.
		if ev.UsageMetadata != nil {
			lastUsage = ev.UsageMetadata
			tokens := int(ev.UsageMetadata.TotalTokenCount)
			if tokens <= 0 {
				tokens = int(ev.UsageMetadata.PromptTokenCount + ev.UsageMetadata.CandidatesTokenCount)
			}
			api.bridge.SendUsage(sessionID, tokens, 0)
		}
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					if part.Thought {
						thinkBuf.WriteString(part.Text)
						api.bridge.SendStreamContent(sessionID, "", part.Text)
					} else {
						contentBuf.WriteString(part.Text)
						api.bridge.SendStreamContent(sessionID, part.Text, "")
					}
				}
				if part.FunctionCall != nil {
					api.bridge.SendLog(sessionID,
						fmt.Sprintf("Call: %s(%v)", part.FunctionCall.Name, part.FunctionCall.Args),
					)
				}
				if part.FunctionResponse != nil {
					api.bridge.SendLog(sessionID,
						fmt.Sprintf("Response: %s", part.FunctionResponse.Name),
					)
				}
			}
		}
	}
	api.persistAgentResponse(sessionID, contentBuf.String(), thinkBuf.String(), lastUsage)
	api.bridge.SendDone(sessionID)
}

// persistAgentResponse saves the agent's answer to the session store so the
// web UI can render it after a reload. The message is appended to the session
// identified by sessionID directly (not the active session) so concurrent
// runs in different sessions cannot misroute replies. A run that produced no
// text (e.g. it only made tool calls and then errored) writes nothing.
func (api *ChatAPI) persistAgentResponse(sessionID, content, thinking string, usage *genai.GenerateContentResponseUsageMetadata) {
	if api.sessionSvc == nil || strings.TrimSpace(content) == "" && strings.TrimSpace(thinking) == "" {
		return
	}
	tokens := 0
	if usage != nil {
		tokens = int(usage.TotalTokenCount)
		if tokens <= 0 {
			tokens = int(usage.PromptTokenCount + usage.CandidatesTokenCount)
		}
	}
	store := api.sessionSvc.Store()
	sess, err := store.Load(sessionID)
	if err != nil {
		log.Printf("chat: warning: failed to load session %s for persistence: %v", sessionID, err)
		return
	}
	sess.AddMessageWithMetaAndAttachments("agent", content, thinking, tokens, hakasesession.MessageKindText, nil)
	if err := store.Save(sess); err != nil {
		log.Printf("chat: warning: failed to persist agent reply for session %s: %v", sessionID, err)
	}
}

// StreamSSE handles GET /api/sessions/{id}/stream.
// Sets SSE headers, subscribes to the bridge, and streams events to the client.
// Sends keepalive pings every 15 seconds. Cleans up on client disconnect.
func (api *ChatAPI) StreamSSE(w http.ResponseWriter, r *http.Request) {
	sessionID := chatSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe to the bridge.
	subID, ch := api.bridge.Subscribe(sessionID)
	defer api.bridge.Unsubscribe(sessionID, subID)

	// Keepalive ticker.
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	log.Printf("chat: SSE client connected for session %s (sub %d)", sessionID, subID)

	for {
		select {
		case <-r.Context().Done():
			log.Printf("chat: SSE client disconnected for session %s (sub %d)", sessionID, subID)
			return

		case <-keepalive.C:
			_, err := w.Write(sse.PingComment())
			if err != nil {
				log.Printf("chat: SSE keepalive write failed for session %s: %v", sessionID, err)
				return
			}
			flusher.Flush()

		case data, ok := <-ch:
			if !ok {
				// Channel closed (unsubscribed or bridge shut down).
				log.Printf("chat: SSE channel closed for session %s (sub %d)", sessionID, subID)
				return
			}
			_, err := w.Write(data)
			if err != nil {
				log.Printf("chat: SSE write failed for session %s: %v", sessionID, err)
				return
			}
			flusher.Flush()
		}
	}
}

// Ensure sse.EventBridge does not import handlers (no circular deps).
var _ interfaces.EventNotifier = (*sse.EventBridge)(nil)
