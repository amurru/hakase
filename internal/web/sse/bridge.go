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
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// EventBridge bridges the agent's event bus to SSE format.
// It implements interfaces.EventNotifier and provides methods that the
// chat handler calls to stream agent content, logs, and prompts to
// connected SSE clients.
type EventBridge struct {
	mu   sync.RWMutex
	subs map[string]map[int64]chan []byte // sessionID -> subscriptionID -> channel
	next int64                            // atomic counter for subscription IDs
}

// NewEventBridge creates a new EventBridge.
func NewEventBridge() *EventBridge {
	return &EventBridge{
		subs: make(map[string]map[int64]chan []byte),
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

// publishBytes fans out a pre-formatted SSE byte slice to all subscribers
// for the session. Non-blocking: slow clients are dropped.
func (b *EventBridge) publishBytes(sessionID string, data []byte) {
	b.mu.RLock()
	sess, ok := b.subs[sessionID]
	b.mu.RUnlock()
	if !ok || len(sess) == 0 {
		return
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Re-check under lock to avoid TOCTOU
	sess, ok = b.subs[sessionID]
	if !ok {
		return
	}
	for _, ch := range sess {
		select {
		case ch <- data:
		default:
			// Drop: client is too slow
		}
	}
}

// SendStreamContent sends a streaming content chunk.
func (b *EventBridge) SendStreamContent(sessionID, content, thinking string) {
	payload, _ := json.Marshal(map[string]string{
		"content":  content,
		"thinking": thinking,
	})
	b.publishBytes(sessionID, formatSSE("stream", payload))
}

// SendMessage sends a complete agent message.
func (b *EventBridge) SendMessage(sessionID, content, thinking string) {
	payload, _ := json.Marshal(map[string]string{
		"content":  content,
		"thinking": thinking,
	})
	b.publishBytes(sessionID, formatSSE("message", payload))
}

// SendLog sends an agent log line.
func (b *EventBridge) SendLog(sessionID, line string) {
	payload, _ := json.Marshal(map[string]string{"line": line})
	b.publishBytes(sessionID, formatSSE("log", payload))
}

// SendDone signals the agent run has completed.
func (b *EventBridge) SendDone(sessionID string) {
	b.publishBytes(sessionID, formatSSE("done", []byte("{}")))
}

// SendApprovalPrompt sends an approval request to SSE clients.
// The id field is the approval request ID used for the response endpoint (task 22).
func (b *EventBridge) SendApprovalPrompt(sessionID, approvalID, tool, risk, reason, command string) {
	payload, _ := json.Marshal(map[string]any{
		"id":      approvalID,
		"tool":    tool,
		"risk":    risk,
		"reason":  reason,
		"command": command,
	})
	b.publishBytes(sessionID, formatSSE("approval", payload))
}

// SendClarifyPrompt sends a clarify request to SSE clients.
// The id field is the clarify request ID used for the response endpoint (task 22).
func (b *EventBridge) SendClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	payload, _ := json.Marshal(map[string]any{
		"id":           id,
		"question":     question,
		"choices":      choices,
		"multi_select": multiSelect,
	})
	b.publishBytes(sessionID, formatSSE("clarify", payload))
}

// SendApprovalTimeout sends an approval timeout event to SSE clients (task 22).
func (b *EventBridge) SendApprovalTimeout(sessionID, approvalID string) {
	payload, _ := json.Marshal(map[string]string{"id": approvalID})
	b.publishBytes(sessionID, formatSSE("approval_timeout", payload))
}

// SendClarifyTimeout sends a clarify timeout event to SSE clients (task 22).
func (b *EventBridge) SendClarifyTimeout(sessionID, id string) {
	payload, _ := json.Marshal(map[string]string{"id": id})
	b.publishBytes(sessionID, formatSSE("clarify_timeout", payload))
}

// SendDelegation sends a delegation progress event.
func (b *EventBridge) SendDelegation(sessionID, taskID, agent, status, message string) {
	payload, _ := json.Marshal(map[string]string{
		"task_id": taskID,
		"agent":   agent,
		"status":  status,
		"message": message,
	})
	b.publishBytes(sessionID, formatSSE("delegation", payload))
}

// SendCron sends a cron job event.
func (b *EventBridge) SendCron(sessionID, jobID, name, status, summary, outputPath string) {
	payload, _ := json.Marshal(map[string]string{
		"job_id":      jobID,
		"name":        name,
		"status":      status,
		"summary":     summary,
		"output_path": outputPath,
	})
	b.publishBytes(sessionID, formatSSE("cron", payload))
}

// SendTask sends a task board update.
func (b *EventBridge) SendTask(sessionID string, task map[string]any, action string) {
	payload, _ := json.Marshal(map[string]any{
		"task":   task,
		"action": action,
	})
	b.publishBytes(sessionID, formatSSE("task", payload))
}

// SendUsage sends a token usage update.
func (b *EventBridge) SendUsage(sessionID string, tokens int, percent int) {
	payload, _ := json.Marshal(map[string]int{
		"tokens":  tokens,
		"percent": percent,
	})
	b.publishBytes(sessionID, formatSSE("usage", payload))
}

// ---------------------------------------------------------------------------
// interfaces.EventNotifier implementation
// ---------------------------------------------------------------------------

// TaskUpdate pushes a task board mutation to SSE subscribers.
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

// DelegationProgress streams sub-agent progress to SSE subscribers.
func (b *EventBridge) DelegationProgress(status, taskID, agent, message string) {
	b.SendDelegation("", taskID, agent, status, message)
}

// CronJobEvent pushes a cron job lifecycle event to SSE subscribers.
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
	b.publishBytes(sessionID, formatSSE("sidekick", payload))
}

// ---------------------------------------------------------------------------
// SSE format helpers
// ---------------------------------------------------------------------------

// formatSSE formats an SSE message: "event: <name>\ndata: <json>\n\n".
func formatSSE(event string, data []byte) []byte {
	// Pre-allocate: len("event: \ndata: \n\n") + event + data = 16 + event + data
	buf := make([]byte, 0, 16+len(event)+len(data))
	buf = append(buf, "event: "...)
	buf = append(buf, event...)
	buf = append(buf, '\n')
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	return buf
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
	buf := make([]byte, 0, 16+len(event)+len(jsonStr))
	buf = fmt.Appendf(buf, "event: %s\ndata: %s\n\n", event, jsonStr)
	return buf
}
