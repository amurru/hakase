package main

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// queuedPrompt is a message captured while the agent is busy. It is steered
// into the running session at the next model-call boundary (BeforeModelCallback)
// and drained into its own turn when the current run completes.
type queuedPrompt struct {
	text   string
	attach []attachment
}

// pendingQueue is the concurrency-safe FIFO of prompts typed while the agent
// is processing. Written by the TUI goroutine (Enter while busy), read by the
// HistoryBuilder callback (agent goroutine) for mid-run steering, and drained
// by the agentDoneMsg handler (TUI goroutine) at run end.
type pendingQueue struct {
	mu    sync.Mutex
	items []queuedPrompt
}

func newPendingQueue() *pendingQueue { return &pendingQueue{} }

// push appends a prompt to the back of the queue.
func (q *pendingQueue) push(p queuedPrompt) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, p)
}

// pop removes and returns the oldest prompt (ok=false when empty).
func (q *pendingQueue) pop() (queuedPrompt, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return queuedPrompt{}, false
	}
	p := q.items[0]
	q.items = q.items[1:]
	return p, true
}

// popAll removes and returns every queued prompt (used for the Esc-interrupt
// merge, where all pending steers become a single turn).
func (q *pendingQueue) popAll() []queuedPrompt {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// snapshot returns a copy of all queued prompts without consuming them, for
// mid-run steering injection on every model call.
func (q *pendingQueue) snapshot() []queuedPrompt {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queuedPrompt, len(q.items))
	copy(out, q.items)
	return out
}

func (q *pendingQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// runControl tracks the cancellation and interrupt state of the active agent
// run across the TUI goroutine (Esc / Ctrl+C) and the runAgentTask goroutine.
type runControl struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	interrupted bool
}

func newRunControl() *runControl { return &runControl{} }

// setCancel installs (or clears, with nil) the cancel func of the active run.
func (rc *runControl) setCancel(c context.CancelFunc) {
	rc.mu.Lock()
	rc.cancel = c
	rc.mu.Unlock()
}

// interrupt flags the run as user-interrupted and cancels it (no-op when no
// run is active). The flag survives so agentDoneMsg can merge queued prompts
// into a single turn (Codex semantics).
func (rc *runControl) interrupt() {
	rc.mu.Lock()
	rc.interrupted = true
	c := rc.cancel
	rc.mu.Unlock()
	if c != nil {
		c()
	}
}

// wasInterrupted reports whether the user requested an interrupt, without
// clearing it. Used by runAgentTask to suppress the noisy cancel error log.
func (rc *runControl) wasInterrupted() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.interrupted
}

// consumeInterrupt reports and clears the interrupt flag. Called by the
// agentDoneMsg handler once the interrupted run has fully unwound.
func (rc *runControl) consumeInterrupt() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	v := rc.interrupted
	rc.interrupted = false
	return v
}

// steerInterjectionPrefix frames a queued message as a live mid-task
// instruction so the model treats it as the most recent user intent.
const steerInterjectionPrefix = "USER INTERJECTION (while you were working):"

func steerFraming(text string) string {
	if strings.TrimSpace(text) == "" {
		return steerInterjectionPrefix
	}
	return steerInterjectionPrefix + "\n" + text
}

// steeringContent renders a queued prompt as a user-role content carrying the
// interjection framing plus any attachment parts (images as inline data).
func steeringContent(q queuedPrompt) *genai.Content {
	parts := []*genai.Part{genai.NewPartFromText(steerFraming(q.text))}
	for _, a := range q.attach {
		if len(a.Data) == 0 {
			continue
		}
		if imageMimes[a.MIME] {
			parts = append(parts, genai.NewPartFromBytes(a.Data, a.MIME))
		} else {
			parts = append(parts, genai.NewPartFromText(string(a.Data)))
		}
	}
	return genai.NewContentFromParts(parts, genai.RoleUser)
}
