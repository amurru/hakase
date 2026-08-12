package util

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// Attachment is a single file or image attached to a queued message.
type Attachment struct {
	ID    int
	Kind  string // "file" or "image"
	Name  string // display name (basename)
	Path  string // resolved absolute path ("" for pasted clipboard images)
	MIME  string
	Data  []byte
	Label string
}

// IsImageMIME reports whether mime is an image type that should be
// treated as inline data (not text).
func IsImageMIME(mime string) bool {
	return imageMimes[mime]
}

// imageMimes is the set of MIME types treated as images (inline data parts).
var imageMimes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/gif":     true,
	"image/webp":    true,
	"image/bmp":     true,
	"image/x-icon":  true,
	"image/svg+xml": true,
}

// QueuedPrompt is a message captured while the agent is busy. It is steered
// into the running session at the next model-call boundary (BeforeModelCallback)
// and drained into its own turn when the current run completes.
type QueuedPrompt struct {
	Text   string
	Attach []Attachment
}

// PendingQueue is the concurrency-safe FIFO of prompts typed while the agent
// is processing. Written by the TUI goroutine (Enter while busy), read by the
// HistoryBuilder callback (agent goroutine) for mid-run steering, and drained
// by the agentDoneMsg handler (TUI goroutine) at run end.
type PendingQueue struct {
	mu    sync.Mutex
	items []QueuedPrompt
}

// NewPendingQueue creates an empty PendingQueue.
func NewPendingQueue() *PendingQueue { return &PendingQueue{} }

// Push appends a prompt to the back of the queue.
func (q *PendingQueue) Push(p QueuedPrompt) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, p)
}

// Pop removes and returns the oldest prompt (ok=false when empty).
func (q *PendingQueue) Pop() (QueuedPrompt, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return QueuedPrompt{}, false
	}
	p := q.items[0]
	q.items = q.items[1:]
	return p, true
}

// PopAll removes and returns every queued prompt (used for the Esc-interrupt
// merge, where all pending steers become a single turn).
func (q *PendingQueue) PopAll() []QueuedPrompt {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// Snapshot returns a copy of all queued prompts without consuming them, for
// mid-run steering injection on every model call.
func (q *PendingQueue) Snapshot() []QueuedPrompt {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueuedPrompt, len(q.items))
	copy(out, q.items)
	return out
}

// Len returns the number of queued prompts.
func (q *PendingQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// RunControl tracks the cancellation and interrupt state of the active agent
// run across the TUI goroutine (Esc / Ctrl+C) and the runAgentTask goroutine.
type RunControl struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	interrupted bool
}

// NewRunControl creates an empty RunControl.
func NewRunControl() *RunControl { return &RunControl{} }

// SetCancel installs (or clears, with nil) the cancel func of the active run.
func (rc *RunControl) SetCancel(c context.CancelFunc) {
	rc.mu.Lock()
	rc.cancel = c
	rc.mu.Unlock()
}

// Interrupt flags the run as user-interrupted and cancels it (no-op when no
// run is active). The flag survives so agentDoneMsg can merge queued prompts
// into a single turn (Codex semantics).
func (rc *RunControl) Interrupt() {
	rc.mu.Lock()
	rc.interrupted = true
	c := rc.cancel
	rc.mu.Unlock()
	if c != nil {
		c()
	}
}

// WasInterrupted reports whether the user requested an interrupt, without
// clearing it. Used by runAgentTask to suppress the noisy cancel error log.
func (rc *RunControl) WasInterrupted() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.interrupted
}

// ConsumeInterrupt reports and clears the interrupt flag. Called by the
// agentDoneMsg handler once the interrupted run has fully unwound.
func (rc *RunControl) ConsumeInterrupt() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	v := rc.interrupted
	rc.interrupted = false
	return v
}

// SteerInterjectionPrefix frames a queued message as a live mid-task
// instruction so the model treats it as the most recent user intent.
const SteerInterjectionPrefix = "USER INTERJECTION (while you were working):"

// SteerFraming wraps text in the interjection prefix for mid-run steering.
func SteerFraming(text string) string {
	if strings.TrimSpace(text) == "" {
		return SteerInterjectionPrefix
	}
	return SteerInterjectionPrefix + "\n" + text
}

// SteeringContent renders a queued prompt as a user-role content carrying the
// interjection framing plus any attachment parts (images as inline data).
func SteeringContent(q QueuedPrompt) *genai.Content {
	parts := []*genai.Part{genai.NewPartFromText(SteerFraming(q.Text))}
	for _, a := range q.Attach {
		if len(a.Data) == 0 {
			continue
		}
		if IsImageMIME(a.MIME) {
			parts = append(parts, genai.NewPartFromBytes(a.Data, a.MIME))
		} else {
			parts = append(parts, genai.NewPartFromText(string(a.Data)))
		}
	}
	return genai.NewContentFromParts(parts, genai.RoleUser)
}
