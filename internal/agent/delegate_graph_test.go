package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
)

// recordingNotifier captures EventNotifier callbacks for delegation-reporter
// assertions.
type recordingNotifier struct {
	graphEvents []interfaces.GraphEvent
	sessions    []string
}

func (n *recordingNotifier) TaskUpdate(action string, task interfaces.TaskMeta) {}

func (n *recordingNotifier) DelegationProgress(status, taskID, agent, message string) {}

func (n *recordingNotifier) CronJobEvent(status, jobID, name, summary, outputPath string) {}

func (n *recordingNotifier) SidekickEvent(sessionID, severity, text string) {}

func (n *recordingNotifier) EmitGraphEvent(sessionID string, ev interfaces.GraphEvent) {
	n.sessions = append(n.sessions, sessionID)
	n.graphEvents = append(n.graphEvents, ev)
}

// withGraphNotifier installs a fresh Runtime + recording notifier as the
// package-global rt, restoring the previous value at test end.
func withGraphNotifier(t *testing.T) *recordingNotifier {
	t.Helper()
	old := rt
	t.Cleanup(func() { rt = old })
	rt = &Runtime{}
	n := &recordingNotifier{}
	rt.SetEventNotifier(n)
	return n
}

func TestDelegationReporterGraphEvents(t *testing.T) {
	n := withGraphNotifier(t)

	r := newDelegationReporter("task_sub", "web_researcher", "sess-1", "task_root")
	r.started("find stuff")
	r.thought("pondering")
	r.toolCall("", "download", map[string]any{"url": "https://x"})
	r.toolResult("", "download", map[string]any{"status": "ok"})
	r.finish("completed", nil, "all done")

	if len(n.graphEvents) != 5 {
		t.Fatalf("expected 5 graph events, got %d: %+v", len(n.graphEvents), n.graphEvents)
	}
	for i, sid := range n.sessions {
		if sid != "sess-1" {
			t.Errorf("event %d: expected parent session scoping, got %q", i, sid)
		}
	}

	start, thought, tStart, tEnd, end := n.graphEvents[0], n.graphEvents[1], n.graphEvents[2], n.graphEvents[3], n.graphEvents[4]

	if start.Type != interfaces.GraphAgentStart || start.NodeID != "task_sub" ||
		start.ParentID != "task_root" || start.Agent != "web_researcher" || start.Goal != "find stuff" {
		t.Errorf("unexpected agent_start: %+v", start)
	}
	if thought.Type != interfaces.GraphAgentThought || thought.Text != "pondering" {
		t.Errorf("unexpected agent_thought: %+v", thought)
	}
	if tStart.Type != interfaces.GraphToolStart || tStart.CallID != "c1" || tStart.Tool != "download" {
		t.Errorf("unexpected tool_start: %+v", tStart)
	}
	if !strings.Contains(string(tStart.Args), "https://x") {
		t.Errorf("expected serialized args, got %q", string(tStart.Args))
	}
	if tEnd.Type != interfaces.GraphToolEnd || tEnd.CallID != "c1" || !tEnd.OK ||
		!strings.Contains(tEnd.Result, "status") {
		t.Errorf("unexpected tool_end: %+v", tEnd)
	}
	if end.Type != interfaces.GraphAgentEnd || end.Status != interfaces.GraphStatusCompleted ||
		end.Summary != "all done" || end.DurationMs < 0 {
		t.Errorf("unexpected agent_end: %+v", end)
	}
}

func TestDelegationReporterSessionlessDropsGraph(t *testing.T) {
	n := withGraphNotifier(t)

	r := newDelegationReporter("task_sub", "web_researcher", "", "")
	r.started("goal")
	r.finish("completed", nil, "done")

	if len(n.graphEvents) != 0 {
		t.Errorf("expected session-less graph events to be dropped, got %d", len(n.graphEvents))
	}
}

func TestDelegationReporterRepeatedToolPairing(t *testing.T) {
	n := withGraphNotifier(t)

	r := newDelegationReporter("task_sub", "general_purpose", "sess-1", "task_root")
	r.toolCall("", "search", nil) // c1
	r.toolCall("", "search", nil) // c2
	r.toolResult("", "search", nil)
	r.toolResult("", "search", nil)

	ends := []interfaces.GraphEvent{}
	for _, ev := range n.graphEvents {
		if ev.Type == interfaces.GraphToolEnd {
			ends = append(ends, ev)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("expected 2 tool_end events, got %d", len(ends))
	}
	// Reverse-order matching: most recent open call (c2) closes first.
	if ends[0].CallID != "c2" || ends[1].CallID != "c1" {
		t.Errorf("expected reverse-order pairing (c2, c1), got %s, %s", ends[0].CallID, ends[1].CallID)
	}
}

func TestDelegationReporterFinishFailureCarriesError(t *testing.T) {
	n := withGraphNotifier(t)

	r := newDelegationReporter("task_sub", "code_interpreter", "sess-1", "task_root")
	r.finish("failed", errBoom{}, "")

	end := n.graphEvents[len(n.graphEvents)-1]
	if end.Type != interfaces.GraphAgentEnd || end.Status != interfaces.GraphStatusFailed ||
		end.Error != "boom" {
		t.Errorf("unexpected failed agent_end: %+v", end)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
