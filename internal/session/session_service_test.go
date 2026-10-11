package session

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/finops"
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

func TestRenameSessionUpdatesTitleOnly(t *testing.T) {
	svc := newTestSessionService(t)
	sess, err := svc.CreateSession("Original")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.AddMessage("user", "hello", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := svc.RenameSession(sess.ID, "Renamed"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}

	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "Renamed" {
		t.Fatalf("title = %q, want Renamed", loaded.Title)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (rename must not touch messages)", len(loaded.Messages))
	}
}

func TestRenameSessionValidation(t *testing.T) {
	svc := newTestSessionService(t)
	sess, err := svc.CreateSession("Keep")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := svc.RenameSession("", "x"); err == nil {
		t.Error("expected error for empty id")
	}
	if err := svc.RenameSession(sess.ID, ""); err == nil {
		t.Error("expected error for empty title")
	}

	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "Keep" {
		t.Fatalf("title = %q, want Keep after failed renames", loaded.Title)
	}
}

func TestArchiveSessionClearsActivePointer(t *testing.T) {
	svc := newTestSessionService(t)
	sess, err := svc.CreateSession("Active one")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if svc.ActiveSessionID() != sess.ID {
		t.Fatal("created session should be active")
	}

	if err := svc.ArchiveSession(sess.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if svc.ActiveSessionID() != "" {
		t.Fatalf("active id = %q, want empty after archiving the active session", svc.ActiveSessionID())
	}
}

// TestAppendSummaryEvictsPriorSummaryAndLogsRewrite pins PC-006: the prior
// summary is evicted in the same save as the new one, and the rewrite is
// logged once with direction, tokens before/after, and reason.
func TestAppendSummaryEvictsPriorSummaryAndLogsRewrite(t *testing.T) {
	svc := newTestSessionService(t)
	if err := svc.RecordUsage("user", "q1", "", 5); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()

	var addBuf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&addBuf)
	defer log.SetOutput(old)
	if err := svc.AppendSummary(id, "SUMMARY ONE"); err != nil {
		t.Fatal(err)
	}
	if add := addBuf.String(); !strings.Contains(add, "direction=add") || !strings.Contains(add, "reason=summary") {
		t.Fatalf("first-summary rewrite log = %q, want direction=add", add)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)

	if err := svc.AppendSummary(id, "SUMMARY TWO"); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if !strings.Contains(got, "cache: rewrite direction=replace") || !strings.Contains(got, "reason=summary") {
		t.Fatalf("summary rewrite log missing fields: %q", got)
	}

	session, err := svc.Store().Load(id)
	if err != nil {
		t.Fatal(err)
	}
	inContext := 0
	for _, m := range session.Messages {
		if m.Kind == MessageKindSummary && m.InContext {
			inContext++
			if m.Content != "SUMMARY TWO" {
				t.Fatalf("in-context summary = %q, want the newest", m.Content)
			}
		}
	}
	if inContext != 1 {
		t.Fatalf("in-context summaries = %d, want exactly 1", inContext)
	}
}

// TestDeleteSessionClearsCacheGuardState pins the PC-007 cleanup wiring: the
// once-per-session guard state must not outlive the session it belongs to.
func TestDeleteSessionClearsCacheGuardState(t *testing.T) {
	svc := newTestSessionService(t)
	if err := svc.RecordUsage("user", "q", "", 5); err != nil {
		t.Fatal(err)
	}
	id := svc.ActiveSessionID()
	rec := finops.UsageRecord{Prompt: 5000, Total: 6000}
	if ok, _ := finops.CacheWarn(id, rec, 0.8, "gemini"); !ok {
		t.Fatal("guard should fire before the session is deleted")
	}
	if err := svc.DeleteSession(id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := finops.CacheWarn(id, rec, 0.8, "gemini"); !ok {
		t.Fatal("deleting the session must clear its guard state")
	}
}
