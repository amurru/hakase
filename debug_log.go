package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// debugMode reports whether dev-mode structured JSON logging is enabled.
var debugMode bool

// debugLog is the structured JSON logger used in dev mode; nil when disabled.
var debugLog *slog.Logger

// debugLogFile is the open JSONL file backing debugLog; closed on shutdown.
var debugLogFile *os.File

// debugLogDir is where structured log files are written. Overridable in tests.
var debugLogDir = "logs"

// maxLogField caps any single string value emitted to the JSON log so large
// payloads (python code, file contents) do not bloat the file.
const maxLogField = 2000

// truncateStr caps s to maxLogField runes with a truncation marker.
func truncateStr(s string) string {
	r := []rune(s)
	if len(r) <= maxLogField {
		return s
	}
	return string(r[:maxLogField]) + fmt.Sprintf("...[truncated %d runes]", len(r)-maxLogField)
}

// sanitizeLogValue recursively truncates long strings inside maps and slices
// so structured fields stay bounded.
func sanitizeLogValue(v any) any {
	switch t := v.(type) {
	case string:
		return truncateStr(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = sanitizeLogValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeLogValue(val)
		}
		return out
	default:
		return v
	}
}

// initDebugLogging enables structured JSON logging when enabled is true.
// It creates the log directory, opens a timestamped JSONL file, and installs
// the slog handler. Returns the log file path, or "" when disabled or on
// failure (failures are silent - debug logging is best-effort).
func initDebugLogging(enabled bool) string {
	debugMode = enabled
	debugLog = nil
	debugLogFile = nil
	if !enabled {
		return ""
	}
	if err := os.MkdirAll(debugLogDir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(debugLogDir, fmt.Sprintf("hakase-debug-%s.jsonl", time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ""
	}
	debugLogFile = f
	debugLog = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return path
}

// closeDebugLogging flushes and closes the JSONL file. Safe to call when disabled.
func closeDebugLogging() {
	if debugLogFile != nil {
		_ = debugLogFile.Close()
		debugLogFile = nil
	}
}

// debugEventAt emits one structured JSON line at the given slog level when dev
// mode is enabled. event is the event type (e.g. "status_log",
// "subagent_tool_call"). fields is an alternating key/value list; values are
// sanitized before serialization. No-op when debug mode is off (or debugLog is
// nil). slog.Logger is safe for concurrent use. The slog JSONHandler emits the
// level under its standard "level" key; the event type is carried in the
// "event" field, preserving the existing JSONL schema.
func debugEventAt(level slog.Level, event string, fields ...any) {
	if debugLog == nil {
		return
	}
	sanitized := make([]any, 0, len(fields)+2)
	sanitized = append(sanitized, "event", event)
	for i := 0; i+1 < len(fields); i += 2 {
		sanitized = append(sanitized, fields[i], sanitizeLogValue(fields[i+1]))
	}
	debugLog.Log(nil, level, event, sanitized...)
}

// debugEvent emits one structured JSON line at INFO level when dev mode is
// enabled. It is a thin wrapper around debugEventAt for backward
// compatibility; existing call sites keep their byte-identical output.
func debugEvent(event string, fields ...any) {
	debugEventAt(slog.LevelInfo, event, fields...)
}

// debugError emits a structured JSON line at ERROR level when dev mode is on.
// No-op when debug mode is off.
func debugError(event string, fields ...any) {
	debugEventAt(slog.LevelError, event, fields...)
}

// debugWarn emits a structured JSON line at WARN level when dev mode is on.
// No-op when debug mode is off.
func debugWarn(event string, fields ...any) {
	debugEventAt(slog.LevelWarn, event, fields...)
}
