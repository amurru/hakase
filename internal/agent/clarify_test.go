package agent

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
)

// stubContext satisfies agent.Context for handler tests that don't read
// from the context.
type stubContext struct {
	agent.ContextMock
}

// mockClarifyGate is a test implementation of interfaces.ClarifyGate
type mockClarifyGate struct {
	askFunc    func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error)
	configFunc func() interfaces.ClarifyConfig
	expiryFunc func() time.Duration
}

func (m *mockClarifyGate) AskClarify(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
	if m.askFunc != nil {
		return m.askFunc(req)
	}
	return interfaces.ClarifyResponse{}, nil
}

func (m *mockClarifyGate) ClarifyConfig() interfaces.ClarifyConfig {
	if m.configFunc != nil {
		return m.configFunc()
	}
	return interfaces.ClarifyConfig{}
}

func (m *mockClarifyGate) ClarifyExpiry() time.Duration {
	if m.expiryFunc != nil {
		return m.expiryFunc()
	}
	return 120 * time.Second
}

func TestClarifyExecNilAskClarifyFailsClosed(t *testing.T) {
	saved := rt
	rt = nil
	t.Cleanup(func() { rt = saved })

	resp, err := askClarify(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err == nil {
		t.Error("askClarify returned nil error when rt is nil, want error")
	}
	if resp.Canceled || resp.TimedOut || len(resp.Answer) > 0 {
		t.Error("askClarify returned a non-zero response with nil gate")
	}
}

func TestClarifyExecWithStub(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			return interfaces.ClarifyResponse{Answer: []string{"A"}}, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	resp, err := askClarify(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("askClarify returned error: %v", err)
	}
	if len(resp.Answer) != 1 || resp.Answer[0] != "A" {
		t.Errorf("Answer = %v, want [A]", resp.Answer)
	}
}

func TestClarifyExecWithStubCanceled(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			return interfaces.ClarifyResponse{Canceled: true}, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	resp, err := askClarify(ClarifyRequest{
		Question: "What should I do?",
		Choices:  []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("askClarify returned error: %v", err)
	}
	if !resp.Canceled {
		t.Error("expected Canceled=true")
	}
}

func TestClarifyExecWithStubTimedOut(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			return interfaces.ClarifyResponse{TimedOut: true}, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	resp, err := askClarify(ClarifyRequest{
		Question: "What should I do?",
	})
	if err != nil {
		t.Fatalf("askClarify returned error: %v", err)
	}
	if !resp.TimedOut {
		t.Error("expected TimedOut=true")
	}
}

func TestClarifyExecPropagatesRequest(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	var capturedReq interfaces.ClarifyRequest
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			capturedReq = req
			return interfaces.ClarifyResponse{Answer: []string{"B"}}, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	req := ClarifyRequest{
		Question:    "Pick one",
		Choices:     []string{"A", "B", "C"},
		MultiSelect: false,
	}
	resp, err := askClarify(req)
	if err != nil {
		t.Fatalf("askClarify returned error: %v", err)
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

func TestClarifyTimeoutDefault(t *testing.T) {
	saved := deps
	deps = &Deps{ClarifyCfg: config.ClarifyConfig{}}
	t.Cleanup(func() { deps = saved })

	d := clarifyTimeout()
	if d != 120*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 120s (default)", d)
	}
}

func TestClarifyTimeoutConfigured(t *testing.T) {
	saved := deps
	deps = &Deps{ClarifyCfg: config.ClarifyConfig{ExpirySeconds: 30}}
	t.Cleanup(func() { deps = saved })

	d := clarifyTimeout()
	if d != 30*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 30s", d)
	}
}

func TestClarifyTimeoutZeroFallsBack(t *testing.T) {
	saved := deps
	deps = &Deps{ClarifyCfg: config.ClarifyConfig{ExpirySeconds: 0}}
	t.Cleanup(func() { deps = saved })

	d := clarifyTimeout()
	if d != 120*time.Second {
		t.Errorf("clarifyTimeout() = %v, want 120s (fallback)", d)
	}
}
