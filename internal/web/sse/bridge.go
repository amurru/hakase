// Package sse provides the EventBridge that translates the agent event bus
// into Server-Sent Events (SSE). It implements interfaces.EventNotifier for
// structured events (TaskUpdate, DelegationProgress, CronJobEvent) and exposes
// Send* methods the chat handler calls for stream content, logs, and
// interactive prompts.
//
// The bridge is thread-safe: multiple SSE clients can connect to the same
// session, and publishes are non-blocking (slow clients are dropped).
package sse

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/util"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Event is a typed bridge event, delivered to non-SSE consumers (e.g. the
// communication-channel router). Name is the SSE event name ("stream", "log",
// "approval", ...) and Data is the raw JSON payload exactly as published.
type Event struct {
	Name string
	Data json.RawMessage
}

// EventBridge bridges the agent's event bus to SSE format.
// It implements interfaces.EventNotifier and provides methods that the
// chat handler calls to stream agent content, logs, and prompts to
// connected SSE clients.
type EventBridge struct {
	mu        sync.RWMutex
	subs      map[string]map[int64]chan []byte // sessionID -> subscriptionID -> channel
	typedSubs map[string]map[int64]chan Event  // sessionID -> subscriptionID -> typed channel
	next      int64                            // atomic counter for subscription IDs

	// graphLogs retains recent execution-canvas events per session for the
	// REST backfill endpoint (see graph.go). Guarded by mu.
	graphLogs     map[string]*graphLog
	graphLogOrder []string // FIFO of session ids for capacity eviction
}

// NewEventBridge creates a new EventBridge.
func NewEventBridge() *EventBridge {
	return &EventBridge{
		subs:          make(map[string]map[int64]chan []byte),
		typedSubs:     make(map[string]map[int64]chan Event),
		graphLogs:     make(map[string]*graphLog),
		graphLogOrder: []string{},
	}
}

// Subscribe registers a new SSE client for the given session.
// Returns the subscription ID and a receive-only channel.
func (b *EventBridge) Subscribe(sessionID string) (int64, <-chan []byte) {
	id := atomic.AddInt64(&b.next, 1)
	ch := make(chan []byte, 256)

	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = make(map[int64]chan []byte)
	}
	b.subs[sessionID][id] = ch
	b.mu.Unlock()

	return id, ch
}

// Unsubscribe removes a subscription. Safe to call multiple times.
func (b *EventBridge) Unsubscribe(sessionID string, subID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sess, ok := b.subs[sessionID]; ok {
		if ch, ok := sess[subID]; ok {
			delete(sess, subID)
			close(ch)
		}
		if len(sess) == 0 {
			delete(b.subs, sessionID)
		}
	}
}

// SubscribeEvents registers a typed-event consumer for the given session.
// Unlike Subscribe (raw SSE frames), events arrive as Event structs with the
// event name and raw JSON payload already separated. Returns the subscription
// ID and a receive-only channel.
func (b *EventBridge) SubscribeEvents(sessionID string) (int64, <-chan Event) {
	id := atomic.AddInt64(&b.next, 1)
	ch := make(chan Event, 256)

	b.mu.Lock()
	if b.typedSubs[sessionID] == nil {
		b.typedSubs[sessionID] = make(map[int64]chan Event)
	}
	b.typedSubs[sessionID][id] = ch
	b.mu.Unlock()

	return id, ch
}

// UnsubscribeEvents removes a typed-event subscription. Safe to call multiple
// times.
func (b *EventBridge) UnsubscribeEvents(sessionID string, subID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sess, ok := b.typedSubs[sessionID]; ok {
		if ch, ok := sess[subID]; ok {
			delete(sess, subID)
			close(ch)
		}
		if len(sess) == 0 {
			delete(b.typedSubs, sessionID)
		}
	}
}

// publish fans out an event to all raw-frame subscribers (as a formatted SSE
// frame) and all typed-event subscribers (as an Event struct) for the session.
// Non-blocking: slow consumers are dropped.
func (b *EventBridge) publish(sessionID string, event string, data []byte) {
	frame := formatSSE(event, data)
	payloadCopy := append(json.RawMessage(nil), data...)

	b.mu.Lock()
	rawSess := b.subs[sessionID]
	typedSess := b.typedSubs[sessionID]
	for _, ch := range rawSess {
		select {
		case ch <- frame:
		default:
			// Drop: client is too slow
		}
	}
	for _, ch := range typedSess {
		select {
		case ch <- Event{Name: event, Data: payloadCopy}:
		default:
			// Drop: consumer is too slow
		}
	}
	b.mu.Unlock()
}

// SendStreamContent sends a streaming content chunk.
func (b *EventBridge) SendStreamContent(sessionID, content, thinking string) {
	payload, _ := json.Marshal(map[string]string{
		"content":  content,
		"thinking": thinking,
	})
	b.publish(sessionID, "stream", payload)
}

// SendMessage sends a complete agent message.
func (b *EventBridge) SendMessage(sessionID, content, thinking string) {
	payload, _ := json.Marshal(map[string]string{
		"content":  content,
		"thinking": thinking,
	})
	b.publish(sessionID, "message", payload)
}

// SendLog sends an agent log line.
func (b *EventBridge) SendLog(sessionID, line string) {
	payload, _ := json.Marshal(map[string]string{"line": line})
	b.publish(sessionID, "log", payload)
}

// SendDone signals the agent run has completed.
func (b *EventBridge) SendDone(sessionID string) {
	b.publish(sessionID, "done", []byte("{}"))
}

// SendApprovalPrompt sends an approval request to SSE clients. routeSession
// is the topic the event is published under ("" = the global topic every
// gate consumer subscribes to); sessionID is the asking run's session, which
// travels in the payload so channel transports can route the prompt to the
// conversation bound to that session. The id field is the approval request
// ID used for the response endpoint (task 22).
func (b *EventBridge) SendApprovalPrompt(routeSession, sessionID, approvalID, tool, risk, reason, command string) {
	payload, _ := json.Marshal(map[string]any{
		"id":         approvalID,
		"session_id": sessionID,
		"tool":       tool,
		"risk":       risk,
		"reason":     reason,
		"command":    command,
	})
	b.publish(routeSession, "approval", payload)
}

// SendClarifyPrompt sends a clarify request to SSE clients; routeSession and
// sessionID follow SendApprovalPrompt's split. The id field is the clarify
// request ID used for the response endpoint (task 22).
func (b *EventBridge) SendClarifyPrompt(routeSession, sessionID, id, question string, choices []string, multiSelect bool) {
	payload, _ := json.Marshal(map[string]any{
		"id":           id,
		"session_id":   sessionID,
		"question":     question,
		"choices":      choices,
		"multi_select": multiSelect,
	})
	b.publish(routeSession, "clarify", payload)
}

// SendApprovalTimeout sends an approval timeout event to SSE clients (task 22).
func (b *EventBridge) SendApprovalTimeout(sessionID, approvalID string) {
	payload, _ := json.Marshal(map[string]string{"id": approvalID})
	b.publish(sessionID, "approval_timeout", payload)
}

// SendClarifyTimeout sends a clarify timeout event to SSE clients (task 22).
func (b *EventBridge) SendClarifyTimeout(sessionID, id string) {
	payload, _ := json.Marshal(map[string]string{"id": id})
	b.publish(sessionID, "clarify_timeout", payload)
}

// SendDelegation sends a delegation progress event. sessionID is the
// producer (parent run's) session, carried in the payload as session_id
// so channel transports can gate the push on the producer label (S8);
// the frame is published to the producer topic for per-session SSE views
// and additionally to the global topic (""), which the channel Router
// subscribes to. An empty sessionID publishes to the global topic once
// (v1 parity for session-less producers). S12: redact-only - the message
// may carry tool output; it still publishes (no gating, no binding at
// the bridge - per-recipient gating stays at the push transports).
func (b *EventBridge) SendDelegation(sessionID, taskID, agent, status, message string) {
	payload, _ := json.Marshal(map[string]string{
		"session_id": sessionID,
		"task_id":    taskID,
		"agent":      agent,
		"status":     status,
		"message":    util.RedactTruncate(message, 0),
	})
	b.publish(sessionID, "delegation", payload)
	if sessionID != "" {
		b.publish("", "delegation", payload)
	}
}

// SendCron sends a cron job event. sessionID is the producer session
// (see SendDelegation); empty keeps the legacy global-only publish.
// S12: redact-only (see SendDelegation) - the summary may carry the job
// prompt or run output.
func (b *EventBridge) SendCron(sessionID, jobID, name, status, summary, outputPath string) {
	payload, _ := json.Marshal(map[string]string{
		"session_id":  sessionID,
		"job_id":      jobID,
		"name":        util.RedactTruncate(name, 0),
		"status":      status,
		"summary":     util.RedactTruncate(summary, 0),
		"output_path": util.RedactTruncate(outputPath, 0),
	})
	b.publish(sessionID, "cron", payload)
	if sessionID != "" {
		b.publish("", "cron", payload)
	}
}

// SendTask sends a task board update. sessionID is the producer session
// (see SendDelegation); empty keeps the legacy global-only publish.
func (b *EventBridge) SendTask(sessionID string, task map[string]any, action string) {
	payload, _ := json.Marshal(map[string]any{
		"session_id": sessionID,
		"task":       task,
		"action":     action,
	})
	b.publish(sessionID, "task", payload)
	if sessionID != "" {
		b.publish("", "task", payload)
	}
}

// SendUsage sends a token usage update (SSE v2: cost_usd and budget_pct
// are additive; older clients ignore unknown fields). cached carries the
// prompt-cache read for the turn (PC-007).
func (b *EventBridge) SendUsage(sessionID string, tokens, cached int, percent int, costUSD float64, budgetPct int) {
	payload, _ := json.Marshal(map[string]any{
		"tokens":     tokens,
		"cached":     cached,
		"percent":    percent,
		"cost_usd":   costUSD,
		"budget_pct": budgetPct,
	})
	b.publish(sessionID, "usage", payload)
}

// ---------------------------------------------------------------------------
// interfaces.EventNotifier implementation
// ---------------------------------------------------------------------------

// TaskUpdate pushes a task board mutation to SSE subscribers. Task
// mutations carry no producer session (the board is shared state), so
// the push stays global and channels gate on the recipient label.
func (b *EventBridge) TaskUpdate(action string, task interfaces.TaskMeta) {
	taskMap := map[string]any{
		"id":           task.ID,
		"version":      task.Version,
		"title":        task.Title,
		"description":  task.Description,
		"status":       string(task.Status),
		"priority":     string(task.Priority),
		"owner":        task.Owner,
		"assignee":     task.Assignee,
		"dependencies": task.Dependencies,
		"blocked_by":   task.BlockedBy,
		"parent_id":    task.ParentID,
		"tags":         task.Tags,
		"attempts":     task.Attempts,
		"max_attempts": task.MaxAttempts,
	}
	b.SendTask("", taskMap, action)
}

// DelegationProgress streams sub-agent progress to SSE subscribers. The
// producer session resolves via the task registry (delegate.go registers
// the delegation task id against the parent run's session); unknown tasks
// stay session-less (recipient-gated fallback, v1 parity).
func (b *EventBridge) DelegationProgress(status, taskID, agent, message string) {
	b.SendDelegation(interfaces.SessionForTask(taskID), taskID, agent, status, message)
}

// CronJobEvent pushes a cron job lifecycle event to SSE subscribers.
// Cron producers run session-less, so the push stays global and channels
// gate on the recipient label; SendCron with a session routes a
// session-scoped producer when a caller has one.
func (b *EventBridge) CronJobEvent(status, jobID, name, summary, outputPath string) {
	b.SendCron("", jobID, name, status, summary, outputPath)
}

// SidekickEvent pushes a sidekick advisory note to SSE subscribers.
func (b *EventBridge) SidekickEvent(sessionID, severity, text string) {
	b.SendSidekick(sessionID, severity, text)
}

// SendSidekick sends a sidekick advisory note event.
func (b *EventBridge) SendSidekick(sessionID, severity, text string) {
	payload, _ := json.Marshal(map[string]string{
		"severity": severity,
		"text":     text,
	})
	b.publish(sessionID, "sidekick", payload)
}

// ---------------------------------------------------------------------------
// SSE format helpers
// ---------------------------------------------------------------------------

// formatSSE formats an SSE message: "event: <name>\ndata: <json>\n\n".
// The frame is grown by append/fmt rather than a computed make size: any
// len-derived size expression trips CodeQL's allocation-size-overflow query.
func formatSSE(event string, data []byte) []byte {
	buf := fmt.Appendf(nil, "event: %s\ndata: ", event)
	buf = append(buf, data...)
	return append(buf, '\n', '\n')
}

// PingComment returns a keepalive SSE comment line.
func PingComment() []byte {
	return []byte(": ping\n\n")
}

// SSEError returns an SSE error event.
func SSEError(message string) []byte {
	payload, _ := json.Marshal(map[string]string{"error": message})
	return formatSSE("error", payload)
}

// formatSSEBytes is a convenience wrapper returning []byte from formatted strings.
// Used for testing and debugging.
func formatSSEBytes(event string, jsonStr string) []byte {
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, jsonStr)
}
