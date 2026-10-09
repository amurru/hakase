package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// queueRouter wires the full RBAC route set for tests.
func queueRouter(gate *WebApprovalGate, roles RoleMap) *chi.Mux {
	r := chi.NewRouter()
	RegisterApprovalRoutesWithRoles(r, gate, roles)
	return r
}

func doReq(t *testing.T, r *chi.Mux, method, target, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if user != "" {
		req.Header.Set("X-Hakase-User", user)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// blockPrompt parks one AskApproval so the queue is non-empty; it returns
// the prompt ID scraped from PendingApprovals and a done channel.
func blockPrompt(t *testing.T, gate *WebApprovalGate, tool string) (string, chan bool) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.AskApproval(context.Background(), interfaces.ApprovalRequest{
			Tool: tool, Command: tool + " run", Risk: "high", Reason: "test",
		})
		done <- approved
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pv := gate.PendingApprovals(); len(pv) > 0 {
			return pv[0].ID, done
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt never appeared in the queue")
	return "", done
}

// TestPendingQueue pins the queue contents and metadata.
func TestPendingQueue(t *testing.T) {
	gate := NewWebApprovalGate(sse.NewEventBridge(), "sess", interfaces.ApprovalConfig{ExpirySeconds: 5})
	id, done := blockPrompt(t, gate, "system_exec")
	defer func() { gate.RespondApproval(id, false); <-done }()

	rr := doReq(t, queueRouter(gate, nil), "GET", "/approvals/pending", "anyone", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET pending = %d, want 200", rr.Code)
	}
	var views []PendingPromptView
	if err := json.Unmarshal(rr.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("queue = %d entries, want 1", len(views))
	}
	v := views[0]
	if v.Tool != "system_exec" || v.Risk != "high" || v.Reason != "test" || v.Command != "system_exec run" {
		t.Errorf("queue entry = %+v, want prompt metadata", v)
	}
	if v.Since == "" {
		t.Error("queue entry missing since timestamp")
	}
}

// TestPendingIncludesResurrected pins re-emitted prompts listed with their
// pause IDs for the mobile queue.
func TestPendingIncludesResurrected(t *testing.T) {
	gate := NewWebApprovalGate(sse.NewEventBridge(), "sess", interfaces.ApprovalConfig{})
	gate.TrackResurrected("appr_old", "pause_9")

	rr := doReq(t, queueRouter(gate, nil), "GET", "/approvals/pending", "anyone", "")
	var views []PendingPromptView
	if err := json.Unmarshal(rr.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 || !views[0].Resurrect || views[0].PauseID != "pause_9" {
		t.Errorf("queue = %+v, want one resurrected entry with pause_9", views)
	}
}

// TestBatchRespondFirstWins pins batch answering delivering to live
// prompts with first-response-wins preserved per prompt.
func TestBatchRespondFirstWins(t *testing.T) {
	gate := NewWebApprovalGate(sse.NewEventBridge(), "sess", interfaces.ApprovalConfig{ExpirySeconds: 5})
	id1, done1 := blockPrompt(t, gate, "tool_a")
	// Park a second prompt and take its (newer) ID: blockPrompt returns
	// the oldest entry, so scan for the ID that is not id1.
	done2ch := make(chan bool, 1)
	go func() {
		approved, _ := gate.AskApproval(context.Background(), interfaces.ApprovalRequest{
			Tool: "tool_b", Command: "tool_b run", Risk: "high", Reason: "test",
		})
		done2ch <- approved
	}()
	id2 := ""
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, pv := range gate.PendingApprovals() {
			if pv.ID != id1 {
				id2 = pv.ID
			}
		}
		if id2 != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id2 == "" {
		t.Fatal("second prompt never appeared in the queue")
	}
	done2 := done2ch
	// Cleanup answers any prompt the batch did not resolve (no-ops when
	// the test already consumed both outcomes below).
	defer func() {
		gate.RespondApproval(id1, false)
		gate.RespondApproval(id2, false)
	}()

	rr := doReq(t, queueRouter(gate, nil), "POST", "/approvals/respond",
		"anyone", `{"ids":[`+jsonStr(id1)+`,`+jsonStr(id2)+`],"approved":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST batch = %d, want 200", rr.Code)
	}
	var out struct {
		Results map[string]bool `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Results[id1] || !out.Results[id2] {
		t.Errorf("results = %v, want both true", out.Results)
	}
	if a := <-done1; !a {
		t.Error("prompt 1 got false, want true (first response wins)")
	}
	if a := <-done2; !a {
		t.Error("prompt 2 got false, want true (first response wins)")
	}

	// Unknown IDs report false without failing the batch.
	rr = doReq(t, queueRouter(gate, nil), "POST", "/approvals/respond",
		"anyone", `{"ids":["appr_nope"],"approved":true}`)
	var out2 struct {
		Results map[string]bool `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out2.Results["appr_nope"] {
		t.Errorf("unknown id result = true, want false")
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestApprovalRBAC pins the tier matrix: viewer reads, approver answers,
// unlisted reads-only, open map allows all.
func TestApprovalRBAC(t *testing.T) {
	newGate := func() *WebApprovalGate {
		return NewWebApprovalGate(sse.NewEventBridge(), "sess", interfaces.ApprovalConfig{ExpirySeconds: 5})
	}
	roles, err := ParseRoleMap(map[string]string{"amy": "admin", "bob": "approver", "cat": "viewer"})
	if err != nil {
		t.Fatalf("ParseRoleMap: %v", err)
	}

	// Open map: everyone admin.
	gate := newGate()
	id, done := blockPrompt(t, gate, "tool_a")
	rr := doReq(t, queueRouter(gate, nil), "POST", "/approvals/"+id+"/respond", "stranger", `{"approved":true}`)
	if rr.Code != http.StatusOK {
		t.Errorf("open map respond = %d, want 200", rr.Code)
	}
	<-done

	// Listed tiers.
	gate = newGate()
	id, done = blockPrompt(t, gate, "tool_a")
	r := queueRouter(gate, roles)
	defer func() { gate.RespondApproval(id, false); <-done }()

	if rr := doReq(t, r, "GET", "/approvals/pending", "cat", ""); rr.Code != http.StatusOK {
		t.Errorf("viewer GET pending = %d, want 200", rr.Code)
	}
	if rr := doReq(t, r, "POST", "/approvals/"+id+"/respond", "cat", `{"approved":true}`); rr.Code != http.StatusForbidden {
		t.Errorf("viewer respond = %d, want 403", rr.Code)
	}
	if rr := doReq(t, r, "POST", "/approvals/respond", "bob", `{"ids":["appr_x"],"approved":true}`); rr.Code != http.StatusOK {
		t.Errorf("approver batch = %d, want 200", rr.Code)
	}
	// Unlisted user: reads like viewer, cannot answer.
	if rr := doReq(t, r, "GET", "/approvals/pending", "mallory", ""); rr.Code != http.StatusOK {
		t.Errorf("unlisted GET pending = %d, want 200", rr.Code)
	}
	if rr := doReq(t, r, "POST", "/approvals/"+id+"/respond", "mallory", `{"approved":true}`); rr.Code != http.StatusForbidden {
		t.Errorf("unlisted respond = %d, want 403", rr.Code)
	}
}

// TestParseRoleMapRejectsUnknown pins strict role validation.
func TestParseRoleMapRejectsUnknown(t *testing.T) {
	if _, err := ParseRoleMap(map[string]string{"amy": "superuser"}); err == nil {
		t.Error("ParseRoleMap accepted superuser, want error")
	}
}
