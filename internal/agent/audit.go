package agent

import (
	"amurru/hakase/internal/util"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// CommandAuditEntry records one command-execution decision.
type CommandAuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Tool      string    `json:"tool"` // system_exec | python_interpreter | pip
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	CWD       string    `json:"cwd"`
	// SessionID is the hakase session of the asking run (possibly empty).
	// It lets the sleep harvest join audit tool names to sessions directly
	// instead of the best-effort time-window join.
	SessionID   string `json:"session_id,omitempty"`
	SandboxMode string `json:"sandbox_mode"`
	Decision    string `json:"decision"` // allowed | denied | approved | not_approved | error | timeout
	Risk        string `json:"risk"`
	Reason      string `json:"reason"`
	DurationMs  int64  `json:"duration_ms"`
	ExitCode    int    `json:"exit_code"`
}

// auditLogDir is where the always-on audit log is written. Overridable in tests.
var auditLogDir = "logs"

// Audit rotation bounds logs/ (issue #13): the audit trail is append-only
// and already ~730KB with no retention. Defaults keep ~30MB max.
var (
	// AuditMaxBytes triggers a rotation when the next write would exceed it.
	AuditMaxBytes int64 = 5 * 1024 * 1024 // 5MB
	// AuditMaxBackups caps rotated files (exec-audit.1.jsonl .. .N.jsonl).
	AuditMaxBackups = 5
)

// auditMu serialises in-process writes to the audit log file. Cross-process
// safety comes from the exec-audit.lock flock held alongside this mutex.
var auditMu sync.Mutex

// AuditCommandExec appends one JSON line to the always-on audit log at
// <auditLogDir>/exec-audit.jsonl (created on first write). NEVER gated by
// debugMode - this is a security audit trail, always on. Best-effort: errors
// are swallowed (never break the agent because logging failed).
// Concurrent-safe via auditMu + cross-process flock; size-rotated via
// AuditMaxBytes/AuditMaxBackups.
func AuditCommandExec(entry CommandAuditEntry) {
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

	// Cross-process lock so TUI + web rotation/append is safe.
	lock, err := os.OpenFile(filepath.Join(auditLogDir, "exec-audit.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if ferr := util.FlockExclusive(lock); ferr == nil {
			defer func() {
				_ = util.FlockUnlock(lock)
				_ = lock.Close()
			}()
		} else {
			_ = lock.Close()
		}
	}

	rotateAuditLogIfNeeded(path, int64(len(b)+1))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = os.Chmod(path, 0o600)

	// Append one JSON line per entry, \n terminated.
	_, _ = f.Write(b)
	_, _ = f.Write([]byte("\n"))
}

// rotateAuditLogIfNeeded renames exec-audit.jsonl -> .1.jsonl (shifting older
// backups up, dropping beyond AuditMaxBackups) when the next write would
// exceed AuditMaxBytes. Caller must hold auditMu and the audit flock.
func rotateAuditLogIfNeeded(path string, nextWrite int64) {
	if AuditMaxBytes <= 0 || AuditMaxBackups <= 0 {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return // missing or unreadable: nothing to rotate
	}
	if fi.Size()+nextWrite <= AuditMaxBytes {
		return
	}
	// Drop the oldest backup, shift the rest up, then rotate current to .1.
	oldest := filepath.Join(filepath.Dir(path), "exec-audit."+strconv.Itoa(AuditMaxBackups)+".jsonl")
	_ = os.Remove(oldest)
	for i := AuditMaxBackups - 1; i >= 1; i-- {
		src := filepath.Join(filepath.Dir(path), "exec-audit."+strconv.Itoa(i)+".jsonl")
		dst := filepath.Join(filepath.Dir(path), "exec-audit."+strconv.Itoa(i+1)+".jsonl")
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(path, filepath.Join(filepath.Dir(path), "exec-audit.1.jsonl"))
}
