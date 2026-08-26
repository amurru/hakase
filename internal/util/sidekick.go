package util

import "sync"

// SidekickNote is a single advisory observation produced by the sidekick
// watchdog, delivered to the orchestrator and surfaced in the UI.
type SidekickNote struct {
	Severity string // "info" | "suggestion" | "warning" | "critical"
	Text     string
}

// SidekickNoteQueue is a concurrency-safe buffer of sidekick advisory notes
// awaiting injection into the next orchestrator model call. Written by the
// sidekick consultation (which may run on a background goroutine) and drained
// by the HistoryBuilder.BeforeModelCallback on the agent goroutine.
type SidekickNoteQueue struct {
	mu    sync.Mutex
	notes []SidekickNote
}

// NewSidekickNoteQueue creates an empty SidekickNoteQueue.
func NewSidekickNoteQueue() *SidekickNoteQueue { return &SidekickNoteQueue{} }

// Add appends a note to the queue.
func (q *SidekickNoteQueue) Add(n SidekickNote) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.notes = append(q.notes, n)
}

// Pending removes and returns every queued note (drained at injection time).
func (q *SidekickNoteQueue) Pending() []SidekickNote {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.notes
	q.notes = nil
	return out
}

// Len returns the number of queued notes.
func (q *SidekickNoteQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.notes)
}
