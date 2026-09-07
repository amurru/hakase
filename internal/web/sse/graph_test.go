package sse

import (
	"encoding/json"
	"fmt"
	"testing"

	"amurru/hakase/internal/interfaces"
)

func TestSendGraphPublishesAndRetains(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendGraph("sess-1", interfaces.GraphEvent{
		Type:   interfaces.GraphAgentStart,
		NodeID: "task_root",
		Agent:  "orchestrator",
		Goal:   "research",
	})

	select {
	case data := <-ch:
		validateSSE(t, data, "graph", func(payload map[string]any) {
			if payload["seq"] != float64(1) {
				t.Errorf("expected seq=1, got %v", payload["seq"])
			}
			if payload["ts"] == float64(0) {
				t.Error("expected non-zero ts")
			}
			if payload["type"] != interfaces.GraphAgentStart || payload["node_id"] != "task_root" {
				t.Errorf("unexpected embedded event: %v", payload)
			}
		})
	default:
		t.Fatal("expected event on channel")
	}

	backfill := b.GraphBackfill("sess-1")
	if len(backfill) != 1 {
		t.Fatalf("expected 1 retained event, got %d", len(backfill))
	}
	var frame graphFrame
	if err := json.Unmarshal(backfill[0], &frame); err != nil {
		t.Fatalf("backfill payload invalid: %v", err)
	}
	if frame.NodeID != "task_root" || frame.Seq != 1 {
		t.Errorf("unexpected backfill event: %+v", frame)
	}
}

func TestSendGraphEmptySessionDropped(t *testing.T) {
	b := NewEventBridge()
	b.SendGraph("", interfaces.GraphEvent{Type: interfaces.GraphAgentStart, NodeID: "n"})
	if got := b.GraphBackfill(""); got != nil {
		t.Errorf("expected no retention for empty session, got %v", got)
	}
}

func TestGraphBackfillSeqOrderAndCap(t *testing.T) {
	b := NewEventBridge()
	total := graphEventCap + 10
	for i := 0; i < total; i++ {
		b.SendGraph("sess-1", interfaces.GraphEvent{
			Type:   interfaces.GraphAgentText,
			NodeID: "n1",
			Text:   fmt.Sprintf("chunk-%d", i),
		})
	}

	frames := b.GraphBackfill("sess-1")
	if len(frames) != graphEventCap {
		t.Fatalf("expected ring capped at %d, got %d", graphEventCap, len(frames))
	}
	var first, last graphFrame
	if err := json.Unmarshal(frames[0], &first); err != nil {
		t.Fatalf("bad frame: %v", err)
	}
	if err := json.Unmarshal(frames[len(frames)-1], &last); err != nil {
		t.Fatalf("bad frame: %v", err)
	}
	if first.Text != "chunk-10" || first.Seq != 11 {
		t.Errorf("expected oldest retained event chunk-10 (seq 11), got %q seq %d", first.Text, first.Seq)
	}
	if last.Text != fmt.Sprintf("chunk-%d", total-1) || last.Seq != int64(total) {
		t.Errorf("expected newest retained event chunk-%d (seq %d), got %q seq %d", total-1, total, last.Text, last.Seq)
	}
}

func TestDropGraphLog(t *testing.T) {
	b := NewEventBridge()
	b.SendGraph("sess-1", interfaces.GraphEvent{Type: interfaces.GraphAgentStart, NodeID: "n"})
	b.DropGraphLog("sess-1")
	if got := b.GraphBackfill("sess-1"); got != nil {
		t.Errorf("expected dropped log, got %d events", len(got))
	}
	// Drop of an unknown session must not panic.
	b.DropGraphLog("never-existed")
}

func TestGraphSessionCapEviction(t *testing.T) {
	b := NewEventBridge()
	for i := 0; i <= graphSessionCap; i++ {
		id := fmt.Sprintf("sess-%02d", i)
		b.SendGraph(id, interfaces.GraphEvent{Type: interfaces.GraphAgentStart, NodeID: "n"})
	}
	if got := b.GraphBackfill("sess-00"); got != nil {
		t.Errorf("expected oldest session log evicted, got %d events", len(got))
	}
	if got := b.GraphBackfill(fmt.Sprintf("sess-%02d", graphSessionCap)); len(got) != 1 {
		t.Errorf("expected newest session log retained, got %d events", len(got))
	}
}

func TestEmitGraphEventRoutesToSendGraph(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.EmitGraphEvent("sess-1", interfaces.GraphEvent{Type: interfaces.GraphAgentEnd, NodeID: "n"})
	select {
	case data := <-ch:
		validateSSE(t, data, "graph", func(payload map[string]any) {
			if payload["type"] != interfaces.GraphAgentEnd {
				t.Errorf("expected agent_end, got %v", payload["type"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}
