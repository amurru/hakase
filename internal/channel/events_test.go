package channel

import (
	"context"
	"testing"
	"time"

	"amurru/hakase/internal/web/sse"
)

type fakePush struct {
	sessionIDs  []string // session ids seen on gate prompts
	approvals   []string // gate IDs
	clarifies   []string
	crons       [][2]string // status, name
	tasks       [][2]string // action, title
	delegations [][2]string // status, agent
}

func (f *fakePush) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	f.sessionIDs = append(f.sessionIDs, sessionID)
	f.approvals = append(f.approvals, id)
}
func (f *fakePush) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	f.clarifies = append(f.clarifies, id)
}
func (f *fakePush) CronEvent(status, jobID, name, summary, outputPath string) {
	f.crons = append(f.crons, [2]string{status, name})
}
func (f *fakePush) TaskEvent(action string, id, title, status string) {
	f.tasks = append(f.tasks, [2]string{action, title})
}
func (f *fakePush) DelegationEvent(status, taskID, agent, message string) {
	f.delegations = append(f.delegations, [2]string{status, agent})
}

func TestRouterDispatchesGateAndLifecycleEvents(t *testing.T) {
	bridge := sse.NewEventBridge()
	push := &fakePush{}
	router := NewRouter(bridge, []PushHandler{push}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Give the router a moment to subscribe before publishing; the assertions
	// below also poll with their own deadline, so a slow subscribe cannot
	// flake the test.
	time.Sleep(50 * time.Millisecond)

	bridge.SendApprovalPrompt("", "sess_web", "appr_1", "system_exec", "high", "because", "rm -rf")
	bridge.SendClarifyPrompt("", "", "clar_1", "Which?", []string{"A", "B"}, false)
	bridge.SendCron("", "job1", "backup", "completed", "done", "outputs/x.md")
	bridge.SendCron("", "job2", "ticker", "started", "", "") // must be filtered
	bridge.SendTask("", map[string]any{"id": "t1", "title": "Write docs", "status": "completed"}, "completed")
	bridge.SendTask("", map[string]any{"id": "t2", "title": "Noise", "status": "pending"}, "created") // filtered
	bridge.SendDelegation("", "d1", "researcher", "completed", "found it")
	bridge.SendDelegation("", "d2", "researcher", "thinking", "...") // filtered

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(push.approvals) == 1 && len(push.clarifies) == 1 && len(push.crons) == 1 &&
			len(push.tasks) == 1 && len(push.delegations) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	if len(push.approvals) != 1 || push.approvals[0] != "appr_1" {
		t.Errorf("approvals = %v", push.approvals)
	}
	// The gate's session id must survive the router (topics-mode routing).
	if len(push.sessionIDs) != 1 || push.sessionIDs[0] != "sess_web" {
		t.Errorf("session ids = %v, want [sess_web]", push.sessionIDs)
	}
	if len(push.clarifies) != 1 || push.clarifies[0] != "clar_1" {
		t.Errorf("clarifies = %v", push.clarifies)
	}
	if len(push.crons) != 1 || push.crons[0] != [2]string{"completed", "backup"} {
		t.Errorf("crons = %v (verbose statuses must be filtered)", push.crons)
	}
	if len(push.tasks) != 1 || push.tasks[0] != [2]string{"completed", "Write docs"} {
		t.Errorf("tasks = %v", push.tasks)
	}
	if len(push.delegations) != 1 || push.delegations[0] != [2]string{"completed", "researcher"} {
		t.Errorf("delegations = %v", push.delegations)
	}
}

func TestRouterSurvivesMalformedPayloads(t *testing.T) {
	bridge := sse.NewEventBridge()
	push := &fakePush{}
	router := NewRouter(bridge, []PushHandler{push}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	bridge.SendApprovalPrompt("", "", "", "", "", "", "") // empty ID ignored
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("router did not stop")
	}
	if len(push.approvals) != 0 {
		t.Errorf("malformed events dispatched: %v", push.approvals)
	}
}
