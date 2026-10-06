// resume_driver_test.go - Phase 5 resume driver tests.
//
// Histories are crafted directly against the durable ADK service
// (crash shape: call + marker persisted, no response), then exercised
// through the driver across fresh service instances (simulated
// restart). The clarify end-to-end uses the scripted model and the
// real clarify tool with a gate that fails the test if consulted:
// resume must not re-execute the tool.
package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
)

// resumeTestDeps wires deps with a pause registry and session store in
// dir, restoring globals on cleanup.
func resumeTestDeps(t *testing.T, dir string) *hakasesession.PauseRegistry {
	t.Helper()
	preg, err := hakasesession.NewPauseRegistry(dir)
	if err != nil {
		t.Fatalf("NewPauseRegistry: %v", err)
	}
	store, err := hakasesession.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	savedDeps, savedRt := deps, rt
	deps = &Deps{PauseRegistry: preg, SessionService: svc}
	rt = &Runtime{}
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
	return preg
}

// craftPausedHistory writes user + tool-call(+marker) events with no
// response: the crash-interrupted shape. completed names get
// FunctionResponse events appended before the open call.
func craftPausedHistory(t *testing.T, dir, adkSessionID, toolName, callID string, completed []string) {
	t.Helper()
	ctx := context.Background()
	svc, err := hakasesession.NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	cr, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName: ResumeAppName, UserID: ResumeUserID, SessionID: adkSessionID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	mkEv := func(id, author string, content *genai.Content, markers []string) *adksession.Event {
		return &adksession.Event{
			ID: id, Timestamp: ts, InvocationID: "inv-9", Author: author,
			LLMResponse:        model.LLMResponse{Content: content},
			LongRunningToolIDs: markers,
		}
	}
	if err := svc.AppendEvent(ctx, cr.Session, mkEv("evt-u1", "user",
		genai.NewContentFromText("go", genai.RoleUser), nil)); err != nil {
		t.Fatalf("AppendEvent user: %v", err)
	}
	for i, name := range completed {
		cid := fmt.Sprintf("done-%d", i)
		if err := svc.AppendEvent(ctx, cr.Session, mkEv("evt-call-"+cid, "orchestrator",
			&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{ID: cid, Name: name, Args: map[string]any{}},
			}}}, nil)); err != nil {
			t.Fatalf("AppendEvent completed call: %v", err)
		}
		if err := svc.AppendEvent(ctx, cr.Session, mkEv("evt-resp-"+cid, name,
			&genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{ID: cid, Name: name, Response: map[string]any{"ok": true}},
			}}}, nil)); err != nil {
			t.Fatalf("AppendEvent completed response: %v", err)
		}
	}
	if err := svc.AppendEvent(ctx, cr.Session, mkEv("evt-open", "orchestrator",
		&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: callID, Name: toolName, Args: map[string]any{"question": "which?"}},
		}}}, []string{callID})); err != nil {
		t.Fatalf("AppendEvent open call: %v", err)
	}
}

// recordPause stores one pause directly in the registry.
func recordPause(t *testing.T, preg *hakasesession.PauseRegistry, rec hakasesession.PauseRecord) string {
	t.Helper()
	id, err := preg.Record(rec)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return id
}

func TestListResumablePausesFindsOpenGate(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	ctx := context.Background()
	craftPausedHistory(t, dir, "turn-1", "clarify", "call-9", nil)
	recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-1",
		Gate: hakasesession.PauseGateClarify, Summary: "clarify: which?",
	})

	got, err := ListResumablePauses(ctx)
	if err != nil {
		t.Fatalf("ListResumablePauses: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("resumable = %d, want 1", len(got))
	}
	if len(got[0].OpenCalls) != 1 || got[0].OpenCalls[0].ID != "call-9" {
		t.Fatalf("open calls = %+v, want [call-9]", got[0].OpenCalls)
	}
	if got[0].OpenCalls[0].ToolName != "clarify" {
		t.Fatalf("open call tool = %q, want clarify", got[0].OpenCalls[0].ToolName)
	}
}

func TestListResumablePausesSkips(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	ctx := context.Background()

	// Stale record with open history: skipped by age.
	craftPausedHistory(t, dir, "turn-old", "clarify", "call-1", nil)
	recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-old",
		Gate: hakasesession.PauseGateClarify, CreatedAt: time.Now().Add(-2 * time.Hour),
	})
	// Unaddressed record (no ADK session): display-only, not resumable.
	recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", Gate: hakasesession.PauseGateApproval,
	})
	// Record whose history holds no open call: gate already settled.
	svc, _ := hakasesession.NewDurableADKService(dir)
	cr, _ := svc.Create(ctx, &adksession.CreateRequest{
		AppName: ResumeAppName, UserID: ResumeUserID, SessionID: "turn-settled",
	})
	_ = svc.AppendEvent(ctx, cr.Session, &adksession.Event{
		ID: "e1", Timestamp: time.Now().UTC(), Author: "user",
		LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("hi", genai.RoleUser)},
	})
	recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-settled",
		Gate: hakasesession.PauseGateClarify,
	})
	// Record pointing at pruned history.
	recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-gone",
		Gate: hakasesession.PauseGateClarify,
	})

	got, err := ListResumablePauses(ctx)
	if err != nil {
		t.Fatalf("ListResumablePauses: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resumable = %d, want 0 (all skipped)", len(got))
	}
}

func TestListResumablePausesFeatureOff(t *testing.T) {
	savedDeps := deps
	deps = nil
	t.Cleanup(func() { deps = savedDeps })
	got, err := ListResumablePauses(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("feature off: got=%v err=%v, want nil,nil", got, err)
	}
}

func TestResumeClarifyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	ctx := context.Background()
	// The gate must never run during resume: the answer arrives as the
	// tool result, the handler is not re-executed.
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			t.Error("clarify gate consulted during resume; tool re-executed")
			return interfaces.ClarifyResponse{}, fmt.Errorf("must not block")
		},
	})
	craftPausedHistory(t, dir, "turn-9", "clarify", "call-9", nil)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-9", ADKSessionID: "turn-9",
		Gate:    hakasesession.PauseGateClarify,
		Summary: "clarify: which?",
		Detail:  map[string]any{"question": "which?"},
	})

	clarifyT, err := registerClarifyTool()
	if err != nil {
		t.Fatalf("registerClarifyTool: %v", err)
	}
	m := &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeTextResponse("resumed-after-restart")},
	}}
	agt, err := llmagent.New(llmagent.Config{
		Name: "orchestrator", Model: m, Tools: []tool.Tool{clarifyT},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	svc, err := hakasesession.NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName: ResumeAppName, Agent: agt,
		SessionService: svc, AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	out, err := ResumeClarify(ctx, r, pauseID, ClarifyResponse{Answer: []string{"blue"}})
	if err != nil {
		t.Fatalf("ResumeClarify: %v", err)
	}
	if !strings.Contains(out, "resumed-after-restart") {
		t.Fatalf("resume output = %q, want resumed-after-restart", out)
	}
	if recs, _ := preg.ListForSession("sess-9"); len(recs) != 0 {
		t.Fatalf("pause not unrecorded after resume: %d left", len(recs))
	}
}

func TestResumeClarifyRejectsApprovalPause(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-1",
		Gate: hakasesession.PauseGateApproval,
	})
	if _, err := ResumeClarify(context.Background(), nil, pauseID, ClarifyResponse{}); err == nil {
		t.Fatal("ResumeClarify on approval pause: want error")
	}
}

func TestResolveApprovalPauseDeny(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-1", ADKSessionID: "turn-1",
		Gate:   hakasesession.PauseGateApproval,
		Detail: map[string]any{"tool": "system_exec", "command": "rm -rf /tmp/x"},
	})
	drive, err := ResolveApprovalPause(context.Background(), pauseID, false)
	if err != nil || drive {
		t.Fatalf("deny: drive=%v err=%v, want false,nil", drive, err)
	}
	if recs, _ := preg.ListForSession("sess-1"); len(recs) != 0 {
		t.Fatal("denied pause not unrecorded")
	}
}

func TestResolveApprovalPauseApproveClean(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	ctx := context.Background()
	// History: one settled read-only call, then the open approval call.
	craftPausedHistory(t, dir, "turn-2", "system_exec", "call-2", []string{"read_file"})
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-2", ADKSessionID: "turn-2",
		Gate:   hakasesession.PauseGateApproval,
		Detail: map[string]any{"tool": "system_exec", "command": "ls /tmp"},
	})
	drive, err := ResolveApprovalPause(ctx, pauseID, true)
	if err != nil || !drive {
		t.Fatalf("approve clean: drive=%v err=%v, want true,nil", drive, err)
	}
	// Pre-grant passes the exact command without a gate (rt has none:
	// without the grant this fails closed).
	ok, err := ApproveExec(ctx, ApprovalRequest{Tool: "system_exec", Command: "ls /tmp"})
	if err != nil || !ok {
		t.Fatalf("pre-granted ApproveExec = %v,%v; want true,nil", ok, err)
	}
	// One-shot: a second identical request fails closed again.
	if _, err := ApproveExec(ctx, ApprovalRequest{Tool: "system_exec", Command: "ls /tmp"}); err == nil {
		t.Fatal("pre-grant reused: want fail-closed error on second use")
	}
	// Different command never matched.
	grantApproval("system_exec", "echo scoped")
	if consumePreGrant("system_exec", "echo other") {
		t.Fatal("pre-grant matched a different command")
	}
	if !consumePreGrant("system_exec", "echo scoped") {
		t.Fatal("pre-grant did not match its exact command")
	}
}

func TestResolveApprovalPauseRefusesRiskyHistory(t *testing.T) {
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	ctx := context.Background()
	// The crashed turn already completed a system_exec before blocking
	// on the next approval: re-driving would risk running it twice.
	craftPausedHistory(t, dir, "turn-3", "system_exec", "call-3", []string{"system_exec"})
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-3", ADKSessionID: "turn-3",
		Gate:   hakasesession.PauseGateApproval,
		Detail: map[string]any{"tool": "system_exec", "command": "ls /tmp"},
	})
	if _, err := ResolveApprovalPause(ctx, pauseID, true); err == nil {
		t.Fatal("approve with completed system_exec: want refusal")
	} else if !strings.Contains(err.Error(), "system_exec") {
		t.Fatalf("refusal should name the risky tool, got: %v", err)
	}
	if recs, _ := preg.ListForSession("sess-3"); len(recs) != 1 {
		t.Fatal("refused pause must keep its record for manual handling")
	}
}
