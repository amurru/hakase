package hooks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// DryRunResult is the raw outcome of dry-running one handler with a sample
// payload (`hakase hooks test`). Blocked mirrors the gate verdict the
// handler WOULD produce on a matching call; Context is the injectable
// context (PostToolUse additionalContext, SessionStart output).
type DryRunResult struct {
	ExitCode   int
	TimedOut   bool
	Blocked    bool
	Reason     string
	Context    string
	HookErr    string
	DurationMs int64
}

// DryRun executes one handler with a sample payload and reports the raw
// outcome. It deliberately bypasses trust: the caller warns that review
// requires execution.
func DryRun(event string, h Handler) DryRunResult {
	var res DryRunResult
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds
	}
	p := samplePayload(event)
	stdin, err := json.Marshal(p)
	if err != nil {
		res.HookErr = err.Error()
		return res
	}
	start := time.Now()
	stdout, stderr, timedOut, runErr := runCommand(context.Background(), time.Duration(timeout)*time.Second, h.Command, hookEnv(event, p), p.CWD, stdin)
	res.DurationMs = time.Since(start).Milliseconds()
	if timedOut {
		res.TimedOut = true
		return res
	}
	res.ExitCode = exitCodeOf(runErr, &res.HookErr)
	if res.HookErr != "" {
		return res
	}
	if event == EventSessionStart {
		if res.ExitCode != 0 {
			res.HookErr = "exit " + strconv.Itoa(res.ExitCode)
			return res
		}
		ctxOut, err := sessionContextFromStdout(stdout)
		if err != nil {
			res.HookErr = "invalid JSON: " + err.Error()
			return res
		}
		res.Context = ctxOut
		return res
	}
	v := parseExit(timeout, res.ExitCode, stdout, stderr)
	res.Blocked, res.Reason, res.Context, res.HookErr = v.block, v.reason, v.additionalContext, v.hookErr
	return res
}

// exitCodeOf maps a runCommand error to an exit code, reporting exec
// failures (not ExitErrors) through hookErr.
func exitCodeOf(runErr error, hookErr *string) int {
	if runErr == nil {
		return 0
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	*hookErr = "exec failed: " + runErr.Error()
	return -1
}

// samplePayload builds the synthetic payload for a dry run.
func samplePayload(event string) payload {
	cwd, _ := os.Getwd()
	p := payload{
		HookEventName: event,
		SessionID:     "dry-run",
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		CWD:           cwd,
	}
	switch event {
	case EventPreToolUse:
		p.ToolName = "system_exec"
		p.ToolInput = map[string]any{"command": "echo hook-test"}
	case EventPostToolUse:
		p.ToolName = "system_exec"
		p.ToolInput = map[string]any{"command": "echo hook-test"}
		p.ToolResponse = map[string]any{"ok": true}
	}
	return p
}
