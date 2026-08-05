package main

import (
	"errors"
	"testing"
	"time"
)

func TestApproveExecNilAskApprovalFailsClosed(t *testing.T) {
	// Ensure askApproval is nil (headless mode).
	saved := askApproval
	askApproval = nil
	t.Cleanup(func() { askApproval = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "rm -rf /",
		Risk:      "HIGH",
		Reason:    "destructive command",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := approveExec(req)
	if approved {
		t.Error("approveExec returned true when askApproval is nil, want false")
	}
	if err == nil {
		t.Error("approveExec returned nil error when askApproval is nil, want error")
	}
	if !errors.Is(err, err) { // sanity: err is not nil
		t.Logf("error message (expected): %v", err)
	}
}

func TestApproveExecWithStub(t *testing.T) {
	saved := askApproval
	askApproval = func(req ApprovalRequest) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { askApproval = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "ls",
		Risk:      "LOW",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := approveExec(req)
	if !approved {
		t.Error("approveExec returned false when stub returns true")
	}
	if err != nil {
		t.Errorf("approveExec returned error: %v", err)
	}
}

func TestApproveExecWithStubDeny(t *testing.T) {
	saved := askApproval
	askApproval = func(req ApprovalRequest) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { askApproval = saved })

	req := ApprovalRequest{
		Tool:      "system_exec",
		Command:   "sudo rm -rf /",
		Risk:      "HIGH",
		Source:    "direct",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}

	approved, err := approveExec(req)
	if approved {
		t.Error("approveExec returned true when stub returns false")
	}
	if err != nil {
		t.Errorf("approveExec returned error on clean deny: %v", err)
	}
}

func TestApproveExecPropagatesRequest(t *testing.T) {
	saved := askApproval
	var capturedReq ApprovalRequest
	askApproval = func(req ApprovalRequest) (bool, error) {
		capturedReq = req
		return true, nil
	}
	t.Cleanup(func() { askApproval = saved })

	req := ApprovalRequest{
		Tool:      "python_interpreter",
		Command:   "import os; os.system('rm -rf /')",
		Args:      []string{"-c", "import os; os.system('rm -rf /')"},
		Risk:      "HIGH",
		Reason:    "arbitrary code execution",
		Source:    "delegated",
		ExpiresAt: time.Now().Add(30 * time.Second),
	}

	approved, err := approveExec(req)
	if !approved || err != nil {
		t.Fatalf("approveExec returned (%v, %v)", approved, err)
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
	saved := currentApproval
	currentApproval = ApprovalConfig{}
	t.Cleanup(func() { currentApproval = saved })

	d := approvalExpiry()
	if d != 60*time.Second {
		t.Errorf("approvalExpiry() = %v, want 60s (default)", d)
	}
}

func TestApprovalExpiryConfigured(t *testing.T) {
	saved := currentApproval
	currentApproval = ApprovalConfig{ExpirySeconds: 30}
	t.Cleanup(func() { currentApproval = saved })

	d := approvalExpiry()
	if d != 30*time.Second {
		t.Errorf("approvalExpiry() = %v, want 30s", d)
	}
}

func TestApprovalExpiryZeroFallsBack(t *testing.T) {
	saved := currentApproval
	currentApproval = ApprovalConfig{ExpirySeconds: 0}
	t.Cleanup(func() { currentApproval = saved })

	d := approvalExpiry()
	if d != 60*time.Second {
		t.Errorf("approvalExpiry() = %v, want 60s (fallback)", d)
	}
}
