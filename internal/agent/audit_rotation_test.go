package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditRotationBoundsLog verifies size-based rotation keeps logs bounded.
func TestAuditRotationBoundsLog(t *testing.T) {
	oldDir, oldMax, oldBackups := auditLogDir, AuditMaxBytes, AuditMaxBackups
	auditLogDir = t.TempDir()
	AuditMaxBytes = 1024 // tiny to force rotation
	AuditMaxBackups = 3
	t.Cleanup(func() { auditLogDir = oldDir; AuditMaxBytes = oldMax; AuditMaxBackups = oldBackups })

	for i := 0; i < 50; i++ {
		AuditCommandExec(CommandAuditEntry{
			Timestamp: time.Now(),
			Tool:      "system_exec",
			Command:   strings.Repeat("x", 200),
			Decision:  "allowed",
			SessionID: "sess_test_123",
		})
	}
	// Current + backups must be bounded.
	entries, err := os.ReadDir(auditLogDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) > AuditMaxBackups+2 { // current + backups + lock
		t.Fatalf("log files = %d, want <= %d (rotation unbounded)", len(entries), AuditMaxBackups+2)
	}
	// Every line must still be valid JSON with session_id.
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(auditLogDir, e.Name()))
		for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if ln == "" {
				continue
			}
			if !strings.Contains(ln, "sess_test_123") {
				t.Fatalf("audit line missing session_id: %s", ln[:80])
			}
		}
	}
}

// TestAuditSessionIDRoundTrips verifies the session id survives the JSONL.
func TestAuditSessionIDRoundTrips(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	AuditCommandExec(CommandAuditEntry{
		Timestamp: time.Now(), Tool: "system_exec", Command: "ls",
		Decision: "allowed", SessionID: "sess_abc",
	})
	data, err := os.ReadFile(filepath.Join(auditLogDir, "exec-audit.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "sess_abc") {
		t.Fatalf("session_id not persisted: %s", data)
	}
}
