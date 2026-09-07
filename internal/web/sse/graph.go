package sse

import (
	"encoding/json"
	"sync"
	"time"

	"amurru/hakase/internal/interfaces"
)

// The canvas ring retains the most recent graph events per session so a
// browser that (re)connects mid-run or after a reload can rebuild the canvas
// via the backfill endpoint instead of starting from an empty graph. Retention
// is in-memory only: events vanish on server restart, and sessions nobody
// published to never allocate a log.

const (
	// graphEventCap bounds one session's retained canvas events. Oldest
	// events drop first; the live SSE fan-out is unaffected (publish is
	// fire-and-forget).
	graphEventCap = 500
	// graphSessionCap bounds how many session logs coexist. Eviction is FIFO
	// by log creation; an evicted session simply starts a fresh log on its
	// next event.
	graphSessionCap = 64
)

// graphFrame is the wire form of a canvas event: the GraphEvent plus the
// per-session sequence number and wall-clock timestamp the bridge assigns.
// Seq orders backfill replay and lets clients dedupe against live events.
type graphFrame struct {
	Seq int64 `json:"seq"`
	Ts  int64 `json:"ts"` // unix milliseconds
	interfaces.GraphEvent
}

// graphLog is one session's retained canvas history.
type graphLog struct {
	mu     sync.Mutex
	seq    int64
	events [][]byte // pre-marshaled frames, seq order
}

// append assigns the next seq/timestamp, retains the frame, and returns the
// payload for live publication.
func (g *graphLog) append(ev interfaces.GraphEvent) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	payload, err := json.Marshal(graphFrame{Seq: g.seq, Ts: time.Now().UnixMilli(), GraphEvent: ev})
	if err != nil {
		return nil
	}
	g.events = append(g.events, payload)
	if drop := len(g.events) - graphEventCap; drop > 0 {
		g.events = g.events[drop:]
	}
	return payload
}

// snapshot returns the retained frames in seq order.
func (g *graphLog) snapshot() []json.RawMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]json.RawMessage, len(g.events))
	for i, p := range g.events {
		out[i] = json.RawMessage(p)
	}
	return out
}

// SendGraph retains a canvas event in the session's ring and publishes it to
// the session's SSE subscribers as event "graph". Sessions with an empty id
// (session-less surfaces: TUI, CLI) are dropped - the canvas is
// session-scoped, and those surfaces have no session topic to publish under.
func (b *EventBridge) SendGraph(sessionID string, ev interfaces.GraphEvent) {
	if sessionID == "" {
		return
	}

	b.mu.Lock()
	gl, ok := b.graphLogs[sessionID]
	if !ok {
		if len(b.graphLogs) >= graphSessionCap {
			b.evictOldestGraphLogLocked()
		}
		gl = &graphLog{}
		b.graphLogs[sessionID] = gl
		b.graphLogOrder = append(b.graphLogOrder, sessionID)
	}
	b.mu.Unlock()

	payload := gl.append(ev)
	if payload == nil {
		return
	}
	b.publish(sessionID, "graph", payload)
}

// evictOldestGraphLogLocked drops the oldest session log. Caller holds b.mu.
func (b *EventBridge) evictOldestGraphLogLocked() {
	for len(b.graphLogOrder) > 0 {
		oldest := b.graphLogOrder[0]
		b.graphLogOrder = b.graphLogOrder[1:]
		if _, ok := b.graphLogs[oldest]; ok {
			delete(b.graphLogs, oldest)
			return
		}
	}
}

// GraphBackfill returns the session's retained canvas events in seq order,
// for the REST backfill endpoint. Returns nil when the session has no
// retained history.
func (b *EventBridge) GraphBackfill(sessionID string) []json.RawMessage {
	b.mu.RLock()
	gl := b.graphLogs[sessionID]
	b.mu.RUnlock()
	if gl == nil {
		return nil
	}
	return gl.snapshot()
}

// DropGraphLog discards a session's retained canvas history (session delete).
func (b *EventBridge) DropGraphLog(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.graphLogs, sessionID)
	for i, id := range b.graphLogOrder {
		if id == sessionID {
			b.graphLogOrder = append(b.graphLogOrder[:i], b.graphLogOrder[i+1:]...)
			break
		}
	}
}

// EmitGraphEvent implements interfaces.EventNotifier: delegation-sourced
// canvas events funnel through the same SendGraph path as driver-sourced ones.
func (b *EventBridge) EmitGraphEvent(sessionID string, ev interfaces.GraphEvent) {
	b.SendGraph(sessionID, ev)
}
