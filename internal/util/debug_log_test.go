package util_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"amurru/hakase/internal/util"
)

func TestTruncateStr(t *testing.T) {
	short := "hello"
	if got := util.TruncateStr(short); got != short {
		t.Errorf("TruncateStr(%q) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("x", util.MaxLogField+100)
	got := util.TruncateStr(long)
	// truncated string includes a "...[truncated N runes]" marker, so it
	// will exceed MaxLogField by the marker length.
	if len([]rune(got)) > util.MaxLogField+60 {
		t.Errorf("TruncateStr did not truncate: len=%d", len([]rune(got)))
	}
	if !strings.Contains(got, "truncated 100 runes") {
		t.Errorf("TruncateStr missing truncation marker: %q", got)
	}
}

func TestSanitizeLogValue(t *testing.T) {
	// Test TruncateStr directly since sanitizeLogValue is unexported.
	big := strings.Repeat("y", util.MaxLogField+10)
	got := util.TruncateStr(big)
	if len([]rune(got)) > util.MaxLogField+50 {
		t.Errorf("value not truncated: len=%d", len([]rune(got)))
	}
}

func TestDebugEventWritesJSONL(t *testing.T) {
	oldDir := util.DebugLogDir
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugLogDir = oldDir
		util.DebugMode = oldMode
		util.CloseDebugLogging()
	})

	util.DebugLogDir = t.TempDir()
	path := util.InitDebugLogging(true)
	if path == "" {
		t.Fatal("InitDebugLogging(true) returned empty path")
	}
	if !util.DebugMode {
		t.Fatal("DebugMode not set after InitDebugLogging(true)")
	}

	util.DebugEvent("test_event", "key", "value", "num", 42, "big", strings.Repeat("z", util.MaxLogField+5))

	util.CloseDebugLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(lines), data)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, lines[0])
	}
	if rec["event"] != "test_event" {
		t.Errorf("event field = %v, want test_event", rec["event"])
	}
	if rec["key"] != "value" {
		t.Errorf("key field = %v, want value", rec["key"])
	}
	if s, ok := rec["big"].(string); !ok || len([]rune(s)) > util.MaxLogField+50 {
		t.Errorf("big field not truncated: %v", rec["big"])
	}
}

func TestDebugEventNoopWhenDisabled(t *testing.T) {
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugMode = oldMode
	})
	// Must not panic.
	util.DebugEvent("should_not_emit")
}

// parseLogLines decodes each non-empty line of buf as a JSON object.
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// captureDebugLog swaps in a slog.Logger backed by an in-memory JSON handler
// writing to buf, and returns a cleanup that restores the prior state.

func TestDebugEventAtInfoLevel(t *testing.T) {
	// We test DebugEvent at INFO level by writing to a temp file.
	oldDir := util.DebugLogDir
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugLogDir = oldDir
		util.DebugMode = oldMode
		util.CloseDebugLogging()
	})

	util.DebugLogDir = t.TempDir()
	path := util.InitDebugLogging(true)
	if path == "" {
		t.Fatal("InitDebugLogging returned empty")
	}

	util.DebugEvent("info_event", "key", "value")
	util.CloseDebugLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	records := parseLogLines(t, bytes.NewBuffer(data))
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), string(data))
	}
	if records[0]["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", records[0]["level"])
	}
	if records[0]["event"] != "info_event" {
		t.Errorf("event = %v, want info_event", records[0]["event"])
	}
	if records[0]["key"] != "value" {
		t.Errorf("key = %v, want value", records[0]["key"])
	}
}

func TestDebugErrorWritesErrorLevel(t *testing.T) {
	oldDir := util.DebugLogDir
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugLogDir = oldDir
		util.DebugMode = oldMode
		util.CloseDebugLogging()
	})

	util.DebugLogDir = t.TempDir()
	path := util.InitDebugLogging(true)
	if path == "" {
		t.Fatal("InitDebugLogging returned empty")
	}

	util.DebugError("agent_error", "reason", "boom")
	util.CloseDebugLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	records := parseLogLines(t, bytes.NewBuffer(data))
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), string(data))
	}
	if records[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", records[0]["level"])
	}
	if records[0]["event"] != "agent_error" {
		t.Errorf("event = %v, want agent_error", records[0]["event"])
	}
	if records[0]["reason"] != "boom" {
		t.Errorf("reason = %v, want boom", records[0]["reason"])
	}
}

func TestDebugWarnWritesWarnLevel(t *testing.T) {
	oldDir := util.DebugLogDir
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugLogDir = oldDir
		util.DebugMode = oldMode
		util.CloseDebugLogging()
	})

	util.DebugLogDir = t.TempDir()
	path := util.InitDebugLogging(true)
	if path == "" {
		t.Fatal("InitDebugLogging returned empty")
	}

	util.DebugWarn("degraded", "hint", "slow")
	util.CloseDebugLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	records := parseLogLines(t, bytes.NewBuffer(data))
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), string(data))
	}
	if records[0]["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", records[0]["level"])
	}
	if records[0]["event"] != "degraded" {
		t.Errorf("event = %v, want degraded", records[0]["event"])
	}
	if records[0]["hint"] != "slow" {
		t.Errorf("hint = %v, want slow", records[0]["hint"])
	}
}

func TestDebugEventAtNoopWhenDisabled(t *testing.T) {
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugMode = oldMode
	})
	util.DebugMode = false
	// All severity entry points must be no-ops when disabled.
	util.DebugEvent("noop")
	util.DebugError("noop")
	util.DebugWarn("noop")
}

func TestDebugEventAtSanitizesFields(t *testing.T) {
	oldDir := util.DebugLogDir
	oldMode := util.DebugMode
	t.Cleanup(func() {
		util.DebugLogDir = oldDir
		util.DebugMode = oldMode
		util.CloseDebugLogging()
	})

	util.DebugLogDir = t.TempDir()
	path := util.InitDebugLogging(true)
	if path == "" {
		t.Fatal("InitDebugLogging returned empty")
	}

	big := strings.Repeat("z", util.MaxLogField+5)
	util.DebugError("big_event", "big", big)
	util.CloseDebugLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	records := parseLogLines(t, bytes.NewBuffer(data))
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), string(data))
	}
	if records[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", records[0]["level"])
	}
	if s, ok := records[0]["big"].(string); !ok || len([]rune(s)) > util.MaxLogField+50 {
		t.Errorf("big field not truncated: %v", records[0]["big"])
	}
}
