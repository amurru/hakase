// gate_pause_engine_test.go - engine-behavior experiments for
// durable-resume Phase 4 (gate-as-pause rewiring).
//
// These tests pin the ADK v2.4.0 mechanics the resume design relies
// on, using a scripted model (no network) and the durable service:
//  1. A completed turn through an IsLongRunning tool finishes
//     normally (marker does not poison later turns).
//  2. A crashed turn (call + LongRunningToolIDs marker persisted, no
//     response) resumes from a FunctionResponse WITHOUT re-executing
//     the tool — across service instances (simulated restart).
//  3. Control: the same crash shape without the marker.
package agent

import (
	"context"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	hakasesession "amurru/hakase/internal/session"
)

// scriptedModel replays canned LLM responses per GenerateContent call
// (last script repeats).
type scriptedModel struct {
	mu      sync.Mutex
	calls   int
	scripts [][]*model.LLMResponse
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.calls++
	idx := m.calls - 1
	if idx >= len(m.scripts) {
		idx = len(m.scripts) - 1
	}
	batch := m.scripts[idx]
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, r := range batch {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func spikeTextResponse(text string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: genai.NewContentFromText(text, genai.RoleModel),
	}
}

func spikeCallResponse(toolName, callID string, args map[string]any) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{ID: callID, Name: toolName, Args: args},
			}},
		},
	}
}

type spikeArgs struct {
	Q string `json:"q"`
}

type spikeOut struct {
	A string `json:"a"`
}

// spikeHarness builds a runner over a durable service in dir with one
// tool. execCount records handler invocations.
type spikeHarness struct {
	runner    *runner.Runner
	execCount *atomic.Int64
}

func newSpikeHarness(t *testing.T, dir string, m *scriptedModel, execCount *atomic.Int64, longRunning bool) *spikeHarness {
	t.Helper()
	tl, err := functiontool.New(functiontool.Config{
		Name:          "ask_thing",
		Description:   "test tool",
		IsLongRunning: longRunning,
	}, func(_ adkagent.Context, in spikeArgs) (spikeOut, error) {
		execCount.Add(1)
		return spikeOut{A: "answer:" + in.Q}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	agt, err := llmagent.New(llmagent.Config{
		Name:  "orchestrator",
		Model: m,
		Tools: []tool.Tool{tl},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	svc, err := hakasesession.NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName:           "spike",
		Agent:             agt,
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	return &spikeHarness{runner: r, execCount: execCount}
}

// drainRun collects streamed text from one Runner.Run call.
func drainRun(t *testing.T, r *runner.Runner, sessionID string, msg *genai.Content) string {
	t.Helper()
	var sb strings.Builder
	for ev, err := range r.Run(context.Background(), "user-1", sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.Text != "" && !p.Thought {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}

func TestSpike_LongRunningNormalTurnCompletes(t *testing.T) {
	dir := t.TempDir()
	var execCount atomic.Int64
	m := &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeCallResponse("ask_thing", "call-1", map[string]any{"q": "hi"})},
		{spikeTextResponse("final-done")},
		{spikeTextResponse("second-turn-ok")},
	}}
	h := newSpikeHarness(t, dir, m, &execCount, true)

	out := drainRun(t, h.runner, "sess-a", genai.NewContentFromText("go", genai.RoleUser))
	if !strings.Contains(out, "final-done") {
		t.Fatalf("turn output = %q, want final-done", out)
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	// A follow-up turn on the same ADK session proceeds normally.
	out2 := drainRun(t, h.runner, "sess-a", genai.NewContentFromText("again", genai.RoleUser))
	if !strings.Contains(out2, "second-turn-ok") {
		t.Fatalf("second turn output = %q", out2)
	}
}

// TestSpike_CrashNoMarkerControl repeats the crash shape without the
// LongRunningToolIDs marker: the answer has nothing to match against.
func TestSpike_CrashNoMarkerControl(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc1, err := hakasesession.NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	cr, err := svc1.Create(ctx, &adksession.CreateRequest{
		AppName: "spike", UserID: "user-1", SessionID: "sess-c",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	userEv := &adksession.Event{
		ID: "evt-u1", Timestamp: ts, InvocationID: "inv-9", Author: "user",
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText("go", genai.RoleUser),
		},
	}
	callEv := &adksession.Event{
		ID: "evt-c9", Timestamp: ts, InvocationID: "inv-9", Author: "orchestrator",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID: "call-9", Name: "ask_thing", Args: map[string]any{"q": "hi"},
					},
				}},
			},
		},
		// No LongRunningToolIDs: crash before any marker existed.
	}
	if err := svc1.AppendEvent(ctx, cr.Session, userEv); err != nil {
		t.Fatalf("AppendEvent user: %v", err)
	}
	if err := svc1.AppendEvent(ctx, cr.Session, callEv); err != nil {
		t.Fatalf("AppendEvent call: %v", err)
	}

	var execCount atomic.Int64
	m := &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeTextResponse("resumed-ok")},
	}}
	h := newSpikeHarness(t, dir, m, &execCount, true)

	fr := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID: "call-9", Name: "ask_thing",
				Response: map[string]any{"a": "answer:hi"},
			},
		}},
	}
	out := drainRun(t, h.runner, "sess-c", fr)
	// Documents the contrast: without the marker the answer is still
	// consumed (fresh-run path matches the open call), but the run does
	// NOT route through wf.Resume (no schema validation, no duplicate
	// suppression, no waiting-node bookkeeping). Markers remain required:
	// they select the designed resume path and make open pauses
	// detectable via openLongRunningCallIDs.
	if !strings.Contains(out, "resumed-ok") {
		t.Fatalf("no-marker resume output = %q, want resumed-ok", out)
	}
	if got := execCount.Load(); got != 0 {
		t.Fatalf("tool re-executed %d times, want 0", got)
	}
}

// TestSpike_CrashResumeNoReexec crafts history as-if the process died
// mid-tool-call (call + marker persisted, no response), restarts with
// a fresh service instance, and resumes with the answer.
func TestSpike_CrashResumeNoReexec(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc1, err := hakasesession.NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	cr, err := svc1.Create(ctx, &adksession.CreateRequest{
		AppName: "spike", UserID: "user-1", SessionID: "sess-b",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	userEv := &adksession.Event{
		ID: "evt-u1", Timestamp: ts, InvocationID: "inv-9", Author: "user",
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText("go", genai.RoleUser),
		},
	}
	callEv := &adksession.Event{
		ID: "evt-c9", Timestamp: ts, InvocationID: "inv-9", Author: "orchestrator",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID: "call-9", Name: "ask_thing", Args: map[string]any{"q": "hi"},
					},
				}},
			},
		},
		LongRunningToolIDs: []string{"call-9"},
	}
	if err := svc1.AppendEvent(ctx, cr.Session, userEv); err != nil {
		t.Fatalf("AppendEvent user: %v", err)
	}
	if err := svc1.AppendEvent(ctx, cr.Session, callEv); err != nil {
		t.Fatalf("AppendEvent call: %v", err)
	}

	// Simulated restart: fresh harness (fresh service instance, same dir).
	var execCount atomic.Int64
	m := &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeTextResponse("resumed-ok")},
	}}
	h := newSpikeHarness(t, dir, m, &execCount, true)

	fr := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID: "call-9", Name: "ask_thing",
				Response: map[string]any{"a": "answer:hi"},
			},
		}},
	}
	out := drainRun(t, h.runner, "sess-b", fr)
	if !strings.Contains(out, "resumed-ok") {
		t.Fatalf("resume output = %q, want resumed-ok", out)
	}
	if got := execCount.Load(); got != 0 {
		t.Fatalf("tool re-executed %d times on resume, want 0", got)
	}
}
