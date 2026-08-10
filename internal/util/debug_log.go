package util

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// DebugMode reports whether dev-mode structured JSON logging is enabled.
var DebugMode bool

// debugLog is the structured JSON logger used in dev mode; nil when disabled.
var debugLog *slog.Logger

// debugLogFile is the open JSONL file backing debugLog; closed on shutdown.
var debugLogFile *os.File

// DebugLogDir is where structured log files are written. Overridable in tests.
var DebugLogDir = "logs"

// MaxLogField caps any single string value emitted to the JSON log so large
// payloads (python code, file contents) do not bloat the file.
const MaxLogField = 2000

// TruncateStr caps s to MaxLogField runes with a truncation marker.
func TruncateStr(s string) string {
	r := []rune(s)
	if len(r) <= MaxLogField {
		return s
	}
	return string(r[:MaxLogField]) + fmt.Sprintf("...[truncated %d runes]", len(r)-MaxLogField)
}

// sanitizeLogValue recursively truncates long strings inside maps and slices
// so structured fields stay bounded.
func sanitizeLogValue(v any) any {
	switch t := v.(type) {
	case string:
		return TruncateStr(t)
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

// InitDebugLogging enables structured JSON logging when enabled is true.
// It creates the log directory, opens a timestamped JSONL file, and installs
// the slog handler. Returns the log file path, or "" when disabled or on
// failure (failures are silent - debug logging is best-effort).
func InitDebugLogging(enabled bool) string {
	DebugMode = enabled
	debugLog = nil
	debugLogFile = nil
	if !enabled {
		return ""
	}
	if err := os.MkdirAll(DebugLogDir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(DebugLogDir, fmt.Sprintf("hakase-debug-%s.jsonl", time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ""
	}
	debugLogFile = f
	debugLog = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return path
}

// CloseDebugLogging flushes and closes the JSONL file. Safe to call when disabled.
func CloseDebugLogging() {
	if debugLogFile != nil {
		_ = debugLogFile.Close()
		debugLogFile = nil
	}
}

// debugEventAt emits one structured JSON line at the given slog level when dev
// mode is enabled.
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

// DebugEvent emits one structured JSON line at INFO level when dev mode is
// enabled.
func DebugEvent(event string, fields ...any) {
	debugEventAt(slog.LevelInfo, event, fields...)
}

// DebugError emits a structured JSON line at ERROR level when dev mode is on.
func DebugError(event string, fields ...any) {
	debugEventAt(slog.LevelError, event, fields...)
}

// DebugWarn emits a structured JSON line at WARN level when dev mode is on.
func DebugWarn(event string, fields ...any) {
	debugEventAt(slog.LevelWarn, event, fields...)
}
