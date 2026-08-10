package main

import (
	"amurru/hakase/internal/config"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
)

// stubContext satisfies agent.Context for handler tests that don't read
// from the context.
type stubContext struct {
	agent.ContextMock
}

func TestClarifyExecNilAskClarifyFailsClosed(t *testing.T) {
	saved := askClarify
	askClarify = nil
	t.Cleanup(func() { askClarify = saved })

	resp, err := clarifyExec(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err == nil {
		t.Error("clarifyExec returned nil error when askClarify is nil, want error")
	}
	if resp.Canceled || resp.TimedOut || len(resp.Answer) > 0 {
		t.Error("clarifyExec returned a non-zero response with nil gate")
	}
}

func TestClarifyExecWithStub(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		return ClarifyResponse{Answer: []string{"A"}}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	resp, err := clarifyExec(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("clarifyExec returned error: %v", err)
	}
	if len(resp.Answer) != 1 || resp.Answer[0] != "A" {
		t.Errorf("Answer = %v, want [A]", resp.Answer)
	}
}

func TestClarifyExecWithStubCanceled(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		return ClarifyResponse{Canceled: true}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	resp, err := clarifyExec(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("clarifyExec returned error: %v", err)
	}
	if !resp.Canceled {
		t.Error("expected Canceled=true")
	}
}

func TestClarifyExecWithStubTimedOut(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		return ClarifyResponse{TimedOut: true}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	resp, err := clarifyExec(ClarifyRequest{
		Question: "What should I do?",
	})
	if err != nil {
		t.Fatalf("clarifyExec returned error: %v", err)
	}
	if !resp.TimedOut {
		t.Error("expected TimedOut=true")
	}
}

func TestClarifyExecPropagatesRequest(t *testing.T) {
	saved := askClarify
	var capturedReq ClarifyRequest
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		capturedReq = req
		return ClarifyResponse{Answer: []string{"B"}}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	req := ClarifyRequest{
		Question:    "Pick one",
		Choices:     []string{"A", "B", "C"},
		MultiSelect: false,
	}
	resp, err := clarifyExec(req)
	if err != nil {
		t.Fatalf("clarifyExec returned error: %v", err)
	}
	if len(resp.Answer) != 1 || resp.Answer[0] != "B" {
		t.Errorf("Answer = %v, want [B]", resp.Answer)
	}
	if capturedReq.Question != req.Question {
		t.Errorf("Question = %q, want %q", capturedReq.Question, req.Question)
	}
	if len(capturedReq.Choices) != 3 {
		t.Errorf("Choices len = %d, want 3", len(capturedReq.Choices))
	}
}

func TestClarifyHandlerChoicesClamped(t *testing.T) {
	saved := askClarify
	var capturedReq ClarifyRequest
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		capturedReq = req
		return ClarifyResponse{Answer: []string{"E"}}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	input := ClarifyInput{
		Question: "Pick one",
		Choices:  []string{"A", "B", "C", "D", "E", "F"},
	}
	out, err := clarifyHandler(&stubContext{}, input)
	if err != nil {
		t.Fatalf("clarifyHandler returned error: %v", err)
	}
	if len(capturedReq.Choices) != 4 {
		t.Errorf("choices clamped to %d, want 4", len(capturedReq.Choices))
	}
	if len(out.ChoicesOffered) != 4 {
		t.Errorf("ChoicesOffered = %d, want 4", len(out.ChoicesOffered))
	}
}

func TestClarifyHandlerCanceledNormalizesAnswer(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		return ClarifyResponse{Answer: []string{"should be cleared"}, Canceled: true}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	input := ClarifyInput{
		Question: "What now?",
		Choices:  []string{"A", "B"},
	}
	out, err := clarifyHandler(&stubContext{}, input)
	if err != nil {
		t.Fatalf("clarifyHandler returned error: %v", err)
	}
	if !out.Canceled {
		t.Error("expected Canceled=true in output")
	}
	if len(out.UserResponse) != 0 {
		t.Errorf("UserResponse should be nil when canceled, got %v", out.UserResponse)
	}
}

func TestClarifyHandlerTimedOutNormalizesAnswer(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		return ClarifyResponse{Answer: []string{"stale"}, TimedOut: true}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	input := ClarifyInput{
		Question: "Quick?",
	}
	out, err := clarifyHandler(&stubContext{}, input)
	if err != nil {
		t.Fatalf("clarifyHandler returned error: %v", err)
	}
	if !out.TimedOut {
		t.Error("expected TimedOut=true in output")
	}
	if len(out.UserResponse) != 0 {
		t.Errorf("UserResponse should be nil when timed out, got %v", out.UserResponse)
	}
}

func TestClarifyHandlerMultiSelectPassthrough(t *testing.T) {
	saved := askClarify
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		if !req.MultiSelect {
			t.Error("expected MultiSelect=true in request")
		}
		return ClarifyResponse{Answer: []string{"A", "C"}}, nil
	}
	t.Cleanup(func() { askClarify = saved })

	input := ClarifyInput{
		Question:    "Pick options",
		Choices:     []string{"A", "B", "C"},
		MultiSelect: true,
	}
	out, err := clarifyHandler(&stubContext{}, input)
	if err != nil {
		t.Fatalf("clarifyHandler returned error: %v", err)
	}
	if len(out.UserResponse) != 2 {
		t.Errorf("UserResponse len = %d, want 2", len(out.UserResponse))
	}
}

func TestClarifyTimeoutDefault(t *testing.T) {
	saved := currentClarify
	currentClarify = config.ClarifyConfig{}
	t.Cleanup(func() { currentClarify = saved })

	d := clarifyTimeout()
	if d != 120*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 120s (default)", d)
	}
}

func TestClarifyTimeoutConfigured(t *testing.T) {
	saved := currentClarify
	currentClarify = config.ClarifyConfig{ExpirySeconds: 30}
	t.Cleanup(func() { currentClarify = saved })

	d := clarifyTimeout()
	if d != 30*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 30s", d)
	}
}

func TestClarifyTimeoutZeroFallsBack(t *testing.T) {
	saved := currentClarify
	currentClarify = config.ClarifyConfig{ExpirySeconds: 0}
	t.Cleanup(func() { currentClarify = saved })

	d := clarifyTimeout()
	if d != 120*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 120s (fallback)", d)
	}
}
