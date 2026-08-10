package agent

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"errors"
	"testing"
	"time"
)

// mockApprovalGate is a test implementation of interfaces.ApprovalGate
type mockApprovalGate struct {
	approveFunc func(req interfaces.ApprovalRequest) (bool, error)
	configFunc  func() interfaces.ApprovalConfig
	expiryFunc  func() time.Duration
}

func (m *mockApprovalGate) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
	if m.approveFunc != nil {
		return m.approveFunc(req)
	}
	return false, nil
}

func (m *mockApprovalGate) ApprovalConfig() interfaces.ApprovalConfig {
	if m.configFunc != nil {
		return m.configFunc()
	}
	return interfaces.ApprovalConfig{}
}

func (m *mockApprovalGate) ApprovalExpiry() time.Duration {
	if m.expiryFunc != nil {
		return m.expiryFunc()
	}
	return 60 * time.Second
}

func TestApproveExecNilAskApprovalFailsClosed(t *testing.T) {
	// Ensure rt is nil (headless mode).
	saved := rt
	rt = nil
	t.Cleanup(func() { rt = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "rm -rf /",
		Risk:      "HIGH",
		Reason:    "destructive command",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := ApproveExec(req)
	if approved {
		t.Error("ApproveExec returned true when rt is nil, want false")
	}
	if err == nil {
		t.Error("ApproveExec returned nil error when rt is nil, want error")
	}
	if !errors.Is(err, err) { // sanity: err is not nil
		t.Logf("error message (expected): %v", err)
	}
}

func TestApproveExecWithStub(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	rt.SetApprovalGate(&mockApprovalGate{
		approveFunc: func(req interfaces.ApprovalRequest) (bool, error) {
			return true, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "ls",
		Risk:      "LOW",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := ApproveExec(req)
	if !approved {
		t.Error("ApproveExec returned false when stub returns true")
	}
	if err != nil {
		t.Errorf("ApproveExec returned error: %v", err)
	}
}

func TestApproveExecWithStubDeny(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	rt.SetApprovalGate(&mockApprovalGate{
		approveFunc: func(req interfaces.ApprovalRequest) (bool, error) {
			return false, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "sudo rm -rf /",
		Risk:      "HIGH",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := ApproveExec(req)
	if approved {
		t.Error("ApproveExec returned true when stub returns false")
	}
	if err != nil {
		t.Errorf("ApproveExec returned error on clean deny: %v", err)
	}
}

func TestApproveExecPropagatesRequest(t *testing.T) {
	saved := rt
	rt = &Runtime{}
	var capturedReq interfaces.ApprovalRequest
	rt.SetApprovalGate(&mockApprovalGate{
		approveFunc: func(req interfaces.ApprovalRequest) (bool, error) {
			capturedReq = req
			return true, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	req := ApprovalRequest{
		Tool:      "python_interpreter",
		Command:   "import os; os.system('rm -rf /')",
		Args:      []string{"-c", "import os; os.system('rm -rf /')"},
		Risk:      "HIGH",
		Reason:    "arbitrary code execution",
		Source:    "delegated",
		ExpiresAt: time.Now().Add(30 * time.Second),
	}

	approved, err := ApproveExec(req)
	if !approved || err != nil {
		t.Fatalf("ApproveExec returned (%v, %v)", approved, err)
	}

	if capturedReq.Tool != req.Tool {
		t.Errorf("Tool = %q, want %q", capturedReq.Tool, req.Tool)
	}
	if capturedReq.Command != req.Command {
		t.Errorf("Command = %q, want %q", capturedReq.Command, req.Command)
	}
	if capturedReq.Risk != req.Risk {
		t.Errorf("Risk = %q, want %q", capturedReq.Risk, req.Risk)
	}
	if capturedReq.Source != req.Source {
		t.Errorf("Source = %q, want %q", capturedReq.Source, req.Source)
	}
	if len(capturedReq.Args) != len(req.Args) {
		t.Errorf("Args length = %d, want %d", len(capturedReq.Args), len(req.Args))
	}
}

func TestApprovalExpiryDefault(t *testing.T) {
	saved := deps
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{}}
	t.Cleanup(func() { deps = saved })

	d := ApprovalExpiry()
	if d != 60*time.Second {
		t.Errorf("ApprovalExpiry() = %v, want 60s (default)", d)
	}
}

func TestApprovalExpiryConfigured(t *testing.T) {
	saved := deps
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{ExpirySeconds: 30}}
	t.Cleanup(func() { deps = saved })

	d := ApprovalExpiry()
	if d != 30*time.Second {
		t.Errorf("ApprovalExpiry() = %v, want 30s", d)
	}
}

func TestApprovalExpiryZeroFallsBack(t *testing.T) {
	saved := deps
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{ExpirySeconds: 0}}
	t.Cleanup(func() { deps = saved })

	d := ApprovalExpiry()
	if d != 60*time.Second {
		t.Errorf("ApprovalExpiry() = %v, want 60s (fallback)", d)
	}
}
