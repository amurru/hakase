package session

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
)

// newTestADKService returns a durable service rooted in a temp dir.
func newTestADKService(t *testing.T) *DurableADKService {
	t.Helper()
	svc, err := NewDurableADKService(t.TempDir())
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	return svc
}

func testCreateReq() *adksession.CreateRequest {
	return &adksession.CreateRequest{
		AppName:   "hakase_harness",
		UserID:    "user-1",
		SessionID: "test-session-1",
		State:     map[string]any{"k": "v"},
	}
}

// testPauseEvent builds an event shaped like a gate pause: a tool call
// carrying LongRunningToolIDs plus RequestedInput and InvocationID —
// the fields resume depends on (RQs 2-3).
func testPauseEvent() *adksession.Event {
	return &adksession.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "appr_123",
						Name: "system_exec",
						Args: map[string]any{"command": "rm -rf /tmp/x"},
					},
				}},
			},
		},
		ID:                 "evt-pause-1",
		Timestamp:          time.Now().UTC(),
		InvocationID:       "inv-1",
		Author:             "orchestrator",
		LongRunningToolIDs: []string{"appr_123"},
		RequestedInput: &adksession.RequestInput{
			InterruptID: "appr_123",
			Message:     "Approve?",
			Payload:     map[string]any{"command": "rm -rf /tmp/x"},
		},
	}
}

func TestDurableADKService_CreateGet(t *testing.T) {
	ctx := context.Background()
	svc := newTestADKService(t)
	if _, err := svc.Create(ctx, testCreateReq()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Session.ID() != "test-session-1" {
		t.Errorf("ID = %q", got.Session.ID())
	}
	v, err := got.Session.State().Get("k")
	if err != nil || v != "v" {
		t.Errorf("state k = %v, %v", v, err)
	}
}

// TestDurableADKService_RestartRecovery is the core Phase 2 test:
// events appended before a simulated restart (fresh service instance
// over the same dir) are recovered with resume-critical fields intact.
func TestDurableADKService_RestartRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc1, err := NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}
	if _, err := svc1.Create(ctx, testCreateReq()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	createResp, err := svc1.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := svc1.AppendEvent(ctx, createResp.Session, testPauseEvent()); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Simulate restart: brand-new service instance, same directory.
	svc2, err := NewDurableADKService(dir)
	if err != nil {
		t.Fatalf("NewDurableADKService (restart): %v", err)
	}
	got, err := svc2.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Session.Events().Len() != 1 {
		t.Fatalf("events after restart = %d, want 1", got.Session.Events().Len())
	}
	ev := got.Session.Events().At(0)
	if len(ev.LongRunningToolIDs) != 1 || ev.LongRunningToolIDs[0] != "appr_123" {
		t.Errorf("LongRunningToolIDs = %v", ev.LongRunningToolIDs)
	}
	if ev.RequestedInput == nil || ev.RequestedInput.InterruptID != "appr_123" {
		t.Errorf("RequestedInput = %+v", ev.RequestedInput)
	}
	if ev.InvocationID != "inv-1" {
		t.Errorf("InvocationID = %q", ev.InvocationID)
	}
	if ev.Author != "orchestrator" {
		t.Errorf("Author = %q", ev.Author)
	}
}

func TestDurableADKService_PartialSkippedAndIDAssigned(t *testing.T) {
	ctx := context.Background()
	svc := newTestADKService(t)
	cr, err := svc.Create(ctx, testCreateReq())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Partial events are never stored (and get no ID).
	partial := &adksession.Event{LLMResponse: model.LLMResponse{Partial: true}}
	if err := svc.AppendEvent(ctx, cr.Session, partial); err != nil {
		t.Fatalf("AppendEvent partial: %v", err)
	}
	if partial.ID != "" {
		t.Errorf("partial event got ID %q, want empty", partial.ID)
	}
	// ID-less events are named in place on the caller's event.
	ev := testPauseEvent()
	ev.ID = ""
	if err := svc.AppendEvent(ctx, cr.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if ev.ID == "" {
		t.Error("ID-less event was not assigned an ID in place")
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Session.Events().Len() != 1 {
		t.Errorf("events = %d, want 1 (partial skipped)", got.Session.Events().Len())
	}
}

func TestDurableADKService_TempKeysTrimmed(t *testing.T) {
	ctx := context.Background()
	svc := newTestADKService(t)
	cr, err := svc.Create(ctx, testCreateReq())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ev := testPauseEvent()
	ev.Actions.StateDelta = map[string]any{
		"temp:scratch": "drop me",
		"keep":         "me",
	}
	if err := svc.AppendEvent(ctx, cr.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored := got.Session.Events().At(0)
	if _, ok := stored.Actions.StateDelta["temp:scratch"]; ok {
		t.Error("temp: key survived in stored event")
	}
	if stored.Actions.StateDelta["keep"] != "me" {
		t.Error("non-temp delta key lost")
	}
}

func TestDurableADKService_ScopedState(t *testing.T) {
	ctx := context.Background()
	svc := newTestADKService(t)
	cr, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "s-scoped",
		State: map[string]any{"app:theme": "dark", "user:lang": "en"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "s-scoped",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, err := got.Session.State().Get("app:theme"); err != nil || v != "dark" {
		t.Errorf("app:theme = %v, %v", v, err)
	}
	// A second session under the same app/user sees the shared scoped keys.
	if _, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "s-scoped-2",
	}); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	got2, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "s-scoped-2",
	})
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if v, err := got2.Session.State().Get("user:lang"); err != nil || v != "en" {
		t.Errorf("shared user:lang = %v, %v", v, err)
	}
	_ = cr
}

func TestDurableADKService_GetFiltersDeleteList(t *testing.T) {
	ctx := context.Background()
	svc := newTestADKService(t)
	if _, err := svc.Create(ctx, testCreateReq()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	sess := got.Session
	for i := 0; i < 3; i++ {
		ev := testPauseEvent()
		ev.ID = ""
		ev.Timestamp = time.Now().UTC().Add(time.Duration(i) * time.Second)
		if err := svc.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	recent, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
		NumRecentEvents: 2,
	})
	if err != nil {
		t.Fatalf("Get recent: %v", err)
	}
	if recent.Session.Events().Len() != 2 {
		t.Errorf("NumRecentEvents=2 returned %d", recent.Session.Events().Len())
	}

	list, err := svc.List(ctx, &adksession.ListRequest{AppName: "hakase_harness", UserID: "user-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("List = %d sessions, want 1", len(list.Sessions))
	}
	if list.Sessions[0].Events().Len() != 0 {
		t.Error("List sessions should carry no events")
	}

	if _, err := svc.Create(ctx, testCreateReq()); err == nil {
		t.Error("duplicate Create should fail")
	}
	if err := svc.Delete(ctx, &adksession.DeleteRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	}); err == nil {
		t.Error("Get after Delete should fail")
	}
	// Deleting again is a no-op, mirroring the engine.
	if err := svc.Delete(ctx, &adksession.DeleteRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	}); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

// TestDurableADKService_Conformance runs the engine's shared
// backend-agnostic suite against the durable service.
func TestDurableADKService_Conformance(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
		ProvidesServerAssignedEventID: false,
		AppName:                       "hakase_harness",
	}, func(t *testing.T) adksession.Service {
		svc, err := NewDurableADKService(t.TempDir())
		if err != nil {
			t.Fatalf("NewDurableADKService: %v", err)
		}
		return svc
	})
}

// TestDurableADKService_AppendEventUpdatesPassedSession pins issue #110:
// the ADK node runtime builds each model request's contents from the
// session object it holds, so AppendEvent must make the appended event
// observable on the passed session, not only on the next Get. Temp state
// keys stay trimmed from that view, and the session-scoped state delta
// lands on the session's state map the way the engine's in-memory service
// does.
func TestDurableADKService_AppendEventUpdatesPassedSession(t *testing.T) {
	svc := newTestADKService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, testCreateReq())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ev := &adksession.Event{
		ID:           "evt-1",
		Timestamp:    time.Now().UTC(),
		InvocationID: "inv-1",
		Author:       "user",
		Actions: adksession.EventActions{StateDelta: map[string]any{
			"temp:hakase.test": "ephemeral",
			"kept":             "yes",
		}},
		LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("hi", genai.RoleUser)},
	}
	if err := svc.AppendEvent(ctx, created.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if got := created.Session.Events().Len(); got != 1 {
		t.Fatalf("passed session events = %d, want 1 (in-flight append invisible)", got)
	}
	stored := created.Session.Events().At(0)
	if stored == nil || stored.ID != "evt-1" {
		t.Fatalf("passed session event = %+v, want evt-1", stored)
	}
	if _, leaked := stored.Actions.StateDelta["temp:hakase.test"]; leaked {
		t.Fatalf("temp state delta leaked into the passed session view: %+v", stored.Actions.StateDelta)
	}
	if stored.Actions.StateDelta["kept"] != "yes" {
		t.Fatalf("session state delta missing from the passed session view: %+v", stored.Actions.StateDelta)
	}
	if v, err := created.Session.State().Get("kept"); err != nil || v != "yes" {
		t.Fatalf("session state kept = %v, %v; want yes", v, err)
	}

	// The durable record still agrees after a fresh Get.
	got, err := svc.Get(ctx, &adksession.GetRequest{
		AppName: "hakase_harness", UserID: "user-1", SessionID: "test-session-1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Session.Events().Len() != 1 || got.Session.Events().At(0).ID != "evt-1" {
		t.Fatalf("re-read record diverged: %+v", got.Session.Events())
	}
}
