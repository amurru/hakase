// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/agentrun"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	hakasesidekick "amurru/hakase/internal/sidekick"
	"amurru/hakase/internal/web/sse"

	"github.com/go-chi/chi/v5"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// maxConcurrentAgentRuns is the maximum number of concurrent agent runs
// allowed per session. Additional runs receive a 429 response.
const maxConcurrentAgentRuns = 3

// Web attachment caps, mirroring internal/tui/attach.go.
const (
	maxWebAttachImageBytes = 10 * 1024 * 1024
	maxWebAttachTextBytes  = 200 * 1024
	// maxWebAttachBase64Str bounds the raw base64 string before decoding so a
	// huge payload cannot balloon during decode (4/3 expansion + slack).
	maxWebAttachBase64Str = maxWebAttachImageBytes*4/3 + 4096
)

// incomingAttachment mirrors one entry of the `attachments` array the web UI
// posts to POST /api/sessions/{id}/messages. Pasted images carry base64
// Data; @-picked workspace files carry Path (resolved server-side).
type incomingAttachment struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	MIME  string `json:"mime"`
	Label string `json:"label"`
	Data  string `json:"data,omitempty"`
}

// buildAttachmentParts converts web-UI attachments into genai content parts,
// mirroring the TUI path (internal/tui/attach.go buildMessageParts): images
// become inline data parts, text files become text parts. It also returns the
// AttachmentRefs to persist with the user message (path + mime only; content
// is re-read from disk on history rebuilds) and manifest lines describing
// each attachment.
//
// The manifest matters on non-vision main models: the vision pipeline
// replaces inline image parts with a text description that carries no file
// path, so without it the agent could not reference the attached file by path
// (e.g. as generate_video's image argument).
//
// Per-attachment failures are hard errors: unlike the TUI (which validates at
// chip-creation time), the web UI sends everything in one request, so a
// dropped attachment would silently change what the model sees. The returned
// error names the failing attachment.
func buildAttachmentParts(atts []incomingAttachment) (parts []*genai.Part, refs []hakasesession.AttachmentRef, manifest []string, err error) {
	for i, att := range atts {
		name := att.Name
		if name == "" {
			name = fmt.Sprintf("attachment %d", i+1)
		}
		fail := func(format string, args ...interface{}) error {
			return fmt.Errorf("attachment %q: %s", name, fmt.Sprintf(format, args...))
		}

		switch {
		case strings.TrimSpace(att.Data) != "":
			// Inline payload (pasted image).
			if len(att.Data) > maxWebAttachBase64Str {
				return nil, nil, nil, fail("inline data too large (max %d MB)", maxWebAttachImageBytes/(1024*1024))
			}
			data, err := base64.StdEncoding.DecodeString(att.Data)
			if err != nil {
				if data, err = base64.RawStdEncoding.DecodeString(att.Data); err != nil {
					return nil, nil, nil, fail("invalid base64 data")
				}
			}
			mimeType := att.MIME
			if mimeType == "" {
				mimeType = detectMIME(name)
			}
			if len(data) > maxWebAttachImageBytes {
				return nil, nil, nil, fail("image too large (%d KB, max %d KB)", len(data)/1024, maxWebAttachImageBytes/1024)
			}
			if isImageMIME(mimeType) {
				parts = append(parts, genai.NewPartFromBytes(data, mimeType))
			} else if !utf8.Valid(data) {
				return nil, nil, nil, fail("attachment %q is not valid UTF-8 text or an image", name)
			} else {
				parts = append(parts, genai.NewPartFromText(hctx.WrapUntrustedData(string(data))))
			}
			// Default the label to match the workspace-file branch so a
			// client-omitted label never leaves a blank manifest field.
			label := att.Label
			if label == "" {
				label = "@" + name
			}
			refs = append(refs, hakasesession.AttachmentRef{
				Name:  name,
				Path:  "", // pasted payloads are not on disk
				MIME:  mimeType,
				Label: label,
			})
			manifest = append(manifest, fmt.Sprintf("%s %s (pasted %s)", label, name, mimeType))

		case strings.TrimSpace(att.Path) != "":
			// Workspace file: resolve through sandbox read roots (fails
			// closed outside approved roots and for sensitive files), then
			// read with the TUI size caps.
			resolved, err := resolveFilePath(att.Path)
			if err != nil {
				return nil, nil, nil, fail("%v", err)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return nil, nil, nil, fail("cannot stat file: %v", err)
			}
			mimeType := att.MIME
			if mimeType == "" || mimeType == "application/octet-stream" {
				mimeType = detectMIME(resolved)
			}
			cap := maxWebAttachTextBytes
			if isImageMIME(mimeType) {
				cap = maxWebAttachImageBytes
			}
			if info.Size() > int64(cap) {
				return nil, nil, nil, fail("too large (%d KB, max %d KB)", info.Size()/1024, cap/1024)
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return nil, nil, nil, fail("cannot read file: %v", err)
			}
			if isImageMIME(mimeType) {
				parts = append(parts, genai.NewPartFromBytes(data, mimeType))
			} else if !utf8.Valid(data) {
				return nil, nil, nil, fail("attachment %q is not valid UTF-8 text or an image", name)
			} else {
				parts = append(parts, genai.NewPartFromText(hctx.WrapUntrustedData(string(data))))
			}
			refs = append(refs, hakasesession.AttachmentRef{
				Name:  name,
				Path:  resolved,
				MIME:  mimeType,
				Label: att.Label,
			})
			label := att.Label
			if label == "" {
				label = "@" + name
			}
			manifest = append(manifest, fmt.Sprintf("%s %s (%s)", label, att.Path, mimeType))
		}
		// Entries with neither data nor path are skipped silently - the web
		// UI never produces them.
	}
	return parts, refs, manifest, nil
}

// sessionSem tracks concurrent agent runs for a single session.
type sessionSem struct {
	mu      sync.Mutex
	counter int
}

// acquire increments the counter if under the limit. Returns true if acquired.
func (s *sessionSem) acquire(max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counter >= max {
		return false
	}
	s.counter++
	return true
}

// release decrements the counter.
func (s *sessionSem) release() {
	s.mu.Lock()
	s.counter--
	s.mu.Unlock()
}

// ChatAPI handles chat message endpoints and SSE streaming.
type ChatAPI struct {
	bridge     *sse.EventBridge
	sessionSvc *hakasesession.SessionService
	runner     *runner.Runner
	runtime    *hakaseagent.Runtime
	history    *hctx.HistoryBuilder
	driver     *agentrun.Driver

	semMu         sync.Mutex
	runSemaphores map[string]*sessionSem
}

// ChatRouter is the minimum interface needed by RegisterChatRoutes.
type ChatRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterChatRoutes registers chat-related API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterChatRoutes(r ChatRouter, bridge *sse.EventBridge, sessionSvc *hakasesession.SessionService, runner *runner.Runner, runtime *hakaseagent.Runtime, history *hctx.HistoryBuilder) {
	api := &ChatAPI{
		bridge:        bridge,
		sessionSvc:    sessionSvc,
		runner:        runner,
		runtime:       runtime,
		history:       history,
		driver:        agentrun.New(runner, sessionSvc),
		runSemaphores: make(map[string]*sessionSem),
	}

	r.Post("/sessions/{id}/messages", api.PostMessage)
	r.Get("/sessions/{id}/stream", api.StreamSSE)
	r.Post("/sessions/{id}/sidekick", api.PostSidekick)
	r.Post("/sessions/{id}/compact", api.PostCompact)
}

// PostSidekick handles POST /api/sessions/{id}/sidekick.
// It asks the configured sidekick model a direct question and streams the
// answer back as a sidekick SSE event (mirroring the TUI /sidekick command).
// The sidekick runs independently of the main agent and does not disturb the
// current conversation. When the sidekick is disabled or no question is
// supplied, it responds with a clear JSON error (no SSE stream is opened).
func (api *ChatAPI) PostSidekick(w http.ResponseWriter, r *http.Request) {
	sessionID := chatSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}

	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question required"})
		return
	}

	if api.runtime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sidekick unavailable"})
		return
	}
	sk := api.runtime.Sidekick()
	if sk == nil || !sk.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sidekick is not enabled"})
		return
	}

	// Run the sidekick Ask asynchronously so the HTTP handler returns promptly;
	// the answer is pushed as a sidekick SSE event when it lands. Use a
	// detached context: the request context is cancelled the moment the 202 is
	// written, which would abort the Ask before it completes.
	sem := api.getOrCreateSem(sessionID)
	if !sem.acquire(2) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many concurrent requests for this session"})
		return
	}
	go func() {
		defer sem.release()
		// Persist the exchange under kind "sidekick" so sessions/<id>.json
		// records provenance: question as a tagged user turn, answer with
		// role "sidekick" (same shape as watchdog notes).
		var skStore hakasesidekick.TranscriptStore
		if api.sessionSvc != nil {
			skStore = api.sessionSvc.Store()
		}

		// Give on-demand asks conversational context: frame the question
		// with the recent in-session transcript (same budget the watchdog
		// uses), BEFORE recording this turn so it is not duplicated.
		prompt := question
		if skStore != nil {
			if sess, err := skStore.Load(sessionID); err == nil && sess != nil {
				prompt = hakasesidekick.BuildAskPrompt(
					hakasesidekick.RecentTranscript(sess, sk.TranscriptWindow()),
					question,
				)
			}
		}
		hakasesidekick.RecordQuestion(skStore, sessionID, question)

		answer, err := sk.Ask(context.Background(), prompt)
		if err != nil {
			// Persist the failure too: with no SSE subscriber connected the
			// warning below would be dropped, leaving the recorded question
			// permanently unanswered in sessions/<id>.json.
			hakasesidekick.RecordAnswer(skStore, sessionID, "[sidekick error] "+err.Error())
			if api.bridge != nil {
				api.bridge.SendSidekick(sessionID, "warning", "sidekick error: "+err.Error())
			}
			return
		}
		hakasesidekick.RecordAnswer(skStore, sessionID, answer)
		if api.bridge != nil {
			api.bridge.SendSidekick(sessionID, "info", answer)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "accepted",
		"session_id": sessionID,
	})
}

// PostCompact handles POST /api/sessions/{id}/compact with an optional
// {"focus": "..."} body. It mirrors the TUI /compact command: a deterministic
// history snip runs immediately (last 2 user turns are always kept) and the
// async LLM summary is scheduled on the shared HistoryBuilder. Refuses while
// an agent run is active for this session, matching TUI behavior.
func (api *ChatAPI) PostCompact(w http.ResponseWriter, r *http.Request) {
	sessionID := chatSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}
	if api.history == nil || api.sessionSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "compaction unavailable"})
		return
	}

	var req struct {
		Focus string `json:"focus"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // optional body; focus may be empty
	}

	// Refuse mid-run compaction: mutating InContext flags while the history
	// builder iterates the same session would race with the live agent.
	api.semMu.Lock()
	sem := api.runSemaphores[sessionID]
	api.semMu.Unlock()
	if sem != nil {
		sem.mu.Lock()
		busy := sem.counter > 0
		sem.mu.Unlock()
		if busy {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "cannot compact while the agent is working"})
			return
		}
	}

	store := api.sessionSvc.Store()
	sess, err := store.Load(sessionID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if len(sess.Messages) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "nothing_to_compact"})
		return
	}

	api.history.StageBSnip(sess, nil, "", 0, 8000, 0)
	if err := store.Save(sess); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save compacted session"})
		return
	}
	api.history.ScheduleSummarize(sessionID, strings.TrimSpace(req.Focus))

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"summary": "generating",
	})
}

// chatSessionID extracts the {id} URL parameter from the request.
func chatSessionID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// getOrCreateSem returns the sessionSem for sessionID, creating one if needed.
// Must be called while holding api.semMu (or use the caller-safe wrapper).
func (api *ChatAPI) getOrCreateSem(sessionID string) *sessionSem {
	s, ok := api.runSemaphores[sessionID]
	if !ok {
		s = &sessionSem{}
		api.runSemaphores[sessionID] = s
	}
	return s
}

// PostMessage handles POST /api/sessions/{id}/messages.
// Accepts {content, attachments?}. Attachments (pasted images as base64
// data, @-picked files as workspace paths) are converted into genai content
// parts and sent to the agent alongside the text, mirroring the TUI path;
// the refs are persisted with the user message. Saves the message to the
// session, starts the agent run in a goroutine, and returns 202 Accepted.
func (api *ChatAPI) PostMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chatSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}

	var req struct {
		Content     string               `json:"content"`
		Attachments []incomingAttachment `json:"attachments,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Content) == "" && len(req.Attachments) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content or attachments required"})
		return
	}

	// Convert attachments into content parts up front so an invalid
	// attachment rejects the whole request before anything is persisted.
	promptText := strings.TrimSpace(req.Content)
	attParts, refs, manifest, err := buildAttachmentParts(req.Attachments)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Append the attachment manifest to the request text (and to the
	// persisted content, keeping them byte-identical for the history dedup).
	// On non-vision models the vision pipeline replaces image parts with a
	// path-less description, so this manifest is the only place the agent
	// learns the attached file's path.
	if len(manifest) > 0 {
		promptText = strings.TrimSpace(promptText + "\n[attachments]\n" + strings.Join(manifest, "\n"))
	}
	parts := make([]*genai.Part, 0, len(attParts)+1)
	if promptText != "" {
		parts = append(parts, genai.NewPartFromText(promptText))
	}
	parts = append(parts, attParts...)

	// Save the user message to the session (prompt + manifest + attachment
	// refs, same contract as the TUI; content is rebuilt from refs on resume).
	if api.sessionSvc != nil {
		// Ensure this session is active so AddMessage targets it.
		_ = api.sessionSvc.SetActiveSession(sessionID)
		if err := api.sessionSvc.RecordUsageWithAttachments("user", promptText, "", 0, refs); err != nil {
			log.Printf("chat: warning: failed to save user message: %v", err)
		}
	}

	// Start the agent run in a goroutine. The run must NOT be tied to the
	// request context: net/http cancels r.Context() when the handler returns,
	// which would kill the agent run the moment the 202 is written. Use a
	// detached context so the run survives the request lifecycle (the browser
	// may refresh or reconnect its SSE stream mid-run).
	if api.runner != nil {
		// Per-session concurrency cap: reject if this session is saturated.
		api.semMu.Lock()
		sem := api.getOrCreateSem(sessionID)
		if !sem.acquire(maxConcurrentAgentRuns) {
			api.semMu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "too many concurrent agent runs for this session",
			})
			return
		}
		api.semMu.Unlock()

		content := genai.NewContentFromParts(parts, genai.RoleUser)
		go api.runAgentTask(context.Background(), sessionID, content)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "accepted",
		"session_id": sessionID,
	})
}

// bridgeSink adapts the SSE bridge to the agentrun.EventSink interface so the
// shared turn driver streams through the same events the web UI consumes.
type bridgeSink struct{ b *sse.EventBridge }

func (s bridgeSink) OnStream(sessionID, content, thinking string) {
	s.b.SendStreamContent(sessionID, content, thinking)
}

func (s bridgeSink) OnLog(sessionID, line string) { s.b.SendLog(sessionID, line) }

func (s bridgeSink) OnUsage(sessionID string, tokens, percent int) {
	s.b.SendUsage(sessionID, tokens, percent)
}

func (s bridgeSink) OnDone(sessionID string) { s.b.SendDone(sessionID) }

// runAgentTask runs the agent in a goroutine via the shared agentrun.Driver,
// which streams all events through the SSE bridge and persists the agent's
// final answer. This mirrors internal/tui/ui.go:runAgentTask; the run loop
// itself lives in internal/agentrun so the Telegram channel and future
// transports drive the identical path. The per-session concurrency slot
// acquired by PostMessage is released here, when the run ends (normally, on
// error, or on panic - the driver recovers panics internally).
func (api *ChatAPI) runAgentTask(ctx context.Context, sessionID string, content *genai.Content) {
	defer func() {
		api.semMu.Lock()
		if sem, ok := api.runSemaphores[sessionID]; ok {
			sem.release()
		}
		api.semMu.Unlock()
	}()
	if api.driver == nil {
		return
	}
	api.driver.RunTurn(ctx, sessionID, content, bridgeSink{b: api.bridge})
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
