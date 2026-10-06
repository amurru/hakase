// resume_test.go - Phase 7 transport resurrection tests.
//
// The settle logic itself (ResumeClarify, ResolveApprovalPause,
// replay guard) is pinned in internal/agent; these tests cover the
// web layer: resurrected-prompt tracking, respond fallthrough to the
// resume backend, the /resumable off-shape, and the pure mapping
// helpers.
package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestResurrectedTrackingRoundtrip(t *testing.T) {
	bridge := sse.NewEventBridge()
	ag := NewWebApprovalGate(bridge, "", interfaces.ApprovalConfig{})
	if _, ok := ag.ResurrectedPause("rsm_x"); ok {
		t.Fatal("unknown resurrected prompt should miss")
	}
	ag.TrackResurrected("rsm_x", "pause-1")
	if id, ok := ag.ResurrectedPause("rsm_x"); !ok || id != "pause-1" {
		t.Fatalf("ResurrectedPause = %q,%v; want pause-1,true", id, ok)
	}
	ag.DropResurrected("rsm_x")
	if _, ok := ag.ResurrectedPause("rsm_x"); ok {
		t.Fatal("dropped prompt should miss")
	}
	// Nil-map read safety on a fresh gate.
	cg := NewWebClarifyGate(bridge, "", interfaces.ClarifyConfig{})
	if _, ok := cg.ResurrectedPause("rsm_y"); ok {
		t.Fatal("fresh clarify gate should miss")
	}
	if cg.ResumeBackend() != nil || ag.ResumeBackend() != nil {
		t.Fatal("backend should default to nil (feature unwired)")
	}
}

// stubResumeBackend records fallthrough calls with canned statuses.
type stubResumeBackend struct {
	status int
	body   map[string]string
	calls  int
	lastID string
}

func (s *stubResumeBackend) AnswerResurrectedApproval(_ context.Context, promptID string, _ bool) (int, map[string]string) {
	s.calls++
	s.lastID = promptID
	return s.status, s.body
}

func (s *stubResumeBackend) AnswerResurrectedClarify(_ context.Context, promptID string, _ interfaces.ClarifyResponse) (int, map[string]string) {
	s.calls++
	s.lastID = promptID
	return s.status, s.body
}

func TestRespondApprovalFallsThroughToResume(t *testing.T) {
	bridge := sse.NewEventBridge()
	gate := NewWebApprovalGate(bridge, "", interfaces.ApprovalConfig{})
	stub := &stubResumeBackend{status: http.StatusAccepted, body: map[string]string{"status": "redriving"}}
	gate.SetResumeBackend(stub)
	r := chi.NewRouter()
	RegisterApprovalRoutes(r, gate)

	body, _ := json.Marshal(map[string]bool{"approved": true})
	req := httptest.NewRequest("POST", "/approvals/rsm_pause-9/respond", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if stub.calls != 1 || stub.lastID != "rsm_pause-9" {
		t.Fatalf("backend calls = %d lastID = %q", stub.calls, stub.lastID)
	}
}

func TestRespondApprovalUnknownWithoutBackend(t *testing.T) {
	bridge := sse.NewEventBridge()
	gate := NewWebApprovalGate(bridge, "", interfaces.ApprovalConfig{})
	r := chi.NewRouter()
	RegisterApprovalRoutes(r, gate)

	body, _ := json.Marshal(map[string]bool{"approved": true})
	req := httptest.NewRequest("POST", "/approvals/nope/respond", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestRespondClarifyFallsThroughToResume(t *testing.T) {
	bridge := sse.NewEventBridge()
	gate := NewWebClarifyGate(bridge, "", interfaces.ClarifyConfig{})
	stub := &stubResumeBackend{status: http.StatusAccepted, body: map[string]string{"status": "resuming"}}
	gate.SetResumeBackend(stub)
	r := chi.NewRouter()
	RegisterClarifyRoutes(r, gate)

	body, _ := json.Marshal(map[string]string{"answer": "blue"})
	req := httptest.NewRequest("POST", "/clarifications/rsm_pause-3/respond", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if stub.calls != 1 || stub.lastID != "rsm_pause-3" {
		t.Fatalf("backend calls = %d lastID = %q", stub.calls, stub.lastID)
	}
}

func TestGetResumableFeatureOffIsEmpty(t *testing.T) {
	// Agent globals are unwired in this test binary (SetupRunner never
	// ran): the registry is nil, so the listing is empty, not an error.
	api, _, _, r := newTestChatAPI(t)
	r.Get("/resumable", api.GetResumable)
	req := httptest.NewRequest("GET", "/resumable", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got struct {
		Pauses []resumablePauseJSON `json:"pauses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Pauses == nil || len(got.Pauses) != 0 {
		t.Fatalf("pauses = %+v, want empty list", got.Pauses)
	}
}

func TestResumeStatusMapping(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{"durable_resume: pause gone not found", http.StatusNotFound},
		{"durable_resume: pause old not resumable (answered, expired, or settled)", http.StatusNotFound},
		{"durable_resume: pause old expired", http.StatusGone},
		{"durable_resume: pause q unsafe to re-drive: completed side-effecting calls [system_exec] would replay", http.StatusConflict},
		{"durable_resume: read paused session: boom", http.StatusInternalServerError},
	}
	for _, c := range cases {
		status, body := resumeStatus(errString(c.msg))
		if status != c.want {
			t.Errorf("resumeStatus(%q) = %d, want %d", c.msg, status, c.want)
		}
		if _, ok := body["error"]; !ok {
			t.Errorf("resumeStatus(%q) body lacks error: %v", c.msg, body)
		}
	}
	if terminalResumeErr(errString("durable_resume: pause q unsafe to re-drive")) {
		t.Error("unsafe pauses must keep their mapping for manual handling")
	}
	if !terminalResumeErr(errString("durable_resume: pause q expired")) {
		t.Error("expired pauses should drop their mapping")
	}
}

func TestDetailConverters(t *testing.T) {
	detail := map[string]any{
		"tool": "system_exec", "command": "ls",
		"question": "which?", "choices": []any{"a", "b", 3},
		"multi_select": true,
	}
	if got := strDetail(detail, "tool"); got != "system_exec" {
		t.Errorf("strDetail tool = %q", got)
	}
	if got := strDetail(detail, "missing"); got != "" {
		t.Errorf("strDetail missing = %q, want empty", got)
	}
	if got := sliceDetail(detail, "choices"); len(got) != 2 || got[1] != "b" {
		t.Errorf("sliceDetail choices = %v (non-strings dropped)", got)
	}
	if got := sliceDetail(detail, "missing"); got != nil {
		t.Errorf("sliceDetail missing = %v, want nil", got)
	}
	if !boolDetail(detail, "multi_select") || boolDetail(detail, "missing") {
		t.Error("boolDetail mismatch")
	}
}

// errString is a test error value.
type errString string

func (e errString) Error() string { return string(e) }
