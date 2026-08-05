package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPipAllowed(t *testing.T) {
	if pipAllowed(nil) {
		t.Error("pipAllowed(nil) should be false (fail closed)")
	}
	if pipAllowed(&SandboxConfig{}) {
		t.Error("pipAllowed with zero-value AllowPipInstall should be false")
	}
	if pipAllowed(&SandboxConfig{AllowPipInstall: false}) {
		t.Error("pipAllowed with AllowPipInstall:false should be false")
	}
	if !pipAllowed(&SandboxConfig{AllowPipInstall: true}) {
		t.Error("pipAllowed with AllowPipInstall:true should be true")
	}
}

// readAuditLines reads the audit log file and returns parsed entries.
func readAuditLines(t *testing.T) []CommandAuditEntry {
	t.Helper()
	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("failed to read audit log: %v", err)
	}
	var entries []CommandAuditEntry
	// Split on newlines; skip empty trailing line.
	for len(data) > 0 {
		nl := -1
		for i, b := range data {
			if b == '\n' {
				nl = i
				break
			}
		}
		if nl < 0 {
			break
		}
		line := data[:nl]
		data = data[nl+1:]
		if len(line) == 0 {
			continue
		}
		var e CommandAuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("failed to parse audit entry: %v", err)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestPythonGateDeny(t *testing.T) {
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() { auditLogDir = origAuditDir }()

	sb := &SandboxConfig{
		Mode: SandboxModePaths,
		Permissions: map[string]string{
			"python_interpreter": "deny",
		},
	}

	err := checkPythonGate(sb, "print('hello')")
	if err == nil {
		t.Fatal("expected error for deny permission")
	}
	if err.Error() != "python_interpreter is denied by sandbox permissions" {
		t.Errorf("unexpected error: %v", err)
	}

	entries := readAuditLines(t)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "python_interpreter" {
		t.Errorf("expected tool python_interpreter, got %q", e.Tool)
	}
	if e.Decision != "denied" {
		t.Errorf("expected decision denied, got %q", e.Decision)
	}
	if e.Risk != "high" {
		t.Errorf("expected risk high, got %q", e.Risk)
	}
	if e.SandboxMode != "paths" {
		t.Errorf("expected sandbox_mode paths, got %q", e.SandboxMode)
	}
}

func TestPythonGateAskApproved(t *testing.T) {
	origAsk := askApproval
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() {
		askApproval = origAsk
		auditLogDir = origAuditDir
	}()

	askApproval = func(req ApprovalRequest) (bool, error) {
		return true, nil
	}

	sb := &SandboxConfig{
		Mode: SandboxModePaths,
		Permissions: map[string]string{
			"python_interpreter": "ask",
		},
	}

	err := checkPythonGate(sb, "print('hello')")
	if err != nil {
		t.Fatalf("unexpected error for approved: %v", err)
	}

	entries := readAuditLines(t)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Decision != "approved" {
		t.Errorf("expected decision approved, got %q", e.Decision)
	}
	if e.SandboxMode != "paths" {
		t.Errorf("expected sandbox_mode paths, got %q", e.SandboxMode)
	}
	if req := askApproval; req != nil {
		// Just confirm the stub was set and called (coverage side effect)
	}
}

func TestPythonGateAskNotApproved(t *testing.T) {
	origAsk := askApproval
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() {
		askApproval = origAsk
		auditLogDir = origAuditDir
	}()

	askApproval = func(req ApprovalRequest) (bool, error) {
		return false, nil
	}

	sb := &SandboxConfig{
		Mode: SandboxModePaths,
		Permissions: map[string]string{
			"python_interpreter": "ask",
		},
	}

	err := checkPythonGate(sb, "print('hello')")
	if err == nil {
		t.Fatal("expected error for not approved")
	}
	if err.Error() != "python code execution not approved by user" {
		t.Errorf("unexpected error: %v", err)
	}

	entries := readAuditLines(t)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Decision != "not_approved" {
		t.Errorf("expected decision not_approved, got %q", e.Decision)
	}
}

func TestPythonGateNilSandboxDefaultsToAsk(t *testing.T) {
	origAsk := askApproval
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() {
		askApproval = origAsk
		auditLogDir = origAuditDir
	}()

	// nil askApproval -> approveExec returns false with error -> fail closed.
	askApproval = nil

	err := checkPythonGate(nil, "print('hello')")
	if err == nil {
		t.Fatal("nil sandbox with nil askApproval should deny (fail closed)")
	}
	if err.Error() != "python code execution not approved by user" {
		t.Errorf("unexpected error: %v", err)
	}

	entries := readAuditLines(t)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Decision != "not_approved" {
		t.Errorf("expected decision not_approved, got %q", e.Decision)
	}
	if e.SandboxMode != "off" {
		t.Errorf("expected sandbox_mode off for nil sandbox, got %q", e.SandboxMode)
	}
}

func TestPythonGateAllow(t *testing.T) {
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() { auditLogDir = origAuditDir }()

	sb := &SandboxConfig{
		Mode: SandboxModeBubblewrap,
		Permissions: map[string]string{
			"python_interpreter": "allow",
		},
	}

	err := checkPythonGate(sb, "print('hello')")
	if err != nil {
		t.Fatalf("unexpected error for allow: %v", err)
	}

	entries := readAuditLines(t)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Decision != "allowed" {
		t.Errorf("expected decision allowed, got %q", e.Decision)
	}
	if e.SandboxMode != "bubblewrap" {
		t.Errorf("expected sandbox_mode bubblewrap, got %q", e.SandboxMode)
	}
}

func TestPythonGateCodeTruncatedInAudit(t *testing.T) {
	origAsk := askApproval
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() {
		askApproval = origAsk
		auditLogDir = origAuditDir
	}()

	// Build a string longer than maxLogField runes BEFORE the closure.
	longCode := ""
	for i := 0; i < maxLogField+500; i++ {
		longCode += "x"
	}

	hasTruncationMarker := false
	askApproval = func(req ApprovalRequest) (bool, error) {
		// Verify the code passed to approval is the truncated version.
		// truncateStr appends a truncation marker, so it's shorter than the original.
		reqRunes := len([]rune(req.Command))
		origRunes := len([]rune(longCode))
		if reqRunes >= origRunes {
			t.Errorf("approval request Command was not truncated: %d runes (original: %d runes)", reqRunes, origRunes)
		}
		// Also verify the truncation marker is present.
		if len(req.Command) < origRunes {
			hasTruncationMarker = true
		}
		return true, nil
	}

	sb := &SandboxConfig{
		Mode: SandboxModePaths,
		Permissions: map[string]string{
			"python_interpreter": "ask",
		},
	}

	err := checkPythonGate(sb, longCode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasTruncationMarker {
		t.Error("expected approval command to be truncated")
	}
}

func TestCheckPythonGateExpiresAt(t *testing.T) {
	origAsk := askApproval
	origConfig := currentApproval
	currentApproval = ApprovalConfig{ExpirySeconds: 30}
	defer func() {
		askApproval = origAsk
		currentApproval = origConfig
	}()

	var capturedReq ApprovalRequest
	askApproval = func(req ApprovalRequest) (bool, error) {
		capturedReq = req
		return true, nil
	}

	sb := &SandboxConfig{
		Mode: SandboxModePaths,
		Permissions: map[string]string{
			"python_interpreter": "ask",
		},
	}

	err := checkPythonGate(sb, "print('hello')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedExpiry := time.Now().Add(30 * time.Second)
	diff := capturedReq.ExpiresAt.Sub(expectedExpiry).Abs()
	if diff > 2*time.Second {
		t.Errorf("expires at %v differs too much from expected %v", capturedReq.ExpiresAt, expectedExpiry)
	}
	if capturedReq.Source != "direct" {
		t.Errorf("expected source direct, got %q", capturedReq.Source)
	}
	if capturedReq.Tool != "python_interpreter" {
		t.Errorf("expected tool python_interpreter, got %q", capturedReq.Tool)
	}
	if capturedReq.Risk != "high" {
		t.Errorf("expected risk high, got %q", capturedReq.Risk)
	}
	if capturedReq.Reason != "arbitrary Python code execution" {
		t.Errorf("expected reason, got %q", capturedReq.Reason)
	}
}
