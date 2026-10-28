// delegate_workflow_test.go - T1.3 workflow pilot, T1.4 workflow label
// contract, and T1.5 session-state copy semantics (AD-002, docs/
// adk-adoption/spec.md security considerations 2026-10-28).
package agent

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

func TestDelegateHandlerWorkflowPilot(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{
		{text: "1. read the file 2. patch it"}, // planner
		{text: "applied the patch"},            // executor
		{text: "work checked, VERIFIED"},       // verifier
	}}
	saved := deps
	deps = &Deps{Model: mdl, WorkflowDelegation: true}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("workflow pilot probe")
	clearDelegCache(t, norm)

	res, err := delegateTaskHandler(newDelegRunCtx("task-wf-pilot"), DelegateTaskArgs{Goal: "workflow pilot probe", AgentName: "general_purpose"})
	if err != nil {
		t.Fatalf("delegateTaskHandler: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want completed (error: %q)", res.Status, res.Error)
	}
	for _, want := range []string{"1. read the file", "applied the patch", "VERIFIED"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("Summary %q missing stage text %q", res.Summary, want)
		}
	}
	if len(mdl.requests) != 3 {
		t.Errorf("model turns = %d, want 3 (plan, execute, verify)", len(mdl.requests))
	}
	if _, ok := delegationCacheGet(norm); !ok {
		t.Error("result not cached after workflow completion")
	}
}

func TestDelegateHandlerWorkflowOffByDefault(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{{text: "single agent did it"}}}
	saved := deps
	deps = &Deps{Model: mdl}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("workflow off probe")
	clearDelegCache(t, norm)

	res, err := delegateTaskHandler(newDelegRunCtx("task-wf-off"), DelegateTaskArgs{Goal: "workflow off probe", AgentName: "general_purpose"})
	if err != nil {
		t.Fatalf("delegateTaskHandler: %v", err)
	}
	if res.Status != "completed" || len(mdl.requests) != 1 {
		t.Errorf("Status = %q turns = %d, want completed/1 (pilot must be opt-in)", res.Status, len(mdl.requests))
	}
}

// probeLabelTool returns a tool whose handler resolves the flow label the
// way gates do and delivers it over ch.
func probeLabelTool(t *testing.T, ch chan<- *permissions.SessionLabel) tool.Tool {
	t.Helper()
	probe, err := util.NewDocTool(functiontool.Config{
		Name:        "probe_label",
		Description: "Resolves and reports the flow label",
	}, func(ctx agent.Context, input struct{}) (map[string]any, error) {
		ch <- flowLabelForCtx(ctx)
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	return probe
}

// TestWorkflowLabelContractInRunner pins the in-runner half of the AD-002
// label contract: workflow children run in the same invocation context, so
// a narrowed registry label resolves for child tools exactly as it does for
// the parent.
func TestWorkflowLabelContractInRunner(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow"})
	sess := "sess-wf-label"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-wf-label", "h")

	ch := make(chan *permissions.SessionLabel, 1)
	mdl := &delegMockModel{entries: []delegMockEntry{
		{call: &genai.FunctionCall{Name: "probe_label", Args: map[string]any{}}},
		{text: "probed"},
	}}
	child, err := llmagent.New(llmagent.Config{
		Name: "wf_probe_child", Model: mdl, Instruction: "call probe_label",
		Tools: []tool.Tool{probeLabelTool(t, ch)},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	wf, err := sequentialagent.New(sequentialagent.Config{
		AgentConfig: agent.Config{Name: "wf_probe", SubAgents: []agent.Agent{child}},
	})
	if err != nil {
		t.Fatalf("sequentialagent.New: %v", err)
	}

	svc := session.InMemoryService()
	if _, err := svc.Create(context.Background(), &session.CreateRequest{
		AppName: "wf_label_test", UserID: "user-wf", SessionID: "adk-wf-label",
	}); err != nil {
		t.Fatalf("session create: %v", err)
	}
	interfaces.RegisterTaskSession("adk-wf-label", sess)
	t.Cleanup(func() { interfaces.UnregisterTask("adk-wf-label") })

	r, err := runner.New(runner.Config{
		AppName: "wf_label_test", Agent: wf, SessionService: svc, AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	for _, evErr := range r.Run(context.Background(), "user-wf", "adk-wf-label", genai.NewContentFromText("probe", genai.RoleUser), agent.RunConfig{}) {
		if evErr != nil {
			t.Fatalf("workflow run: %v", evErr)
		}
	}

	got := <-ch
	if got == nil {
		t.Fatal("child resolved nil label, want the narrowed registry label")
	}
	want := hakasesession.DefaultRegistry.Get(sess)
	if want == nil || got.Trust != want.Trust || got.Audience != want.Audience {
		t.Errorf("child label = %+v, want narrowed %+v", got, want)
	}
}

// TestWorkflowLabelContractAgenttool pins the out-of-runner half: an
// explicitly seeded delegate flow state reaches workflow children through
// agenttool, and a planted-but-unregistered route stays fail-closed (nil,
// evaluated as narrowest under v2) instead of widening to the start label.
func TestWorkflowLabelContractAgenttool(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow"})
	ch := make(chan *permissions.SessionLabel, 1)
	newChild := func(t *testing.T) (agent.Agent, *delegMockModel) {
		t.Helper()
		mdl := &delegMockModel{entries: []delegMockEntry{
			{call: &genai.FunctionCall{Name: "probe_label", Args: map[string]any{}}},
			{text: "probed"},
		}}
		child, err := llmagent.New(llmagent.Config{
			Name: "wf_seed_child", Model: mdl, Instruction: "call probe_label",
			Tools: []tool.Tool{probeLabelTool(t, ch)},
		})
		if err != nil {
			t.Fatalf("llmagent.New: %v", err)
		}
		return child, mdl
	}

	t.Run("explicit seed honored", func(t *testing.T) {
		child, _ := newChild(t)
		wf, err := sequentialagent.New(sequentialagent.Config{
			AgentConfig: agent.Config{Name: "wf_seed", SubAgents: []agent.Agent{child}},
		})
		if err != nil {
			t.Fatalf("sequentialagent.New: %v", err)
		}
		run, ok := agenttool.New(wf, nil).(agentToolRunner)
		if !ok {
			t.Fatal("agenttool does not satisfy agentToolRunner")
		}
		seed := permissions.StartLabel()
		plain := WithDelegateFlowState(context.Background(), &seed)
		actx := &cancelableAgentCtx{Context: newDelegRunCtx("task-wf-seed"), base: plain}
		if _, err := run.Run(actx, map[string]any{"request": "probe"}); err != nil {
			t.Fatalf("agenttool Run: %v", err)
		}
		got := <-ch
		if got == nil {
			t.Fatal("child resolved nil label, want the explicit seed")
		}
		want := permissions.StartLabel()
		if got.Trust != want.Trust || got.Audience != want.Audience {
			t.Errorf("child label = %+v, want seeded %+v", got, want)
		}
	})

	t.Run("unregistered route stays fail-closed", func(t *testing.T) {
		child, _ := newChild(t)
		wf, err := sequentialagent.New(sequentialagent.Config{
			AgentConfig: agent.Config{Name: "wf_unreg", SubAgents: []agent.Agent{child}},
		})
		if err != nil {
			t.Fatalf("sequentialagent.New: %v", err)
		}
		run, ok := agenttool.New(wf, nil).(agentToolRunner)
		if !ok {
			t.Fatal("agenttool does not satisfy agentToolRunner")
		}
		plain := interfaces.WithDelegationRoute(context.Background(), "sess-unregistered-wf", "task-wf-unreg")
		actx := &cancelableAgentCtx{Context: newDelegRunCtx("task-wf-unreg"), base: plain}
		if _, err := run.Run(actx, map[string]any{"request": "probe"}); err != nil {
			t.Fatalf("agenttool Run: %v", err)
		}
		if got := <-ch; got != nil {
			t.Errorf("child label = %+v, want nil (fail-closed narrowest under v2)", got)
		}
	})
}

// TestAgentToolStateCopySemantics pins T1.5: agenttool copies the parent
// session state into the child session minus _adk keys. Today nothing in
// hakase writes ADK session state (second subtest); any future writer must
// re-check this path per the spec's security considerations.
func TestAgentToolStateCopySemantics(t *testing.T) {
	ch := make(chan map[string]any, 1)
	newMock := func() *delegMockModel {
		return &delegMockModel{entries: []delegMockEntry{
			{call: &genai.FunctionCall{Name: "probe_state", Args: map[string]any{}}},
			{text: "probed"},
		}}
	}
	probe, err := util.NewDocTool(functiontool.Config{
		Name: "probe_state", Description: "Reports child session state",
	}, func(ctx agent.Context, input struct{}) (map[string]any, error) {
		seen := map[string]any{}
		for k, v := range ctx.State().All() {
			seen[k] = v
		}
		ch <- seen
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	newRun := func(t *testing.T) agentToolRunner {
		t.Helper()
		child, err := llmagent.New(llmagent.Config{
			Name: "wf_state_child", Model: newMock(), Instruction: "call probe_state",
			Tools: []tool.Tool{probe},
		})
		if err != nil {
			t.Fatalf("llmagent.New: %v", err)
		}
		run, ok := agenttool.New(child, nil).(agentToolRunner)
		if !ok {
			t.Fatal("agenttool does not satisfy agentToolRunner")
		}
		return run
	}

	t.Run("planted keys propagate minus adk", func(t *testing.T) {
		run := newRun(t)
		parent := newDelegRunStateCtx("task-state-copy")
		parent.state = mapState{"hakase_marker": "v", "_adk_internal": "x"}
		if _, err := run.Run(parent, map[string]any{"request": "probe"}); err != nil {
			t.Fatalf("agenttool Run: %v", err)
		}
		seen := <-ch
		if seen["hakase_marker"] != "v" {
			t.Errorf("planted parent state did not reach the child: %v", seen)
		}
		if _, leak := seen["_adk_internal"]; leak {
			t.Errorf("_adk key leaked into child state: %v", seen)
		}
	})

	t.Run("no hakase writer today", func(t *testing.T) {
		run := newRun(t)
		parent := newDelegRunStateCtx("task-state-clean")
		if _, err := run.Run(parent, map[string]any{"request": "probe"}); err != nil {
			t.Fatalf("agenttool Run: %v", err)
		}
		if seen := <-ch; len(seen) != 0 {
			t.Errorf("child inherited unexpected state %v; a hakase session-state writer appeared - re-check the copy path (spec T1.5)", seen)
		}
	})
}

// delegRunStateCtx is delegRunCtx with a settable state map.
type delegRunStateCtx struct {
	agent.StrictContextMock
	taskID string
	state  mapState
}

func newDelegRunStateCtx(taskID string) *delegRunStateCtx {
	return &delegRunStateCtx{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		taskID:            taskID,
	}
}

func (c *delegRunStateCtx) SessionID() string          { return c.taskID }
func (c *delegRunStateCtx) UserID() string             { return "user-wf-test" }
func (c *delegRunStateCtx) State() session.State       { return c.state }
func (c *delegRunStateCtx) Artifacts() agent.Artifacts { return nil }
