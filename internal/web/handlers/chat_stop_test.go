package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postStop issues POST /sessions/{id}/stop against a chi router wiring the
// real PostStop handler.
func postStop(t *testing.T, api *ChatAPI, r interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/stop", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Stop with no tracked run is a 404, not a cancel of nothing.
func TestPostStopWithoutRun(t *testing.T) {
	api, _, _, r := newTestChatAPI(t)
	r.Post("/sessions/{id}/stop", api.PostStop)

	w := postStop(t, api, r, "ses-idle")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no active run, got %d (%s)", w.Code, w.Body.String())
	}
}

// A tracked run's context is canceled by stopRuns, and untracked runs stop
// reporting as active.
func TestTrackRunStopRuns(t *testing.T) {
	api, _, _, _ := newTestChatAPI(t)

	runCtx, untrack := api.trackRun("ses-1", context.Background())
	defer untrack()
	select {
	case <-runCtx.Done():
		t.Fatal("run context canceled before stop")
	default:
	}

	if !api.stopRuns("ses-1") {
		t.Fatal("stopRuns should report a tracked run")
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("run context not canceled by stopRuns")
	}
	if api.stopRuns("ses-1") {
		t.Fatal("stopRuns should report nothing after the run was stopped")
	}
	untrack()
	if api.stopRuns("ses-1") {
		t.Fatal("stopRuns should report nothing after untrack")
	}
}

// A stop arriving before the run goroutine's first line must still cancel
// the accepted run: PostMessage pre-registers the run ctx, and the run body
// derives its tracked ctx from it, so pre-cancellation propagates.
func TestStopBeforeRunStarts(t *testing.T) {
	api, _, _, _ := newTestChatAPI(t)

	runCtx, untrack := api.trackRun("ses-race", context.Background())
	defer untrack()
	if !api.stopRuns("ses-race") {
		t.Fatal("stop should report the pre-registered run")
	}
	child, childDone := api.trackRun("ses-race", runCtx)
	defer childDone()
	select {
	case <-child.Done():
	default:
		t.Fatal("run starting after a pre-stop must observe cancellation")
	}
}

// PostStop cancels a tracked run and answers 202 with the session id.
func TestPostStopCancelsRun(t *testing.T) {
	api, _, _, r := newTestChatAPI(t)
	r.Post("/sessions/{id}/stop", api.PostStop)

	runCtx, untrack := api.trackRun("ses-9", context.Background())
	defer untrack()

	w := postStop(t, api, r, "ses-9")
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["session_id"] != "ses-9" {
		t.Fatalf("expected session_id ses-9, got %q", body["session_id"])
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("tracked run was not canceled by PostStop")
	}
}
