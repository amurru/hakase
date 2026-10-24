package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/web/sse"
)

type fakePush struct {
	mu          sync.Mutex
	sessionIDs  []string // session ids seen on gate prompts
	approvals   []string // gate IDs
	clarifies   []string
	crons       [][3]string // producer session, status, name
	tasks       [][3]string // producer session, action, title
	delegations [][3]string // producer session, status, agent
}

func (f *fakePush) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionIDs = append(f.sessionIDs, sessionID)
	f.approvals = append(f.approvals, id)
}
func (f *fakePush) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clarifies = append(f.clarifies, id)
}
func (f *fakePush) CronEvent(producerSessionID, status, jobID, name, summary, outputPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crons = append(f.crons, [3]string{producerSessionID, status, name})
}
func (f *fakePush) TaskEvent(producerSessionID, action string, id, title, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, [3]string{producerSessionID, action, title})
}
func (f *fakePush) DelegationEvent(producerSessionID, status, taskID, agent, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delegations = append(f.delegations, [3]string{producerSessionID, status, agent})
}

func (f *fakePush) getSessionIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sessionIDs...)
}

func (f *fakePush) getApprovals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.approvals...)
}

func (f *fakePush) getClarifies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.clarifies...)
}

func (f *fakePush) getCrons() [][3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][3]string(nil), f.crons...)
}

func (f *fakePush) getTasks() [][3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][3]string(nil), f.tasks...)
}

func (f *fakePush) getDelegations() [][3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][3]string(nil), f.delegations...)
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
	bridge.SendCron("prod-cron-1", "job1p", "pbackup", "completed", "done", "outputs/x.md")
	bridge.SendCron("", "job2", "ticker", "started", "", "") // must be filtered
	bridge.SendTask("", map[string]any{"id": "t1", "title": "Write docs", "status": "completed"}, "completed")
	bridge.SendTask("prod-task-1", map[string]any{"id": "t1p", "title": "P docs", "status": "failed"}, "failed")
	bridge.SendTask("", map[string]any{"id": "t2", "title": "Noise", "status": "pending"}, "created") // filtered
	bridge.SendDelegation("", "d1", "researcher", "completed", "found it")
	bridge.SendDelegation("prod-deleg-1", "d1p", "researcher", "failed", "broke")
	bridge.SendDelegation("", "d2", "researcher", "thinking", "...") // filtered

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(push.getApprovals()) == 1 && len(push.getClarifies()) == 1 && len(push.getCrons()) == 2 &&
			len(push.getTasks()) == 2 && len(push.getDelegations()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	approvals := push.getApprovals()
	sessionIDs := push.getSessionIDs()
	clarifies := push.getClarifies()
	crons := push.getCrons()
	tasks := push.getTasks()
	delegations := push.getDelegations()

	if len(approvals) != 1 || approvals[0] != "appr_1" {
		t.Errorf("approvals = %v", approvals)
	}
	// The gate's session id must survive the router (topics-mode routing).
	if len(sessionIDs) != 1 || sessionIDs[0] != "sess_web" {
		t.Errorf("session ids = %v, want [sess_web]", sessionIDs)
	}
	if len(clarifies) != 1 || clarifies[0] != "clar_1" {
		t.Errorf("clarifies = %v", clarifies)
	}
	if len(crons) != 2 || crons[0] != [3]string{"", "completed", "backup"} {
		t.Errorf("crons = %v (verbose statuses must be filtered)", crons)
	}
	// The producer session survives the router (S8 producer gating).
	if crons[1] != [3]string{"prod-cron-1", "completed", "pbackup"} {
		t.Errorf("cron producer session lost: %v", crons)
	}
	if len(tasks) != 2 || tasks[0] != [3]string{"", "completed", "Write docs"} {
		t.Errorf("tasks = %v", tasks)
	}
	if tasks[1] != [3]string{"prod-task-1", "failed", "P docs"} {
		t.Errorf("task producer session lost: %v", tasks)
	}
	if len(delegations) != 2 || delegations[0] != [3]string{"", "completed", "researcher"} {
		t.Errorf("delegations = %v", delegations)
	}
	if delegations[1] != [3]string{"prod-deleg-1", "failed", "researcher"} {
		t.Errorf("delegation producer session lost: %v", delegations)
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
	if approvals := push.getApprovals(); len(approvals) != 0 {
		t.Errorf("malformed events dispatched: %v", approvals)
	}
}
