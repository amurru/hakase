package main

import (
	"amurru/hakase/internal/util"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CommandAuditEntry records one command-execution decision.
type CommandAuditEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Tool        string    `json:"tool"` // system_exec | python_interpreter | pip
	Command     string    `json:"command"`
	Args        []string  `json:"args"`
	CWD         string    `json:"cwd"`
	SandboxMode string    `json:"sandbox_mode"`
	Decision    string    `json:"decision"` // allowed | denied | approved | not_approved | error | timeout
	Risk        string    `json:"risk"`
	Reason      string    `json:"reason"`
	DurationMs  int64     `json:"duration_ms"`
	ExitCode    int       `json:"exit_code"`
}

// auditLogDir is where the always-on audit log is written. Overridable in tests.
var auditLogDir = "logs"

// auditMu serialises writes to the audit log file.
var auditMu sync.Mutex

// auditCommandExec appends one JSON line to the always-on audit log at
// <auditLogDir>/exec-audit.jsonl (created on first write). NEVER gated by
// debugMode - this is a security audit trail, always on. Best-effort: errors
// are swallowed (never break the agent because logging failed).
// Concurrent-safe via auditMu.
func auditCommandExec(entry CommandAuditEntry) {
	// Truncate long string fields to keep the audit log bounded.
	entry.Command = util.TruncateStr(entry.Command)
	entry.Reason = util.TruncateStr(entry.Reason)

	b, err := json.Marshal(entry)
	if err != nil {
		return // best-effort: encoding failure is not actionable
	}

	auditMu.Lock()
	defer auditMu.Unlock()

	// Create directory on first write.
	if err := os.MkdirAll(auditLogDir, 0o755); err != nil {
		return
	}

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	// Append one JSON line per entry, \n terminated.
	_, _ = f.Write(b)
	_, _ = f.Write([]byte("\n"))
}
