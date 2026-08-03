package main

import (
	"path/filepath"
	"testing"
)

func newTestSessionService(t *testing.T) *SessionService {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	return svc
}

func TestGetMessagesReturnsPersistedInOrder(t *testing.T) {
	svc := newTestSessionService(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatalf("RecordUsage user: %v", err)
	}
	if err := svc.RecordUsage("agent", "a1", "", 10); err != nil {
		t.Fatalf("RecordUsage agent: %v", err)
	}

	id := svc.ActiveSessionID()
	if id == "" {
		t.Fatal("active session id empty")
	}
	msgs, err := svc.GetMessages(id)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "q1" {
		t.Fatalf("msgs[0] = %+v, want user/q1", msgs[0])
	}
	if msgs[1].Role != "agent" || msgs[1].Content != "a1" {
		t.Fatalf("msgs[1] = %+v, want agent/a1", msgs[1])
	}
	if msgs[0].Sequence != 0 || msgs[1].Sequence != 1 {
		t.Fatalf("sequences = %d,%d want 0,1", msgs[0].Sequence, msgs[1].Sequence)
	}
	if msgs[0].Tokens != 5 || msgs[1].Tokens != 10 {
		t.Fatalf("tokens = %d,%d want 5,10", msgs[0].Tokens, msgs[1].Tokens)
	}
}

func TestGetMessagesEmptyID(t *testing.T) {
	svc := newTestSessionService(t)
	msgs, err := svc.GetMessages("")
	if err != nil {
		t.Fatalf("GetMessages empty: %v", err)
	}
	if msgs != nil {
		t.Fatalf("GetMessages empty = %v, want nil", msgs)
	}
}

func TestRecordUsageCreatesSessionAndSetsTokens(t *testing.T) {
	svc := newTestSessionService(t)
	if svc.HasActiveSession() {
		t.Fatal("should start with no active session")
	}
	if err := svc.RecordUsage("user", "first message here", "", 42); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if !svc.HasActiveSession() {
		t.Fatal("active session should be created")
	}
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("GetActiveSession = %v, %v", session, err)
	}
	if session.Title == "" {
		t.Fatal("session title should be derived from content")
	}
	if len(session.Messages) != 1 || session.Messages[0].Tokens != 42 {
		t.Fatalf("messages = %+v, want 1 message with 42 tokens", session.Messages)
	}
	if !session.Messages[0].InContext {
		t.Fatal("recorded message must be in-context")
	}
}

func TestSetSummaryRoundTrips(t *testing.T) {
	svc := newTestSessionService(t)
	if err := svc.RecordUsage("user", "q", "", 1); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	id := svc.ActiveSessionID()
	if err := svc.SetSummary(id, "3"); err != nil {
		t.Fatalf("SetSummary: %v", err)
	}
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("GetActiveSession = %v, %v", session, err)
	}
	if session.SummaryMessageID != "3" {
		t.Fatalf("SummaryMessageID = %q, want 3", session.SummaryMessageID)
	}
}

func TestAppendSummaryPersistsKindAndID(t *testing.T) {
	svc := newTestSessionService(t)
	if err := svc.RecordUsage("user", "q", "", 1); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	id := svc.ActiveSessionID()
	if err := svc.AppendSummary(id, "running summary text"); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	msgs, err := svc.GetMessages(id)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	sum := msgs[1]
	if sum.Kind != MessageKindSummary {
		t.Fatalf("kind = %q, want summary", sum.Kind)
	}
	if sum.Role != "agent" {
		t.Fatalf("role = %q, want agent", sum.Role)
	}
	if !sum.InContext {
		t.Fatal("summary must be in-context for re-injection")
	}
	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("GetActiveSession: %v, %v", session, err)
	}
	if session.SummaryMessageID != "1" {
		t.Fatalf("SummaryMessageID = %q, want 1 (sequence of summary)", session.SummaryMessageID)
	}
}
