package agentrun

import (
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"

	"google.golang.org/adk/v2/session"
)

// recordingSink captures EventSink callbacks for canvas assertions.
type recordingSink struct {
	logs   []string
	events []interfaces.GraphEvent
	done   bool
}

func (s *recordingSink) OnStream(sessionID, content, thinking string) {}

func (s *recordingSink) OnLog(sessionID, line string) { s.logs = append(s.logs, line) }

func (s *recordingSink) OnUsage(sessionID string, tokens, percent int) {}

func (s *recordingSink) OnDone(sessionID string) { s.done = true }

func (s *recordingSink) OnGraphEvent(sessionID string, ev interfaces.GraphEvent) {
	if sessionID != "sess-1" {
		s.logs = append(s.logs, "wrong session "+sessionID)
	}
	s.events = append(s.events, ev)
}

func newTestTracker(t *testing.T, sink *recordingSink) *graphTracker {
	t.Helper()
	g := newGraphTracker("sess-1", sink, "task_root")
	g.agentStart("do the thing")
	return g
}

func TestGraphTrackerRootLifecycle(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after agentStart, got %d", len(sink.events))
	}
	start := sink.events[0]
	if start.Type != interfaces.GraphAgentStart || start.NodeID != "task_root" ||
		start.Agent != rootAgentLabel || start.Goal != "do the thing" {
		t.Errorf("unexpected agent_start: %+v", start)
	}

	g.agentEnd("final answer", "")
	if !g.ended {
		t.Error("expected ended=true after agentEnd")
	}
	end := sink.events[len(sink.events)-1]
	if end.Type != interfaces.GraphAgentEnd || end.NodeID != "task_root" ||
		end.Status != interfaces.GraphStatusCompleted || end.Summary != "final answer" {
		t.Errorf("unexpected agent_end: %+v", end)
	}
}

func TestGraphTrackerFailureStatus(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)
	g.runStatus = interfaces.GraphStatusFailed
	g.runError = "boom"
	g.agentEnd("", "boom")

	end := sink.events[len(sink.events)-1]
	if end.Status != interfaces.GraphStatusFailed || end.Error != "boom" {
		t.Errorf("expected failed status with error, got %+v", end)
	}
}

func TestGraphTrackerToolPairing(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)

	g.toolStart("prov-1", "download", map[string]any{"url": "https://x"})
	g.toolEnd("prov-1", "download", map[string]any{"status": "ok"})

	start, end := sink.events[1], sink.events[2]
	if start.Type != interfaces.GraphToolStart || start.CallID != "prov-1" || start.Tool != "download" {
		t.Errorf("unexpected tool_start: %+v", start)
	}
	if len(start.Args) == 0 || !strings.Contains(string(start.Args), "https://x") {
		t.Errorf("expected serialized args, got %q", string(start.Args))
	}
	if end.Type != interfaces.GraphToolEnd || end.CallID != "prov-1" || !end.OK {
		t.Errorf("unexpected tool_end: %+v", end)
	}
	if !strings.Contains(end.Result, "ok") {
		t.Errorf("expected serialized result, got %q", end.Result)
	}
	if end.DurationMs < 0 {
		t.Errorf("expected non-negative duration, got %d", end.DurationMs)
	}
}

func TestGraphTrackerSynthesizedCallIDsAndNameFallback(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)

	g.toolStart("", "search", nil) // c1
	g.toolStart("", "search", nil) // c2
	g.toolEnd("", "search", nil)   // must close c2 (most recent open)
	g.toolEnd("", "search", nil)   // must close c1

	// events: agent_start, tool_start c1, tool_start c2, tool_end, tool_end.
	ids := []string{sink.events[3].CallID, sink.events[4].CallID}
	if ids[0] != "c2" || ids[1] != "c1" {
		t.Errorf("expected reverse-order name matching (c2, c1), got %v", ids)
	}
	if len(g.openOrder) != 0 {
		t.Errorf("expected no open calls left, got %v", g.openOrder)
	}

	// Response with no matching open call: still emitted with a fresh id.
	g.toolEnd("prov-9", "download", nil)
	last := sink.events[len(sink.events)-1]
	if last.CallID == "" || last.Type != interfaces.GraphToolEnd {
		t.Errorf("expected synthesized tool_end, got %+v", last)
	}
}

func TestGraphTrackerFallbackMatchesToolName(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)

	// Two different tools in flight (parallel calls); responses arrive in
	// reverse opening order.
	g.toolStart("", "search", nil)   // c1
	g.toolStart("", "download", nil) // c2
	g.toolEnd("", "download", nil)   // must close c2, not c1
	g.toolEnd("", "search", nil)     // must close c1

	// events: agent_start, tool_start c1, tool_start c2, tool_end, tool_end.
	if sink.events[3].Tool != "download" || sink.events[4].Tool != "search" {
		t.Errorf("expected name-aware matching, got %s then %s", sink.events[3].Tool, sink.events[4].Tool)
	}
	if sink.events[3].CallID != "c2" || sink.events[4].CallID != "c1" {
		t.Errorf("expected calls c2 then c1, got %s then %s", sink.events[3].CallID, sink.events[4].CallID)
	}
}

func TestGraphTrackerToolErrorDetection(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)
	g.toolStart("p1", "system_exec", nil)
	g.toolEnd("p1", "system_exec", map[string]any{"error": "exit code 1"})

	end := sink.events[len(sink.events)-1]
	if end.OK || end.Error != "exit code 1" {
		t.Errorf("expected OK=false with error detail, got %+v", end)
	}
}

func TestGraphTrackerTransfer(t *testing.T) {
	sink := &recordingSink{}
	g := newTestTracker(t, sink)

	g.observeEvent(&session.Event{Author: "orchestrator"})
	g.observeEvent(&session.Event{Author: "orchestrator", Actions: session.EventActions{TransferToAgent: "web_researcher"}})

	// transfer announcement + agent_start for the target node.
	transfer, sub := sink.events[1], sink.events[2]
	if transfer.Type != interfaces.GraphTransfer || transfer.Target != "web_researcher" {
		t.Errorf("unexpected transfer: %+v", transfer)
	}
	if sub.Type != interfaces.GraphAgentStart || sub.NodeID != "transfer:web_researcher:1" || sub.ParentID != "task_root" {
		t.Errorf("unexpected transferred agent_start: %+v", sub)
	}

	// Tool activity after the transfer attributes to the transferred node.
	g.toolStart("t1", "download", nil)
	if got := sink.events[len(sink.events)-1].NodeID; got != "transfer:web_researcher:1" {
		t.Errorf("expected attribution to transferred node, got %q", got)
	}

	// Transfer back to the root author closes the transferred node.
	g.observeEvent(&session.Event{Author: "orchestrator"})
	closeEv := sink.events[len(sink.events)-1]
	if closeEv.Type != interfaces.GraphAgentEnd || closeEv.NodeID != "transfer:web_researcher:1" {
		t.Errorf("expected transfer node agent_end on transfer-back, got %+v", closeEv)
	}

	g.toolStart("t2", "read_file", nil)
	if got := sink.events[len(sink.events)-1].NodeID; got != "task_root" {
		t.Errorf("expected attribution back on root, got %q", got)
	}

	// A second transfer to the same target gets a fresh node id.
	g.observeEvent(&session.Event{Author: "orchestrator", Actions: session.EventActions{TransferToAgent: "web_researcher"}})
	sub2 := sink.events[len(sink.events)-1]
	if sub2.NodeID != "transfer:web_researcher:2" || sub2.ParentID != "task_root" {
		t.Errorf("expected unique second transfer node, got %+v", sub2)
	}
}

func TestGraphArgsTruncation(t *testing.T) {
	big := map[string]any{"blob": strings.Repeat("x", interfaces.GraphArgsCap*3)}
	args := interfaces.GraphArgs(big)
	if len(args) > interfaces.GraphArgsCap+128 {
		t.Errorf("expected truncated args, got %d bytes", len(args))
	}
	if !strings.Contains(string(args), "_truncated") {
		t.Errorf("expected truncation marker, got %q", string(args)[:min(80, len(args))])
	}
	if interfaces.GraphArgs(nil) != nil {
		t.Error("expected nil args for empty map")
	}
}
