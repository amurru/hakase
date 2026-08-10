package main

import (
	"amurru/hakase/internal/util"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditCommandExecOneLineAppended(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	entry := CommandAuditEntry{
		Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Tool:        "system_exec",
		Command:     "ls -la",
		Args:        []string{"-la"},
		CWD:         "/home/user",
		SandboxMode: "paths",
		Decision:    "allowed",
		Risk:        "LOW",
		Reason:      "safe command",
		DurationMs:  42,
		ExitCode:    0,
	}

	auditCommandExec(entry)

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %s", len(lines), data)
	}

	var parsed CommandAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("JSON parse error: %v\n%s", err, lines[0])
	}

	if parsed.Tool != entry.Tool {
		t.Errorf("Tool = %q, want %q", parsed.Tool, entry.Tool)
	}
	if parsed.Command != entry.Command {
		t.Errorf("Command = %q, want %q", parsed.Command, entry.Command)
	}
	if parsed.Decision != entry.Decision {
		t.Errorf("Decision = %q, want %q", parsed.Decision, entry.Decision)
	}
	if parsed.Risk != entry.Risk {
		t.Errorf("Risk = %q, want %q", parsed.Risk, entry.Risk)
	}
	if parsed.Reason != entry.Reason {
		t.Errorf("Reason = %q, want %q", parsed.Reason, entry.Reason)
	}
	if parsed.DurationMs != entry.DurationMs {
		t.Errorf("DurationMs = %d, want %d", parsed.DurationMs, entry.DurationMs)
	}
	if parsed.ExitCode != entry.ExitCode {
		t.Errorf("ExitCode = %d, want %d", parsed.ExitCode, entry.ExitCode)
	}
}

func TestAuditCommandExecAppendsMultipleLines(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	for i := range 5 {
		entry := CommandAuditEntry{
			Timestamp: time.Now(),
			Tool:      "system_exec",
			Command:   "cmd-" + string(rune('0'+i)),
			Decision:  "allowed",
		}
		auditCommandExec(entry)
	}

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %s", len(lines), data)
	}

	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line is not valid JSON: %v\n%s", err, line)
		}
	}
}

func TestAuditCommandExecConcurrentSafety(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	const goroutines = 20
	const writesPerGoroutine = 25

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range writesPerGoroutine {
				entry := CommandAuditEntry{
					Timestamp: time.Now(),
					Tool:      "system_exec",
					Command:   "cmd",
					Decision:  "allowed",
					// Distinguish each entry via DurationMs.
					DurationMs: int64(id*1000 + i),
				}
				auditCommandExec(entry)
			}
		}(g)
	}
	wg.Wait()

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	expected := goroutines * writesPerGoroutine
	if len(lines) != expected {
		t.Errorf("expected %d lines, got %d (some writes may have been lost)", expected, len(lines))
	}
}

func TestAuditCommandExecLongValuesTruncated(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	longCmd := strings.Repeat("A", util.MaxLogField+100)
	longReason := strings.Repeat("B", util.MaxLogField+100)

	entry := CommandAuditEntry{
		Timestamp: time.Now(),
		Tool:      "system_exec",
		Command:   longCmd,
		Decision:  "denied",
		Risk:      "HIGH",
		Reason:    longReason,
	}

	auditCommandExec(entry)

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var parsed CommandAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}

	// Command should be truncated.
	if len([]rune(parsed.Command)) > util.MaxLogField+50 {
		t.Errorf("Command not truncated: len=%d (%d runes)", len(parsed.Command), len([]rune(parsed.Command)))
	}
	if !strings.Contains(parsed.Command, "truncated 100 runes") {
		t.Errorf("Command missing truncation marker: %q", parsed.Command)
	}

	// Reason should be truncated.
	if len([]rune(parsed.Reason)) > util.MaxLogField+50 {
		t.Errorf("Reason not truncated: len=%d (%d runes)", len(parsed.Reason), len([]rune(parsed.Reason)))
	}
	if !strings.Contains(parsed.Reason, "truncated 100 runes") {
		t.Errorf("Reason missing truncation marker: %q", parsed.Reason)
	}
}

func TestAuditCommandExecShortValuesNotTruncated(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	shortCmd := "ls"

	entry := CommandAuditEntry{
		Timestamp: time.Now(),
		Tool:      "system_exec",
		Command:   shortCmd,
		Decision:  "allowed",
	}

	auditCommandExec(entry)

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	var parsed CommandAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}

	if parsed.Command != shortCmd {
		t.Errorf("short Command got modified: %q, want %q", parsed.Command, shortCmd)
	}
}

func TestAuditCommandExecJSONParsesBack(t *testing.T) {
	oldDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = oldDir })

	now := time.Now().Truncate(time.Second)
	entry := CommandAuditEntry{
		Timestamp:   now,
		Tool:        "python_interpreter",
		Command:     "print('hello')",
		Args:        []string{"-c", "print('hello')"},
		CWD:         "/workspace",
		SandboxMode: "bubblewrap",
		Decision:    "approved",
		Risk:        "HIGH",
		Reason:      "arbitrary Python execution",
		DurationMs:  1500,
		ExitCode:    0,
	}

	auditCommandExec(entry)

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var parsed CommandAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("JSON parse error: %v\n%s", err, lines[0])
	}

	// Verify all fields round-trip correctly.
	if !parsed.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", parsed.Timestamp, now)
	}
	if parsed.Tool != entry.Tool {
		t.Errorf("Tool = %q, want %q", parsed.Tool, entry.Tool)
	}
	if parsed.Decision != entry.Decision {
		t.Errorf("Decision = %q, want %q", parsed.Decision, entry.Decision)
	}
	if parsed.SandboxMode != entry.SandboxMode {
		t.Errorf("sandbox.SandboxMode = %q, want %q", parsed.SandboxMode, entry.SandboxMode)
	}
	if len(parsed.Args) != len(entry.Args) {
		t.Errorf("Args len = %d, want %d", len(parsed.Args), len(entry.Args))
	}
}

func TestAuditCommandExecDirCreatedOnFirstWrite(t *testing.T) {
	oldDir := auditLogDir
	// Use a non-existent subdirectory to verify auto-creation.
	baseDir := t.TempDir()
	auditLogDir = filepath.Join(baseDir, "nested", "audit")
	t.Cleanup(func() { auditLogDir = oldDir })

	entry := CommandAuditEntry{
		Timestamp: time.Now(),
		Tool:      "system_exec",
		Command:   "ls",
		Decision:  "allowed",
	}

	// First write should create the directory.
	auditCommandExec(entry)

	path := filepath.Join(auditLogDir, "exec-audit.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("audit log file was not created: %v", err)
	}
}
