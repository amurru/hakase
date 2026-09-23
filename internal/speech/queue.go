// queue.go - serialized transcription (docs/telegram-voice/spec.md TV-001).
// ASR is CPU-heavy: one transcription at a time per process, a bounded wait
// queue, and ErrBusy when the queue is full so the transport can say "try
// again in a moment" instead of piling up work.
package speech

import (
	"context"
	"time"
)

// Queue serializes Transcribe calls.
type Queue struct {
	// slots bounds how many callers may WAIT (queue depth).
	slots chan struct{}
	// worker is the single in-flight transcription slot.
	worker chan struct{}
}

// NewQueue creates a queue with the given wait depth (minimum 1).
func NewQueue(depth int) *Queue {
	if depth < 1 {
		depth = 1
	}
	return &Queue{
		slots:  make(chan struct{}, depth),
		worker: make(chan struct{}, 1),
	}
}

// Run executes fn once a worker is free, or returns ErrBusy immediately
// when the wait queue is full. ctx bounds both the wait and the run.
func (q *Queue) Run(ctx context.Context, fn func(context.Context) error) error {
	select {
	case q.slots <- struct{}{}:
	default:
		return ErrBusy
	}
	defer func() { <-q.slots }()

	select {
	case q.worker <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-q.worker }()

	return fn(ctx)
}

// WithTimeout wraps ctx with the transcription timeout (0 = no wrapper).
func WithTimeout(ctx context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if timeoutSeconds <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
}
