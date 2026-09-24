package runserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"
)

// fakeRunner scripts the sink side of a turn without a model: it records the
// call and delegates the event stream to onTurn.
type fakeRunner struct {
	mu            sync.Mutex
	runs          int
	lastSessionID string
	lastContent   *genai.Content
	onTurn        func(ctx context.Context, sessionID string, sink agentrun.EventSink)
}

func (f *fakeRunner) RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink) {
	f.mu.Lock()
	f.runs++
	f.lastSessionID = sessionID
	f.lastContent = content
	onTurn := f.onTurn
	f.mu.Unlock()
	if onTurn != nil {
		onTurn(ctx, sessionID, sink)
	}
	sink.OnDone(sessionID)
}

func (f *fakeRunner) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// harness wires the full in-process MCP pair: runserver tools mounted on a
// server connected to a scripted client over in-memory transports. No model,
// no network - the fake runner plays the agent. Gate round trips ride the
// SDK's multi-round-trip middleware, which fulfills input requests with the
// scripted ElicitationHandler and retries the call transparently.
type harness struct {
	t        *testing.T
	srv      *server
	gates    *Gates
	runner   *fakeRunner
	sessions *hakasesession.SessionService
	cs       *mcp.ClientSession
}

type harnessOpts struct {
	elicitation func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error)
	approval    interfaces.ApprovalConfig
	clarify     interfaces.ClarifyConfig
	onTurn      func(ctx context.Context, sessionID string, sink agentrun.EventSink)
}

func newHarness(t *testing.T, opts harnessOpts) *harness {
	t.Helper()
	store, err := hakasesession.NewSessionStoreWithSnapshotLimit(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}
	h := &harness{
		t:        t,
		gates:    NewGates(opts.approval, opts.clarify),
		runner:   &fakeRunner{onTurn: opts.onTurn},
		sessions: svc,
	}
	h.srv = &server{
		deps:    Deps{Runner: h.runner, Sessions: svc, Gates: h.gates},
		runs:    map[string]*suspendedRun{},
		tracker: &runTracker{active: map[string]int{}},
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "hakase", Version: "test"}, nil)
	mountOn(srv, h.srv)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	// Servers must be connected before clients (SDK contract).
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	clientOpts := &mcp.ClientOptions{}
	if opts.elicitation != nil {
		clientOpts.ElicitationHandler = opts.elicitation
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "host", Version: "1"}, clientOpts)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	h.cs = cs
	t.Cleanup(func() { _ = cs.Close() })
	return h
}

// callTool invokes a mounted tool and decodes the structured result into out.
// Returns the raw result for IsError inspection.
func (h *harness) callTool(name string, arguments map[string]any, out any) (*mcp.CallToolResult, error) {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if out != nil && res.StructuredContent != nil {
		b, merr := json.Marshal(res.StructuredContent)
		if merr != nil {
			h.t.Fatalf("marshal structured content: %v", merr)
		}
		if uerr := json.Unmarshal(b, out); uerr != nil {
			h.t.Fatalf("unmarshal structured content into %T: %v (%s)", out, uerr, b)
		}
	}
	return res, nil
}

// contentText flattens a tool result's text content for assertion matching.
func contentText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// runArgs builds the run tool's call arguments.
func runArgs(prompt, sessionID string, timeout int) map[string]any {
	args := map[string]any{"prompt": prompt}
	if sessionID != "" {
		args["session_id"] = sessionID
	}
	if timeout != 0 {
		args["timeout_seconds"] = timeout
	}
	return args
}

// serveBroker answers a broker's queries directly with a scripted result,
// bypassing the wire - it tests the gates' answer-to-contract mapping in
// isolation from the SEP-2322 plumbing.
func serveBroker(b *gateBroker, script func(*mcp.ElicitParams) *mcp.ElicitResult) (done func()) {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case q, ok := <-b.queries:
				if !ok {
					return
				}
				q.respCh <- gateResp{res: script(q.params)}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

func TestRunHappyPath(t *testing.T) {
	h := newHarness(t, harnessOpts{
		onTurn: func(_ context.Context, _ string, sink agentrun.EventSink) {
			sink.OnStream("s", "Hello from ", "")
			sink.OnStream("s", "hakase.", "")
			sink.OnStream("s", "", "deep thought")
			sink.OnLog("s", "Call: read_file(path)")
			sink.OnLog("s", "Response: read_file")
			sink.OnUsage("s", 123, 0)
		},
	})
	var out runOut
	res, err := h.callTool("run", runArgs("Say hello", "", 0), &out)
	if err != nil {
		t.Fatalf("call run: %v", err)
	}
	if res.IsError {
		t.Fatalf("run returned tool error: %v", res.Content)
	}
	if out.Status != "completed" || out.Answer != "Hello from hakase." || out.Thinking != "deep thought" {
		t.Fatalf("unexpected runOut: %+v", out)
	}
	if out.Tokens != 123 {
		t.Fatalf("tokens = %d, want 123", out.Tokens)
	}
	if len(out.Activity) != 2 || out.Activity[0] != "Call: read_file(path)" {
		t.Fatalf("activity = %v", out.Activity)
	}
	if out.SessionID == "" {
		t.Fatal("session_id empty")
	}
	// The user turn is persisted by the run handler (the fake runner plays
	// the agent, so only the user message is expected in the store).
	msgs, err := h.sessions.GetMessages(out.SessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "Say hello" {
		t.Fatalf("persisted messages = %+v", msgs)
	}
	// The runner saw the same session and the prompt as user content.
	if h.runner.lastSessionID != out.SessionID || h.runner.lastContent == nil ||
		len(h.runner.lastContent.Parts) != 1 || h.runner.lastContent.Parts[0].Text != "Say hello" {
		t.Fatalf("runner saw session=%s content=%+v", h.runner.lastSessionID, h.runner.lastContent)
	}
	// The run released its slot and left the registry (after the TTL the
	// entry goes; before that, a finished run must not hold the slot).
	if err := h.srv.tracker.TryStart(out.SessionID); err != nil {
		t.Fatalf("slot not released after run: %v", err)
	}
	h.srv.tracker.Finish(out.SessionID)
}

func TestRunContinuesExistingSession(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var first runOut
	if _, err := h.callTool("run", runArgs("first", "", 0), &first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Seed an agent reply so the session has two turns, then continue it.
	sess, err := h.sessions.Store().Load(first.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sess.AddMessage("agent", "earlier reply", "")
	if err := h.sessions.Store().Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	var second runOut
	res, err := h.callTool("run", runArgs("second prompt", first.SessionID, 0), &second)
	if err != nil || res.IsError {
		t.Fatalf("second run: err=%v isError=%v content=%v", err, res != nil && res.IsError, res)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("session forked: %s != %s", second.SessionID, first.SessionID)
	}
	if h.runner.lastSessionID != first.SessionID {
		t.Fatalf("runner ran on %s, want %s", h.runner.lastSessionID, first.SessionID)
	}
}

func TestRunUnknownSessionIsToolError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res, err := h.callTool("run", runArgs("hi", "sess_missing", 0), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for unknown session")
	}
	if h.runner.runCount() != 0 {
		t.Fatal("runner must not run for unknown sessions")
	}
}

func TestRunEmptyPromptIsToolError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res, err := h.callTool("run", runArgs("   ", "", 0), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for empty prompt")
	}
}

func TestRunBusySession(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// A real session, whose run slot is occupied directly: the go-sdk serves
	// requests sequentially per connection, so a genuinely in-flight `run`
	// would make a second call queue behind it rather than race it. The
	// guard itself is what this asserts (TestRunTracker covers the tracker).
	sess, err := h.sessions.CreateSession("busy")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := h.srv.tracker.TryStart(sess.ID); err != nil {
		t.Fatalf("occupy slot: %v", err)
	}
	defer h.srv.tracker.Finish(sess.ID)

	res, err := h.callTool("run", runArgs("while busy", sess.ID, 0), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(contentText(res), "already active") {
		t.Fatalf("expected busy tool error, got isError=%v content=%q", res.IsError, contentText(res))
	}
	// The refused run must not have started anything.
	if h.runner.runCount() != 0 {
		t.Fatal("runner must not run for a busy session")
	}
}

func TestRunTimeout(t *testing.T) {
	h := newHarness(t, harnessOpts{
		onTurn: func(ctx context.Context, _ string, _ agentrun.EventSink) {
			<-ctx.Done() // the run only ends when its context does
		},
	})
	var out runOut
	res, err := h.callTool("run", runArgs("slow", "", 1), &out)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("timeout surfaced as tool error: %v", res.Content)
	}
	if out.Status != "timeout" || out.Error == "" {
		t.Fatalf("unexpected runOut: %+v", out)
	}
}

func TestRunToolErrorStatusWhenAgentErrored(t *testing.T) {
	h := newHarness(t, harnessOpts{
		onTurn: func(_ context.Context, _ string, sink agentrun.EventSink) {
			sink.OnLog("s", "Error: model unavailable")
		},
	})
	var out runOut
	if _, err := h.callTool("run", runArgs("hi", "", 0), &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Status != "error" || !strings.Contains(out.Error, "model unavailable") {
		t.Fatalf("unexpected runOut: %+v", out)
	}
	if out.Answer != "" {
		t.Fatalf("answer should be empty, got %q", out.Answer)
	}
}

func TestRunNegativeTimeoutIsToolError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res, err := h.callTool("run", runArgs("hi", "", -5), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for negative timeout")
	}
}

func TestUnknownRunStateIsToolError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// Arguments as a plain map: a json.RawMessage inside the any-typed
	// Arguments field would be marshaled as base64.
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:         "run",
		Arguments:    map[string]any{"prompt": "hi"},
		RequestState: "bogus-state",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(contentText(res), "unknown or expired run state") {
		t.Fatalf("expected unknown-state tool error, got isError=%v content=%q", res.IsError, contentText(res))
	}
}

func TestSuspendedRunDeliver(t *testing.T) {
	sr := &suspendedRun{
		broker:  &gateBroker{queries: make(chan *gateQuery)},
		pending: map[string]*gateQuery{},
	}
	q := &gateQuery{params: &mcp.ElicitParams{Message: "approve?"}, respCh: make(chan gateResp, 1)}
	sr.mu.Lock()
	sr.pending["gate-1"] = q
	sr.mu.Unlock()

	sr.deliver(mcp.InputResponseMap{
		"gate-1": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}},
		"nope":   &mcp.ElicitResult{Action: "accept"}, // unknown id: dropped
	})
	select {
	case r := <-q.respCh:
		if r.res == nil || r.res.Action != "accept" || r.res.Content["approve"] != true {
			t.Fatalf("delivered response = %+v", r)
		}
	default:
		t.Fatal("response not delivered")
	}
}

func TestListSessions(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var out runOut
	if _, err := h.callTool("run", runArgs("make a session", "", 0), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	var list listSessionsOut
	if _, err := h.callTool("list_sessions", map[string]any{}, &list); err != nil {
		t.Fatalf("list_sessions: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != out.SessionID {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
	// Archive it: hidden by default, visible with include_archived.
	if err := h.sessions.ArchiveSession(out.SessionID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := h.callTool("list_sessions", map[string]any{}, &list); err != nil {
		t.Fatalf("list_sessions: %v", err)
	}
	if len(list.Sessions) != 0 {
		t.Fatalf("archived session leaked into default list: %+v", list.Sessions)
	}
	if _, err := h.callTool("list_sessions", map[string]any{"include_archived": true}, &list); err != nil {
		t.Fatalf("list_sessions archived: %v", err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("archived session missing: %+v", list.Sessions)
	}
}

func TestGetSession(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var run runOut
	if _, err := h.callTool("run", runArgs("read me", "", 0), &run); err != nil {
		t.Fatalf("run: %v", err)
	}
	sess, err := h.sessions.Store().Load(run.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sess.AddMessage("agent", "the reply", "the thought")
	if err := h.sessions.Store().Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	var out getSessionOut
	if _, err := h.callTool("get_session", map[string]any{"session_id": run.SessionID}, &out); err != nil {
		t.Fatalf("get_session: %v", err)
	}
	if out.ID != run.SessionID || out.Title != "read me" {
		t.Fatalf("unexpected header: %+v", out)
	}
	if len(out.Messages) != 2 || out.Messages[0].Role != "user" || out.Messages[1].Content != "the reply" {
		t.Fatalf("messages = %+v", out.Messages)
	}

	// limit=1 returns only the LAST message.
	var limited getSessionOut
	if _, err := h.callTool("get_session", map[string]any{"session_id": run.SessionID, "limit": 1}, &limited); err != nil {
		t.Fatalf("get_session limited: %v", err)
	}
	if len(limited.Messages) != 1 || limited.Messages[0].Content != "the reply" {
		t.Fatalf("limited messages = %+v", limited.Messages)
	}

	res, err := h.callTool("get_session", map[string]any{"session_id": "sess_missing"}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for unknown session")
	}
}

func TestProgressNotifications(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	store, err := hakasesession.NewSessionStoreWithSnapshotLimit(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}
	gates := NewGates(interfaces.ApprovalConfig{}, interfaces.ClarifyConfig{})
	runner := &fakeRunner{onTurn: func(_ context.Context, _ string, sink agentrun.EventSink) {
		sink.OnStream("s", "chunk one ", "")
		sink.OnStream("s", "chunk two", "")
		sink.OnLog("s", "Call: read_file(path)")
	}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "hakase", Version: "test"}, nil)
	mountOn(srv, &server{
		deps:    Deps{Runner: runner, Sessions: svc, Gates: gates},
		runs:    map[string]*suspendedRun{},
		tracker: &runTracker{active: map[string]int{}},
	})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "host", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			messages = append(messages, req.Params.Message)
			mu.Unlock()
		},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	params := &mcp.CallToolParams{Name: "run", Arguments: runArgs("hi", "", 0)}
	params.SetProgressToken("tok-1")
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	// Notifications are async; poll briefly for the three events.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(messages)
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected >=3 progress notifications, got %d (%v)", n, messages)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(messages[0], "chunk one") {
		t.Fatalf("first notification = %q", messages[0])
	}
	if !strings.Contains(messages[2], "read_file") {
		t.Fatalf("third notification = %q", messages[2])
	}
}

func TestGatesMapping(t *testing.T) {
	// The gates' answer-to-contract mapping, driven through a manually
	// served broker (no wire): the run path tests the SEP-2322 plumbing
	// separately.
	newMapped := func(t *testing.T) *Gates {
		t.Helper()
		return NewGates(interfaces.ApprovalConfig{Mode: "interactive"}, interfaces.ClarifyConfig{})
	}
	ask := func(g *Gates, b *gateBroker, sessionID string) (bool, error) {
		g.attach(sessionID, b)
		defer g.detach(sessionID, b)
		return g.AskApproval(interfaces.ApprovalRequest{
			Tool: "system_exec", Command: "rm -rf /tmp/x", Risk: "high", Reason: "cleanup", SessionID: sessionID,
		})
	}

	t.Run("accept", func(t *testing.T) {
		g := newMapped(t)
		b := &gateBroker{queries: make(chan *gateQuery)}
		var gotMessage string
		done := serveBroker(b, func(p *mcp.ElicitParams) *mcp.ElicitResult {
			gotMessage = p.Message
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}
		})
		defer done()
		ok, err := ask(g, b, "s1")
		if err != nil || !ok {
			t.Fatalf("AskApproval = (%v, %v), want (true, nil)", ok, err)
		}
		if !strings.Contains(gotMessage, "system_exec") || !strings.Contains(gotMessage, "rm -rf /tmp/x") {
			t.Fatalf("elicitation message = %q", gotMessage)
		}
	})
	t.Run("decline is a no not an error", func(t *testing.T) {
		g := newMapped(t)
		b := &gateBroker{queries: make(chan *gateQuery)}
		done := serveBroker(b, func(*mcp.ElicitParams) *mcp.ElicitResult {
			return &mcp.ElicitResult{Action: "decline"}
		})
		defer done()
		ok, err := ask(g, b, "s1")
		if err != nil || ok {
			t.Fatalf("AskApproval = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("allow mode short-circuits", func(t *testing.T) {
		g := NewGates(interfaces.ApprovalConfig{Mode: "allow"}, interfaces.ClarifyConfig{})
		ok, err := g.AskApproval(interfaces.ApprovalRequest{Tool: "system_exec"})
		if err != nil || !ok {
			t.Fatalf("allow mode = (%v, %v)", ok, err)
		}
	})
	t.Run("deny mode short-circuits", func(t *testing.T) {
		g := NewGates(interfaces.ApprovalConfig{Mode: "deny"}, interfaces.ClarifyConfig{})
		ok, err := g.AskApproval(interfaces.ApprovalRequest{Tool: "system_exec"})
		if err != nil || ok {
			t.Fatalf("deny mode = (%v, %v)", ok, err)
		}
	})
	t.Run("unbound fails closed", func(t *testing.T) {
		g := newMapped(t)
		ok, err := g.AskApproval(interfaces.ApprovalRequest{Tool: "system_exec"})
		if ok || err == nil {
			t.Fatalf("unbound = (%v, %v), want fail-closed error", ok, err)
		}
	})
	t.Run("expiry fails closed", func(t *testing.T) {
		g := NewGates(interfaces.ApprovalConfig{Mode: "interactive", ExpirySeconds: 1}, interfaces.ClarifyConfig{})
		b := &gateBroker{queries: make(chan *gateQuery)} // nobody serves it
		g.attach("s1", b)
		defer g.detach("s1", b)
		start := time.Now()
		ok, err := g.AskApproval(interfaces.ApprovalRequest{Tool: "system_exec", SessionID: "s1"})
		if ok || err == nil {
			t.Fatalf("expired ask = (%v, %v), want fail-closed", ok, err)
		}
		if time.Since(start) < 900*time.Millisecond {
			t.Fatalf("ask returned before expiry (%v)", time.Since(start))
		}
	})
}

func TestGatesClarifyMapping(t *testing.T) {
	newGates := func(t *testing.T, expiry int) (*Gates, *gateBroker, func()) {
		t.Helper()
		g := NewGates(interfaces.ApprovalConfig{}, interfaces.ClarifyConfig{ExpirySeconds: expiry})
		b := &gateBroker{queries: make(chan *gateQuery)}
		g.attach("s1", b)
		return g, b, func() { g.detach("s1", b) }
	}
	runAsk := func(g *Gates, req interfaces.ClarifyRequest, script func(*mcp.ElicitParams) *mcp.ElicitResult) (interfaces.ClarifyResponse, error) {
		done := serveBroker(g.brokers["s1"], script)
		defer done()
		req.SessionID = "s1"
		return g.AskClarify(req)
	}

	t.Run("single choice", func(t *testing.T) {
		g, _, detach := newGates(t, 0)
		defer detach()
		var gotSchema map[string]any
		resp, err := runAsk(g, interfaces.ClarifyRequest{Question: "which?", Choices: []string{"a", "b"}},
			func(p *mcp.ElicitParams) *mcp.ElicitResult {
				gotSchema = p.RequestedSchema.(map[string]any)
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"answer": "b"}}
			})
		if err != nil || len(resp.Answer) != 1 || resp.Answer[0] != "b" {
			t.Fatalf("AskClarify = (%+v, %v)", resp, err)
		}
		if !strings.Contains(fmt.Sprint(gotSchema), "enum") {
			t.Fatalf("schema missing enum: %v", gotSchema)
		}
	})
	t.Run("multi select", func(t *testing.T) {
		g, _, detach := newGates(t, 0)
		defer detach()
		resp, err := runAsk(g, interfaces.ClarifyRequest{Question: "which?", Choices: []string{"a", "b", "c"}, MultiSelect: true},
			func(*mcp.ElicitParams) *mcp.ElicitResult {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"answers": []any{"a", "c"}}}
			})
		if err != nil || len(resp.Answer) != 2 {
			t.Fatalf("AskClarify = (%+v, %v)", resp, err)
		}
	})
	t.Run("free text", func(t *testing.T) {
		g, _, detach := newGates(t, 0)
		defer detach()
		resp, err := runAsk(g, interfaces.ClarifyRequest{Question: "how?"},
			func(*mcp.ElicitParams) *mcp.ElicitResult {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"answer": "do it my way"}}
			})
		if err != nil || len(resp.Answer) != 1 || resp.Answer[0] != "do it my way" {
			t.Fatalf("AskClarify = (%+v, %v)", resp, err)
		}
	})
	t.Run("decline cancels", func(t *testing.T) {
		g, _, detach := newGates(t, 0)
		defer detach()
		resp, err := runAsk(g, interfaces.ClarifyRequest{Question: "how?"},
			func(*mcp.ElicitParams) *mcp.ElicitResult { return &mcp.ElicitResult{Action: "decline"} })
		if err != nil || !resp.Canceled {
			t.Fatalf("AskClarify = (%+v, %v), want Canceled", resp, err)
		}
	})
	t.Run("unbound fails closed", func(t *testing.T) {
		g := NewGates(interfaces.ApprovalConfig{}, interfaces.ClarifyConfig{})
		if _, err := g.AskClarify(interfaces.ClarifyRequest{Question: "how?"}); err == nil {
			t.Fatal("unbound clarify must fail closed")
		}
	})
	t.Run("expiry times out", func(t *testing.T) {
		g, b, detach := newGates(t, 1)
		defer detach()
		// Nobody serves the broker: the ask must expire into TimedOut.
		resp, err := g.AskClarify(interfaces.ClarifyRequest{Question: "how?", SessionID: "s1"})
		_ = b
		if err != nil || !resp.TimedOut {
			t.Fatalf("AskClarify = (%+v, %v), want TimedOut", resp, err)
		}
	})
}

// TestRunGateRoundTrip exercises the full SEP-2322 path over the wire: the
// fake runner raises an approval mid-turn, the run suspends with an
// input-required result, the scripted client fulfills it, and the retry
// resumes the run to completion.
func TestRunGateRoundTrip(t *testing.T) {
	h := newHarness(t, harnessOpts{
		elicitation: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			if !strings.Contains(req.Params.Message, "system_exec") {
				return nil, fmt.Errorf("unexpected elicitation: %q", req.Params.Message)
			}
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
		},
	})
	var approvalOK bool
	var approvalErr error
	h.runner.onTurn = func(_ context.Context, sessionID string, _ agentrun.EventSink) {
		// The gates are attached for the run's whole lifetime; the ask rides
		// the run's broker by session id.
		approvalOK, approvalErr = h.gates.AskApproval(interfaces.ApprovalRequest{
			Tool: "system_exec", Command: "echo hi", SessionID: sessionID,
		})
	}
	var out runOut
	res, err := h.callTool("run", runArgs("needs approval", "", 0), &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	if approvalErr != nil || !approvalOK {
		t.Fatalf("mid-run approval = (%v, %v)", approvalOK, approvalErr)
	}
	if out.Status != "completed" {
		t.Fatalf("status = %s (%s)", out.Status, out.Error)
	}
}

// TestRunGateUnfulfillableClient covers the degraded path: the client cannot
// fulfill input requests (no elicitation support). The SDK aborts the call;
// the suspended run's gate fails closed on its own expiry and the run winds
// down without wedging.
func TestRunGateUnfulfillableClient(t *testing.T) {
	h := newHarness(t, harnessOpts{
		approval: interfaces.ApprovalConfig{Mode: "interactive", ExpirySeconds: 1},
	})
	h.runner.onTurn = func(_ context.Context, sessionID string, _ agentrun.EventSink) {
		// Blocks up to the 1s gate expiry, then the denial lets the turn end.
		_, _ = h.gates.AskApproval(interfaces.ApprovalRequest{Tool: "system_exec", SessionID: sessionID})
	}
	_, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "run", Arguments: runArgs("hi", "", 0)})
	if err == nil || !strings.Contains(err.Error(), "fulfill") {
		t.Fatalf("expected client-side fulfillment failure, got %v", err)
	}
}

func TestRunTracker(t *testing.T) {
	tr := &runTracker{active: map[string]int{}}
	for i := 0; i < maxConcurrentRuns; i++ {
		if err := tr.TryStart(fmt.Sprintf("s%d", i)); err != nil {
			t.Fatalf("TryStart s%d: %v", i, err)
		}
	}
	if err := tr.TryStart("overflow"); !errors.Is(err, errRunsSaturated) {
		t.Fatalf("cap not enforced: %v", err)
	}
	if err := tr.TryStart("s0"); !errors.Is(err, errSessionBusy) {
		t.Fatalf("busy not enforced: %v", err)
	}
	tr.Finish("s0")
	if err := tr.TryStart("s0"); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	tr.Finish("unknown") // must not panic
}
