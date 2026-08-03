package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestTruncateStr(t *testing.T) {
	short := "hello"
	if got := truncateStr(short); got != short {
		t.Errorf("truncateStr(%q) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("x", maxLogField+100)
	got := truncateStr(long)
	// truncated string includes a "...[truncated N runes]" marker, so it
	// will exceed maxLogField by the marker length.
	if len([]rune(got)) > maxLogField+60 {
		t.Errorf("truncateStr did not truncate: len=%d", len([]rune(got)))
	}
	if !strings.Contains(got, "truncated 100 runes") {
		t.Errorf("truncateStr missing truncation marker: %q", got)
	}
}

func TestSanitizeLogValue(t *testing.T) {
	big := strings.Repeat("y", maxLogField+10)
	in := map[string]any{"a": big, "b": []any{big, "small"}}
	out := sanitizeLogValue(in).(map[string]any)
	if s := out["a"].(string); len([]rune(s)) > maxLogField+50 {
		t.Errorf("map value not truncated: len=%d", len([]rune(s)))
	}
	if s := out["b"].([]any)[0].(string); len([]rune(s)) > maxLogField+50 {
		t.Errorf("slice value not truncated: len=%d", len([]rune(s)))
	}
	if out["b"].([]any)[1] != "small" {
		t.Errorf("small slice value mangled: %v", out["b"].([]any)[1])
	}
}

func TestDebugEventWritesJSONL(t *testing.T) {
	oldDir := debugLogDir
	oldMode := debugMode
	t.Cleanup(func() {
		debugLogDir = oldDir
		debugMode = oldMode
		closeDebugLogging()
		debugLog = nil
	})

	debugLogDir = t.TempDir()
	path := initDebugLogging(true)
	if path == "" {
		t.Fatal("initDebugLogging(true) returned empty path")
	}
	if !debugMode {
		t.Fatal("debugMode not set after initDebugLogging(true)")
	}

	debugEvent("test_event", "key", "value", "num", 42, "big", strings.Repeat("z", maxLogField+5))

	closeDebugLogging()

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
	if s, ok := rec["big"].(string); !ok || len([]rune(s)) > maxLogField+50 {
		t.Errorf("big field not truncated: %v", rec["big"])
	}
}

func TestDebugEventNoopWhenDisabled(t *testing.T) {
	oldMode := debugMode
	oldLog := debugLog
	t.Cleanup(func() {
		debugMode = oldMode
		debugLog = oldLog
	})
	debugMode = false
	debugLog = nil
	// Must not panic and must not write anything.
	debugEvent("should_not_emit")
}

// captureDebugLog swaps in a slog.Logger backed by an in-memory JSON handler
// writing to buf, and returns a cleanup that restores the prior state. The
// handler level is Debug so all severities are emitted.
func captureDebugLog(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	oldMode := debugMode
	oldLog := debugLog
	t.Cleanup(func() {
		debugMode = oldMode
		debugLog = oldLog
	})
	debugMode = true
	debugLog = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
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

func TestDebugEventAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	captureDebugLog(t, &buf)

	debugEvent("info_event", "key", "value")

	records := parseLogLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), buf.String())
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
	var buf bytes.Buffer
	captureDebugLog(t, &buf)

	debugError("agent_error", "reason", "boom")

	records := parseLogLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), buf.String())
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
	var buf bytes.Buffer
	captureDebugLog(t, &buf)

	debugWarn("degraded", "hint", "slow")

	records := parseLogLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), buf.String())
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
	oldMode := debugMode
	oldLog := debugLog
	t.Cleanup(func() {
		debugMode = oldMode
		debugLog = oldLog
	})
	debugMode = false
	debugLog = nil
	// All severity entry points must be no-ops when debugLog is nil.
	debugEventAt(slog.LevelInfo, "noop")
	debugError("noop")
	debugWarn("noop")
}

func TestDebugEventAtSanitizesFields(t *testing.T) {
	var buf bytes.Buffer
	captureDebugLog(t, &buf)

	big := strings.Repeat("z", maxLogField+5)
	debugError("big_event", "big", big)

	records := parseLogLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(records), buf.String())
	}
	if records[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", records[0]["level"])
	}
	if s, ok := records[0]["big"].(string); !ok || len([]rune(s)) > maxLogField+50 {
		t.Errorf("big field not truncated: %v", records[0]["big"])
	}
}
