// delegate_agenttool_test.go - T1.2 parity coverage for the agenttool-based
// delegation run path (AD-001): the structural Run assertion against ADK's
// agenttool, the callback-driven run state, the delegation route resolvers,
// and the handler's real-run behaviors (completion, repair, guard abort,
// watchdog) against an in-process mock model.
package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/genai"
)

// delegMockEntry is one queued model turn: either a response or an error.
// chunks, when set, are streamed as partial responses followed by a final
// aggregated non-partial response, mirroring the real SSE aggregator.
type delegMockEntry struct {
	text   string
	chunks []string
	err    error
}

// delegMockModel is a minimal model.LLM double. Each GenerateContent call
// consumes one queued entry; with none left it errors loudly so tests fail
// on unexpected model turns instead of hanging.
type delegMockModel struct {
	mu       sync.Mutex
	entries  []delegMockEntry
	requests []*model.LLMRequest
	blockFor time.Duration
}

func (m *delegMockModel) Name() string { return "deleg_mock" }

func (m *delegMockModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	var e delegMockEntry
	if len(m.entries) > 0 {
		e = m.entries[0]
		m.entries = m.entries[1:]
	} else {
		e = delegMockEntry{err: errors.New("deleg_mock: no queued response")}
	}
	block := m.blockFor
	m.mu.Unlock()

	return func(yield func(*model.LLMResponse, error) bool) {
		if block > 0 {
			// Model calls honor context cancellation like real providers.
			select {
			case <-time.After(block):
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
		if e.err != nil {
			yield(nil, e.err)
			return
		}
		if len(e.chunks) > 0 {
			var complete strings.Builder
			for _, chunk := range e.chunks {
				complete.WriteString(chunk)
				resp := &model.LLMResponse{
					Content: genai.NewContentFromText(chunk, genai.RoleModel),
					Partial: true,
				}
				if !yield(resp, nil) {
					return
				}
			}
			yield(&model.LLMResponse{
				Content: genai.NewContentFromText(complete.String(), genai.RoleModel),
			}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText(e.text, genai.RoleModel),
		}, nil)
	}
}

// delegRunCtx backs a real delegation run: agenttool.Run needs State,
// UserID, and Artifacts on the parent context in addition to the session
// probe the override-seam tests use, and the workflow scheduler derives a
// cancelable context from it, so the standard context methods must come
// from a real payload (StrictContextMock.Ctx).
type delegRunCtx struct {
	agent.StrictContextMock
	taskID string
}

func newDelegRunCtx(taskID string) *delegRunCtx {
	return &delegRunCtx{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		taskID:            taskID,
	}
}

func (c *delegRunCtx) SessionID() string          { return c.taskID }
func (c *delegRunCtx) UserID() string             { return "user-deleg-test" }
func (c *delegRunCtx) State() session.State       { return mapState{} }
func (c *delegRunCtx) Artifacts() agent.Artifacts { return nil }

// mapState is a minimal session.State over a plain map.
type mapState map[string]any

func (m mapState) Get(k string) (any, error) {
	v, ok := m[k]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}

func (m mapState) Set(k string, v any) error { m[k] = v; return nil }

func (m mapState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range m {
			if !yield(k, v) {
				return
			}
		}
	}
}

// cbCtx carries a function-call id into the tool callbacks.
type cbCtx struct {
	agent.ContextMock
	id string
}

func (c *cbCtx) FunctionCallID() string { return c.id }

// fakeTool is a named tool.Tool for callback unit tests.
type fakeTool struct{ name string }

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "fake" }
func (f fakeTool) IsLongRunning() bool { return false }

// clearDelegCache wipes cache entries whose key starts with norm, so tests
// never leak results into each other through the shared cache.
func clearDelegCache(t *testing.T, norm string) {
	t.Cleanup(func() {
		delegationCache.mu.Lock()
		for k := range delegationCache.m {
			if strings.HasPrefix(k, norm) {
				delete(delegationCache.m, k)
			}
		}
		delegationCache.mu.Unlock()
	})
}

func TestDelegateAgentToolExposesRun(t *testing.T) {
	mdl := &delegMockModel{}
	sub, err := llmagent.New(llmagent.Config{Name: "canary_agent", Model: mdl, Instruction: "x"})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	if _, ok := agenttool.New(sub, nil).(agentToolRunner); !ok {
		t.Fatal("agenttool.New result no longer satisfies agentToolRunner; update the structural assertion in delegate.go")
	}
}

func TestAgentToolRunSurfacesResultText(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{{text: "final answer"}}}
	sub, err := llmagent.New(llmagent.Config{Name: "smoke_agent", Model: mdl, Instruction: "x"})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	run, ok := agenttool.New(sub, nil).(agentToolRunner)
	if !ok {
		t.Fatal("agenttool.New does not satisfy agentToolRunner")
	}
	out, err := run.Run(newDelegRunCtx("task-smoke"), map[string]any{"request": "goal"})
	if err != nil {
		t.Fatalf("agenttool Run: %v", err)
	}
	if got, _ := out["result"].(string); got != "final answer" {
		t.Errorf("result = %q, want %q", got, "final answer")
	}
}

func TestDelegateRunStateCallbacks(t *testing.T) {
	reporter := newDelegationReporter("task-cb", "tester", "", "")
	st := &delegateRunState{reporter: reporter, agentLabel: "tester", guard: GuardDefaults(config.LoopGuardConfig{}), touch: func() {}}

	bt := st.beforeTool()
	if _, err := bt(&cbCtx{id: "c1"}, fakeTool{name: "write_file"}, map[string]any{"path": "/tmp/x"}); err != nil {
		t.Fatalf("beforeTool: %v", err)
	}
	if _, err := bt(&cbCtx{id: "c2"}, fakeTool{name: "web_search"}, map[string]any{"q": "x"}); err != nil {
		t.Fatalf("beforeTool: %v", err)
	}
	if !st.hasFunctionCall() {
		t.Error("hasFunctionCall = false, want true after native tool calls")
	}
	files := st.filesModifiedCopy()
	if len(files) != 1 || files[0] != "/tmp/x" {
		t.Errorf("filesModified = %v, want [/tmp/x] (only file-op tools harvested)", files)
	}

	am := st.afterModel()
	// Streaming: partial chunks accumulate (they are the increments); the
	// final aggregate after them is the concatenation of those chunks and
	// must be skipped to avoid double-counting.
	p1 := &model.LLMResponse{Content: genai.NewContentFromText("chunk ", genai.RoleModel), Partial: true}
	if _, err := am(&cbCtx{}, p1, nil); err != nil {
		t.Fatalf("afterModel partial 1: %v", err)
	}
	p2 := &model.LLMResponse{Content: genai.NewContentFromText("answer", genai.RoleModel), Partial: true}
	if _, err := am(&cbCtx{}, p2, nil); err != nil {
		t.Fatalf("afterModel partial 2: %v", err)
	}
	agg := &model.LLMResponse{Content: genai.NewContentFromText("chunk answer", genai.RoleModel)}
	if _, err := am(&cbCtx{}, agg, nil); err != nil {
		t.Fatalf("afterModel aggregate: %v", err)
	}
	if got := st.summaryString(); got != "chunk answer" {
		t.Errorf("summary = %q, want %q (aggregate not re-counted)", got, "chunk answer")
	}

	// Non-streaming: a complete response with no prior partials accumulates,
	// thoughts included.
	fresh := &delegateRunState{reporter: reporter, agentLabel: "tester", guard: GuardDefaults(config.LoopGuardConfig{}), touch: func() {}}
	whole := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
		{Text: "whole turn"},
		{Text: "internal thought", Thought: true},
	}}}
	if _, err := fresh.afterModel()(&cbCtx{}, whole, nil); err != nil {
		t.Fatalf("afterModel non-streaming: %v", err)
	}
	got := fresh.summaryString()
	if !strings.Contains(got, "whole turn") || !strings.Contains(got, "internal thought") {
		t.Errorf("summary = %q, want thought and answer text folded", got)
	}

	at := st.afterTool()
	if _, err := at(&cbCtx{id: "c1"}, fakeTool{name: "write_file"}, nil, map[string]any{"ok": true}, nil); err != nil {
		t.Fatalf("afterTool: %v", err)
	}
	// Tool errors pass through unchanged.
	if _, err := at(&cbCtx{id: "c9"}, fakeTool{name: "write_file"}, nil, nil, errors.New("boom")); err == nil || err.Error() != "boom" {
		t.Errorf("afterTool error = %v, want boom passed through", err)
	}
}

func TestDelegateRunStateGuardAbort(t *testing.T) {
	reporter := newDelegationReporter("task-guard", "tester", "", "")
	st := &delegateRunState{
		reporter:   reporter,
		agentLabel: "tester",
		guard:      GuardDefaults(config.LoopGuardConfig{RepetitionLimit: 2}),
		touch:      func() {},
	}
	am := st.afterModel()
	for i := 0; i < 2; i++ {
		// Streamed chunks: the repetition guard must trip on consecutive
		// identical partials, as it did in the bespoke event loop.
		resp := &model.LLMResponse{Content: genai.NewContentFromText("same line", genai.RoleModel), Partial: true}
		_, err := am(&cbCtx{}, resp, nil)
		if i == 0 && err != nil {
			t.Fatalf("first identical chunk aborted early: %v", err)
		}
		if i == 1 && err == nil {
			t.Fatal("second identical chunk did not abort")
		}
	}
	if reason := st.abortReasonGet(); reason == "" {
		t.Error("abortReason empty after guard trip")
	}
}

func TestDelegationRouteResolvers(t *testing.T) {
	// (a) A planted route with no invocation probe resolves directly.
	base := interfaces.WithDelegationRoute(context.Background(), "sess-route", "task-route")
	if got := interfaces.SessionIDFromCtx(base); got != "sess-route" {
		t.Errorf("SessionIDFromCtx = %q, want sess-route", got)
	}
	if got := interfaces.TaskIDFromCtx(base); got != "task-route" {
		t.Errorf("TaskIDFromCtx = %q, want task-route", got)
	}

	// (b) A registered main-run invocation still resolves through the
	// taskSessions probe (unchanged behavior).
	sess, task := "sess-route-main", "task-route-main"
	interfaces.RegisterTaskSession(task, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(task) })
	mainCtx := &delegLoop5Ctx{taskID: task}
	if got := interfaces.SessionIDFromCtx(mainCtx); got != sess {
		t.Errorf("SessionIDFromCtx = %q, want %q", got, sess)
	}
	if got := interfaces.TaskIDFromCtx(mainCtx); got != task {
		t.Errorf("TaskIDFromCtx = %q, want %q", got, task)
	}

	// (c) An unregistered child invocation (agenttool generates its own
	// session id) falls back to the planted route.
	child := &delegLoop5Ctx{taskID: "task-unregistered-child"}
	routed := interfaces.WithDelegationRoute(child, "sess-parent", "task-deleg")
	if got := interfaces.SessionIDFromCtx(routed); got != "sess-parent" {
		t.Errorf("SessionIDFromCtx = %q, want sess-parent (route fallback)", got)
	}
	if got := interfaces.TaskIDFromCtx(routed); got != "task-deleg" {
		t.Errorf("TaskIDFromCtx = %q, want task-deleg (route wins in child)", got)
	}

	// (d) The route survives the watchdog's cancellation wrapper.
	cancelCtx, cancel := context.WithCancel(interfaces.WithDelegationRoute(context.Background(), "sess-c", "task-c"))
	t.Cleanup(cancel)
	wrapped := &cancelableAgentCtx{Context: &delegLoop5Ctx{taskID: "task-inner"}, base: cancelCtx}
	if got := interfaces.SessionIDFromCtx(wrapped); got != "sess-c" {
		t.Errorf("SessionIDFromCtx = %q, want sess-c through cancelableAgentCtx", got)
	}
	if got := interfaces.TaskIDFromCtx(wrapped); got != "task-c" {
		t.Errorf("TaskIDFromCtx = %q, want task-c through cancelableAgentCtx", got)
	}
}

func TestDelegateHandlerAgenttoolHappyPath(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{{text: "all done"}}}
	saved := deps
	deps = &Deps{Model: mdl}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("agenttool happy probe")
	clearDelegCache(t, norm)

	res, err := delegateTaskHandler(newDelegRunCtx("task-happy"), DelegateTaskArgs{Goal: "agenttool happy probe"})
	if err != nil {
		t.Fatalf("delegateTaskHandler: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want completed (error: %q)", res.Status, res.Error)
	}
	if res.Summary != "all done" {
		t.Errorf("Summary = %q, want %q", res.Summary, "all done")
	}
	if res.TaskID == "" {
		t.Error("TaskID not assigned")
	}

	// Blocked tools must stay filtered on the new path: the child's first
	// request carries the tool declarations.
	if len(mdl.requests) == 0 {
		t.Fatal("no model requests captured")
	}
	names := declarationNames(mdl.requests[0])
	if len(names) == 0 {
		t.Fatal("no tool declarations on the child request; blocked-tools parity check is vacuous")
	}
	for name := range blockedTools() {
		if names[name] {
			t.Errorf("blocked tool %q reachable from delegated sub-agent", name)
		}
	}

	// The result is cached under the bare goal key (v1 parity: no policy).
	if _, ok := delegationCacheGet(norm); !ok {
		t.Error("result not cached after completion")
	}
}

func TestDelegateHandlerAgenttoolRepair(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{
		{err: errors.New("openai: parse function call args: unexpected newline")},
		{text: "repaired ok"},
	}}
	saved := deps
	deps = &Deps{Model: mdl}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("agenttool repair probe")
	clearDelegCache(t, norm)

	res, err := delegateTaskHandler(newDelegRunCtx("task-repair"), DelegateTaskArgs{Goal: "agenttool repair probe"})
	if err != nil {
		t.Fatalf("delegateTaskHandler: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want completed after JSON repair (error: %q)", res.Status, res.Error)
	}
	if res.Summary != "repaired ok" {
		t.Errorf("Summary = %q, want %q", res.Summary, "repaired ok")
	}
	if len(mdl.requests) != 2 {
		t.Errorf("model turns = %d, want 2 (original + repair attempt)", len(mdl.requests))
	}
}

func TestDelegateHandlerAgenttoolGuardAbort(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{
		{chunks: []string{"same line", "same line", "same line"}},
	}}
	saved := deps
	deps = &Deps{Model: mdl, LoopGuard: config.LoopGuardConfig{RepetitionLimit: 2}}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("agenttool guard abort probe")
	clearDelegCache(t, norm)

	res, err := delegateTaskHandler(newDelegRunCtx("task-guard"), DelegateTaskArgs{Goal: "agenttool guard abort probe"})
	if err != nil {
		t.Fatalf("delegateTaskHandler returned transport error: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("Status = %q, want failed after guard abort", res.Status)
	}
	if !strings.Contains(res.Error, "aborted") {
		t.Errorf("Error = %q, want the guard abort reason", res.Error)
	}
}

func TestDelegateHandlerAgenttoolTimeout(t *testing.T) {
	mdl := &delegMockModel{entries: []delegMockEntry{{text: "slow"}}, blockFor: 2 * time.Second}
	saved := deps
	deps = &Deps{Model: mdl, DelegateTimeout: 50 * time.Millisecond}
	t.Cleanup(func() { deps = saved })

	norm := normalizeDelegationGoal("agenttool timeout probe")
	clearDelegCache(t, norm)

	start := time.Now()
	res, err := delegateTaskHandler(newDelegRunCtx("task-timeout"), DelegateTaskArgs{Goal: "agenttool timeout probe"})
	if err != nil {
		t.Fatalf("delegateTaskHandler returned transport error: %v", err)
	}
	if res.Status != "timed_out" {
		t.Errorf("Status = %q, want timed_out (error: %q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Error, "did not complete") {
		t.Errorf("Error = %q, want the watchdog message", res.Error)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("timeout took %v, want the ~150ms hard ceiling", elapsed)
	}
}

// declarationNames collects the function names declared on a captured
// request's tool config.
func declarationNames(req *model.LLMRequest) map[string]bool {
	names := map[string]bool{}
	if req == nil || req.Config == nil {
		return names
	}
	for _, tl := range req.Config.Tools {
		if tl == nil {
			continue
		}
		for _, fd := range tl.FunctionDeclarations {
			if fd != nil {
				names[fd.Name] = true
			}
		}
	}
	for name := range req.Tools {
		names[name] = true
	}
	return names
}

// compile-time pin: fakeTool must satisfy tool.Tool for the callbacks.
var _ tool.Tool = fakeTool{}
