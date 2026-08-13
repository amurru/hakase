package tui

import (
	"context"
	"testing"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
)

// fakeHerdr records the last Report/Release call so tui state derivation can
// be asserted without exec'ing the Herdr CLI.
type fakeHerdr struct {
	lastState string
	lastMsg   string
	lastSess  string
	released  bool
}

func (f *fakeHerdr) Report(state, message, sessionID string) {
	f.lastState = state
	f.lastMsg = message
	f.lastSess = sessionID
}

func (f *fakeHerdr) Release() { f.released = true }

func newHerdrTestModel() (AppModel, *fakeHerdr) {
	m := newModel(context.Background(), nil, nil, 100, true, "test-model", "")
	f := &fakeHerdr{}
	m.SetHerdrReporter(f)
	return m, f
}

func TestReportAgentStateIdle(t *testing.T) {
	m, f := newHerdrTestModel()
	m.reportAgentState()
	if f.lastState != "idle" {
		t.Fatalf("state = %q, want idle", f.lastState)
	}
}

func TestReportAgentStateWorking(t *testing.T) {
	m, f := newHerdrTestModel()
	m.IsProcessing = true
	m.reportAgentState()
	if f.lastState != "working" {
		t.Fatalf("state = %q, want working", f.lastState)
	}
}

func TestReportAgentStateBlockedClarify(t *testing.T) {
	m, f := newHerdrTestModel()
	m.IsProcessing = true
	m.pendingClarify = &clarifyPromptMsg{Req: hakaseagent.ClarifyRequest{Question: "Pick a color?"}}
	m.reportAgentState()
	if f.lastState != "blocked" {
		t.Fatalf("state = %q, want blocked", f.lastState)
	}
	if f.lastMsg != "Pick a color?" {
		t.Fatalf("message = %q, want %q", f.lastMsg, "Pick a color?")
	}
}

func TestReportAgentStateBlockedApproval(t *testing.T) {
	m, f := newHerdrTestModel()
	m.IsProcessing = true
	m.pendingApproval = &approvalPromptMsg{Req: interfaces.ApprovalRequest{Reason: "Needs sudo"}}
	m.reportAgentState()
	if f.lastState != "blocked" {
		t.Fatalf("state = %q, want blocked", f.lastState)
	}
	if f.lastMsg != "Needs sudo" {
		t.Fatalf("message = %q, want %q", f.lastMsg, "Needs sudo")
	}
}

func TestReportAgentStateApprovalFallsBackToTool(t *testing.T) {
	m, f := newHerdrTestModel()
	m.IsProcessing = true
	m.pendingApproval = &approvalPromptMsg{Req: interfaces.ApprovalRequest{Tool: "system_exec"}}
	m.reportAgentState()
	if f.lastMsg != "approval: system_exec" {
		t.Fatalf("message = %q, want %q", f.lastMsg, "approval: system_exec")
	}
}

func TestReportAgentStateWorkingWithSession(t *testing.T) {
	store, err := hakasesession.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}
	// Create an active session so ActiveSessionID() is non-empty.
	if err := svc.RecordUsage("user", "hello", "", 1); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	m, f := newHerdrTestModel()
	m.sessionService = svc
	m.IsProcessing = true
	m.reportAgentState()
	if f.lastState != "working" {
		t.Fatalf("state = %q, want working", f.lastState)
	}
	if f.lastSess == "" {
		t.Fatal("expected non-empty session id in report")
	}
}

func TestHerdrReleaseNoopWithoutReporter(t *testing.T) {
	m := newModel(context.Background(), nil, nil, 100, true, "test-model", "")
	m.HerdrRelease() // must not panic
}

func TestHerdrReleaseWithReporter(t *testing.T) {
	m, f := newHerdrTestModel()
	m.HerdrRelease()
	if !f.released {
		t.Fatal("Release() not called on reporter")
	}
}
