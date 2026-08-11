package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// TestWebApprovalGate_AskApproval_Approved tests the full approval flow:
// gate asks approval, SSE receives event, HTTP respond approves, gate returns true.
func TestWebApprovalGate_AskApproval_Approved(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ApprovalConfig{ExpirySeconds: 5}
	gate := NewWebApprovalGate(bridge, sessionID, cfg)

	// Subscribe to SSE events.
	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	// Start AskApproval in a goroutine.
	req := interfaces.ApprovalRequest{
		Tool:    "system_exec",
		Command: "rm -rf /tmp/test",
		Risk:    "high",
		Reason:  "destructive command",
	}
	done := make(chan bool, 1)
	go func() {
		approved, err := gate.AskApproval(req)
		if err != nil {
			t.Errorf("AskApproval error: %v", err)
		}
		done <- approved
	}()

	// Wait for SSE approval event.
	select {
	case data := <-ch:
		event := string(data)
		if !strings.Contains(event, "event: approval") {
			t.Fatalf("expected approval event, got: %s", event)
		}
		// Parse the approval ID from the event.
		var payload map[string]any
		lines := strings.Split(event, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				jsonStr := strings.TrimPrefix(line, "data: ")
				if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
					t.Fatalf("failed to parse approval payload: %v", err)
				}
				break
			}
		}
		approvalID, ok := payload["id"].(string)
		if !ok || approvalID == "" {
			t.Fatalf("missing approval ID in payload: %v", payload)
		}

		// Send HTTP response to approve.
		body, _ := json.Marshal(map[string]bool{"approved": true})
		httpReq := httptest.NewRequest("POST", "/approvals/"+approvalID+"/respond", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		// Create a router with the approval route.
		r := chi.NewRouter()
		RegisterApprovalRoutes(r, gate)
		r.ServeHTTP(w, httpReq)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for approval SSE event")
	}

	// Wait for AskApproval to return.
	select {
	case approved := <-done:
		if !approved {
			t.Fatal("expected approved=true, got false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for AskApproval to return")
	}
}

// TestWebApprovalGate_AskApproval_Denied tests the denial flow.
func TestWebApprovalGate_AskApproval_Denied(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ApprovalConfig{ExpirySeconds: 5}
	gate := NewWebApprovalGate(bridge, sessionID, cfg)

	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	req := interfaces.ApprovalRequest{
		Tool:    "system_exec",
		Command: "ls",
		Risk:    "low",
	}
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.AskApproval(req)
		done <- approved
	}()

	// Wait for SSE event and respond with denial.
	select {
	case data := <-ch:
		var payload map[string]any
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload)
				break
			}
		}
		approvalID := payload["id"].(string)

		body, _ := json.Marshal(map[string]bool{"approved": false})
		httpReq := httptest.NewRequest("POST", "/approvals/"+approvalID+"/respond", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r := chi.NewRouter()
		RegisterApprovalRoutes(r, gate)
		r.ServeHTTP(w, httpReq)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for approval SSE event")
	}

	select {
	case approved := <-done:
		if approved {
			t.Fatal("expected approved=false, got true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for AskApproval to return")
	}
}

// TestWebApprovalGate_Timeout tests the timeout flow: no response, gate returns false.
func TestWebApprovalGate_Timeout(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ApprovalConfig{ExpirySeconds: 1} // Short expiry for testing.
	gate := NewWebApprovalGate(bridge, sessionID, cfg)

	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	req := interfaces.ApprovalRequest{
		Tool:    "system_exec",
		Command: "ls",
	}
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.AskApproval(req)
		done <- approved
	}()

	// Wait for SSE approval event.
	select {
	case data := <-ch:
		if !strings.Contains(string(data), "event: approval") {
			t.Fatalf("expected approval event, got: %s", string(data))
		}
		// Do NOT respond - let it timeout.

	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for approval SSE event")
	}

	// Wait for AskApproval to return (should be false due to timeout).
	select {
	case approved := <-done:
		if approved {
			t.Fatal("expected approved=false on timeout, got true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for AskApproval to return after timeout")
	}

	// Check for approval_timeout SSE event.
	select {
	case data := <-ch:
		if !strings.Contains(string(data), "event: approval_timeout") {
			t.Fatalf("expected approval_timeout event, got: %s", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for approval_timeout SSE event")
	}
}

// TestWebApprovalGate_UnknownID tests responding to an unknown/expired approval ID.
func TestWebApprovalGate_UnknownID(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ApprovalConfig{ExpirySeconds: 5}
	gate := NewWebApprovalGate(bridge, sessionID, cfg)

	body, _ := json.Marshal(map[string]bool{"approved": true})
	httpReq := httptest.NewRequest("POST", "/approvals/unknown-id/respond", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	RegisterApprovalRoutes(r, gate)
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown approval ID, got %d", w.Code)
	}
}

// TestWebClarifyGate_AskClarify_Choices tests the clarify flow with choices.
func TestWebClarifyGate_AskClarify_Choices(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ClarifyConfig{ExpirySeconds: 5}
	gate := NewWebClarifyGate(bridge, sessionID, cfg)

	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	req := interfaces.ClarifyRequest{
		Question:    "Which option?",
		Choices:     []string{"option1", "option2", "option3"},
		MultiSelect: false,
	}
	done := make(chan interfaces.ClarifyResponse, 1)
	go func() {
		resp, _ := gate.AskClarify(req)
		done <- resp
	}()

	// Wait for SSE clarify event.
	select {
	case data := <-ch:
		if !strings.Contains(string(data), "event: clarify") {
			t.Fatalf("expected clarify event, got: %s", string(data))
		}
		var payload map[string]any
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload)
				break
			}
		}
		clarifyID := payload["id"].(string)

		// Respond with choices.
		body, _ := json.Marshal(map[string][]string{"choices": {"option2"}})
		httpReq := httptest.NewRequest("POST", "/clarifications/"+clarifyID+"/respond", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r := chi.NewRouter()
		RegisterClarifyRoutes(r, gate)
		r.ServeHTTP(w, httpReq)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for clarify SSE event")
	}

	// Wait for AskClarify to return.
	select {
	case resp := <-done:
		if len(resp.Answer) != 1 || resp.Answer[0] != "option2" {
			t.Fatalf("expected answer=[option2], got: %v", resp.Answer)
		}
		if resp.TimedOut {
			t.Fatal("expected TimedOut=false, got true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for AskClarify to return")
	}
}

// TestWebClarifyGate_AskClarify_FreeText tests the clarify flow with free text.
func TestWebClarifyGate_AskClarify_FreeText(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ClarifyConfig{ExpirySeconds: 5}
	gate := NewWebClarifyGate(bridge, sessionID, cfg)

	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	req := interfaces.ClarifyRequest{
		Question: "What is your name?",
	}
	done := make(chan interfaces.ClarifyResponse, 1)
	go func() {
		resp, _ := gate.AskClarify(req)
		done <- resp
	}()

	// Wait for SSE event and respond with free text.
	select {
	case data := <-ch:
		var payload map[string]any
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload)
				break
			}
		}
		clarifyID := payload["id"].(string)

		body, _ := json.Marshal(map[string]string{"answer": "Alice"})
		httpReq := httptest.NewRequest("POST", "/clarifications/"+clarifyID+"/respond", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r := chi.NewRouter()
		RegisterClarifyRoutes(r, gate)
		r.ServeHTTP(w, httpReq)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for clarify SSE event")
	}

	select {
	case resp := <-done:
		if len(resp.Answer) != 1 || resp.Answer[0] != "Alice" {
			t.Fatalf("expected answer=[Alice], got: %v", resp.Answer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for AskClarify to return")
	}
}

// TestWebClarifyGate_Timeout tests the clarify timeout flow.
func TestWebClarifyGate_Timeout(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ClarifyConfig{ExpirySeconds: 1} // Short expiry.
	gate := NewWebClarifyGate(bridge, sessionID, cfg)

	subID, ch := bridge.Subscribe(sessionID)
	defer bridge.Unsubscribe(sessionID, subID)

	req := interfaces.ClarifyRequest{
		Question: "Quick question?",
	}
	done := make(chan interfaces.ClarifyResponse, 1)
	go func() {
		resp, _ := gate.AskClarify(req)
		done <- resp
	}()

	// Wait for SSE event but do NOT respond.
	select {
	case data := <-ch:
		if !strings.Contains(string(data), "event: clarify") {
			t.Fatalf("expected clarify event, got: %s", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for clarify SSE event")
	}

	// Wait for AskClarify to return with TimedOut=true.
	select {
	case resp := <-done:
		if !resp.TimedOut {
			t.Fatal("expected TimedOut=true, got false")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for AskClarify to return after timeout")
	}

	// Check for clarify_timeout SSE event.
	select {
	case data := <-ch:
		if !strings.Contains(string(data), "event: clarify_timeout") {
			t.Fatalf("expected clarify_timeout event, got: %s", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for clarify_timeout SSE event")
	}
}

// TestWebClarifyGate_UnknownID tests responding to an unknown clarify ID.
func TestWebClarifyGate_UnknownID(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ClarifyConfig{ExpirySeconds: 5}
	gate := NewWebClarifyGate(bridge, sessionID, cfg)

	body, _ := json.Marshal(map[string][]string{"choices": {"option1"}})
	httpReq := httptest.NewRequest("POST", "/clarifications/unknown-id/respond", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	RegisterClarifyRoutes(r, gate)
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown clarify ID, got %d", w.Code)
	}
}

// TestWebClarifyGate_MissingBody tests that missing choices/answer returns 400.
func TestWebClarifyGate_MissingBody(t *testing.T) {
	bridge := sse.NewEventBridge()
	sessionID := "test-session"
	cfg := interfaces.ClarifyConfig{ExpirySeconds: 5}
	gate := NewWebClarifyGate(bridge, sessionID, cfg)

	body, _ := json.Marshal(map[string]any{}) // Empty body.
	httpReq := httptest.NewRequest("POST", "/clarifications/some-id/respond", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	RegisterClarifyRoutes(r, gate)
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing choices/answer, got %d", w.Code)
	}
}
