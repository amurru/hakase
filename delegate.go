package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// blockedTools lists tool names that leaf sub-agents must not have
// access to, preventing recursion and unauthorized operations.
var blockedTools = map[string]bool{
	"delegate_task": true,
	"clarify":       true,
	"memory":        true,
	"send_message":  true,
	"cronjob":       true,
}

// delegateTimeout bounds how long a delegated sub-agent may run before being
// aborted. Set in setupRunner from config; 0 disables the timeout.
var delegateTimeout time.Duration

// delegationCacheTTL is how long a completed delegation result is reused to
// answer identical (normalized) goals without re-spawning a sub-agent.
var delegationCacheTTL = 10 * time.Minute

// delegationCacheEntry stores a terminal delegation result and its timestamp.
type delegationCacheEntry struct {
	result DelegateTaskResult
	ts     time.Time
}

// delegationCache deduplicates recent delegations by normalized goal so the
// orchestrator does not re-spawn identical sub-agent work that already
// completed (or timed out) within the TTL window.
var delegationCache = struct {
	mu sync.Mutex
	m  map[string]delegationCacheEntry
}{m: make(map[string]delegationCacheEntry)}

// normalizeDelegationGoal canonicalizes a goal string for cache keying:
// lowercase, collapse all whitespace runs to single spaces, truncate to
// 200 runes.
func normalizeDelegationGoal(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	joined := strings.Join(strings.Fields(s), " ")
	r := []rune(joined)
	if len(r) > 200 {
		return string(r[:200])
	}
	return joined
}

// delegationCacheGet returns a cached, non-expired result for norm.
func delegationCacheGet(norm string) (DelegateTaskResult, bool) {
	delegationCache.mu.Lock()
	defer delegationCache.mu.Unlock()
	e, ok := delegationCache.m[norm]
	if !ok {
		return DelegateTaskResult{}, false
	}
	if time.Since(e.ts) > delegationCacheTTL {
		delete(delegationCache.m, norm)
		return DelegateTaskResult{}, false
	}
	return e.result, true
}

// delegationCachePut stores a terminal delegation result keyed by norm.
func delegationCachePut(norm string, r DelegateTaskResult) {
	delegationCache.mu.Lock()
	defer delegationCache.mu.Unlock()
	delegationCache.m[norm] = delegationCacheEntry{result: r, ts: time.Now()}
}

// delegationProgressNotify is set by main and streams DelegationProgressMsg
// events from delegated sub-agents to the TUI. Status values: started,
// running, thinking, tool_call, tool_result, log, completed, failed, timed_out.
var delegationProgressNotify func(status string, taskID, agent, message string)

func notifyDelegation(status string, taskID, agent, message string) {
	debugEvent("delegation_progress", "task_id", taskID, "agent", agent, "status", status, "message", message)
	if delegationProgressNotify != nil {
		delegationProgressNotify(status, taskID, agent, message)
	}
}

// delegationReporter buffers and streams sub-agent output to the TUI through
// delegationProgressNotify, throttling high-frequency text chunks so the log
// pane is not flooded with tiny model tokens.
type delegationReporter struct {
	taskID    string
	agent     string
	textBuf   strings.Builder
	toolStart map[string]time.Time
}

func newDelegationReporter(taskID, agent string) *delegationReporter {
	return &delegationReporter{taskID: taskID, agent: agent, toolStart: make(map[string]time.Time)}
}

// truncate caps s to n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (r *delegationReporter) started(goal string) {
	notifyDelegation("started", r.taskID, r.agent, truncate(goal, 200))
}

func (r *delegationReporter) log(msg string) {
	notifyDelegation("log", r.taskID, r.agent, msg)
}

// flushText emits any buffered text as a single "running" event.
func (r *delegationReporter) flushText() {
	if r.textBuf.Len() == 0 {
		return
	}
	msg := strings.TrimSpace(r.textBuf.String())
	r.textBuf.Reset()
	if msg != "" {
		notifyDelegation("running", r.taskID, r.agent, msg)
	}
}

// text buffers a model text chunk, flushing on newlines or when the buffer is
// large enough to be readable.
func (r *delegationReporter) text(chunk string) {
	r.textBuf.WriteString(chunk)
	if strings.Contains(chunk, "\n") || r.textBuf.Len() >= 240 {
		r.flushText()
	}
}

// thought streams a thinking chunk, truncated to keep the log pane usable.
func (r *delegationReporter) thought(chunk string) {
	trimmed := strings.TrimSpace(chunk)
	if trimmed != "" {
		notifyDelegation("thinking", r.taskID, r.agent, truncate(trimmed, 240))
	}
}

// toolCall records the start of a tool call and reports it.
func (r *delegationReporter) toolCall(name string, args map[string]interface{}) {
	r.flushText()
	r.toolStart[name] = time.Now()
	notifyDelegation("tool_call", r.taskID, r.agent, fmt.Sprintf("%s(%v)", name, args))
}

// toolResult reports a completed tool call with its execution duration.
func (r *delegationReporter) toolResult(name string) {
	msg := name
	if start, ok := r.toolStart[name]; ok {
		msg = fmt.Sprintf("%s (%.1fs)", name, time.Since(start).Seconds())
		delete(r.toolStart, name)
	}
	notifyDelegation("tool_result", r.taskID, r.agent, msg)
}

func (r *delegationReporter) finish(status string, err error, summary string) {
	r.flushText()
	switch status {
	case "timed_out":
		notifyDelegation("timed_out", r.taskID, r.agent, fmt.Sprintf("%v", err))
	case "failed":
		notifyDelegation("failed", r.taskID, r.agent, fmt.Sprintf("%v", err))
	default:
		notifyDelegation("completed", r.taskID, r.agent, truncate(summary, 240))
	}
}

// DelegateTaskArgs is the input schema for the delegate_task tool.
type DelegateTaskArgs struct {
	Goal      string `json:"goal"                doc:"The objective the sub-agent should accomplish (required). Note: there is no 'prompt' field - put the objective in 'goal'."`
	Context   string `json:"context,omitempty"   doc:"Additional context or background information for the sub-agent"`
	AgentName string `json:"agent_name,omitempty" doc:"Target sub-agent type: code_interpreter, web_researcher, or general_purpose"`
	TaskID    string `json:"task_id,omitempty"   doc:"Optional task ID for tracking; auto-generated if omitted"`
}

// DelegateTaskResult is the output schema for the delegate_task tool.
type DelegateTaskResult struct {
	TaskID        string   `json:"task_id"           doc:"The task ID assigned to this delegation"`
	Status        string   `json:"status"            doc:"Execution status: completed, failed, or timed_out"`
	Summary       string   `json:"summary"           doc:"Concise summary of the sub-agent's result"`
	FilesModified []string `json:"files_modified,omitempty" doc:"List of file paths modified by the sub-agent"`
	Error         string   `json:"error,omitempty"   doc:"Error message if the delegation failed"`
}

// delegateTaskHandler creates a task-scoped sub-agent execution.
// Each delegation gets its own task_id, session, and restricted
// toolset. The sub-agent runs in an isolated runner with a fresh
// InMemoryService.
func delegateTaskHandler(ctx agent.Context, input DelegateTaskArgs) (DelegateTaskResult, error) {
	// Dedupe: if an equivalent goal completed recently, return the cached
	// result without spawning a duplicate sub-agent.
	normGoal := normalizeDelegationGoal(input.Goal)
	if cached, ok := delegationCacheGet(normGoal); ok {
		return cached, nil
	}

	// 1. Generate or resolve task_id
	taskID := input.TaskID
	if taskID == "" {
		taskID = GenerateTaskID()
	}

	agentLabel := input.AgentName
	if agentLabel == "" {
		agentLabel = "default"
	}

	// Capture the parent process environment so the sub-agent
	// can inherit it even if the ADK runner strips env vars.
	parentEnv := os.Environ()

	// 2. Create SessionManager entry for this task_id
	sessionMgr := NewSessionManager(5 * time.Minute)
	defer sessionMgr.StopCleanup()

	cwd, _ := os.Getwd()
	sessionMgr.GetOrCreateSession(taskID, cwd)
	sessionMgr.RecordCWD(taskID, cwd)

	reporter := newDelegationReporter(taskID, agentLabel)
	reporter.started(input.Goal)

	// 3. Build a restricted sub-agent via llmagent.New() with
	// blocked tools stripped from the toolset.
	subAgentLogFunc := func(msg string) { reporter.log(msg) }
	subAgentTools, subAgentToolsets := buildSubAgentTools(input.AgentName, parentEnv, subAgentLogFunc)
	subAgentTools = filterBlockedTools(subAgentTools)

	genCfg := buildGenerationConfig("")

	subAgent, err := llmagent.New(llmagent.Config{
		Name:                  fmt.Sprintf("delegate_%s", agentLabel),
		Description:           fmt.Sprintf("Delegated sub-agent for %s tasks", agentLabel),
		Instruction:           buildSubAgentInstruction(input.AgentName, input.Context),
		Model:                 currentModel,
		Tools:                 subAgentTools,
		Toolsets:              subAgentToolsets,
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		reporter.finish("failed", err, "")
		result := DelegateTaskResult{
			TaskID: taskID,
			Status: "failed",
			Error:  fmt.Sprintf("failed to create sub-agent: %v", err),
		}
		delegationCachePut(normGoal, result)
		return result, err
	}
	// 4. Run the sub-agent in an isolated runner with fresh
	// session.InMemoryService().
	msg := genai.NewContentFromText(input.Goal, genai.RoleUser)

	var summary strings.Builder
	var filesModified []string
	var finalErr error

	subRunner, err := runner.New(runner.Config{
		AppName:           fmt.Sprintf("hakase_delegation_%s", taskID),
		Agent:             subAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		reporter.finish("failed", err, "")
		result := DelegateTaskResult{
			TaskID: taskID,
			Status: "failed",
			Error:  fmt.Sprintf("failed to create sub-agent runner: %v", err),
		}
		delegationCachePut(normGoal, result)
		return result, err
	}

	// Bound the sub-agent run so a stuck sub-agent fails loudly instead of
	// hanging the orchestrator indefinitely. The watchdog cancels after
	// delegateTimeout of inactivity; a hard ceiling of 3x delegateTimeout
	// catches agents that emit events forever without finishing. When
	// delegateTimeout <= 0 the watchdog is disabled (ctx stays as-is),
	// preserving the prior no-timeout behavior.
	var runCtx context.Context = ctx
	var cancel context.CancelFunc
	watchdogActive := delegateTimeout > 0
	var lastActivityMu sync.Mutex
	lastActivity := time.Now()
	touchActivity := func() {
		lastActivityMu.Lock()
		lastActivity = time.Now()
		lastActivityMu.Unlock()
	}
	done := make(chan struct{})
	if watchdogActive {
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			ceiling := time.NewTimer(3 * delegateTimeout)
			defer ceiling.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					lastActivityMu.Lock()
					idle := time.Since(lastActivity)
					lastActivityMu.Unlock()
					if idle > delegateTimeout {
						cancel()
						return
					}
				case <-ceiling.C:
					cancel()
					return
				}
			}
		}()
	}

	// Run loop with tool-call JSON repair retry (P0-2). When the provider
	// rejects a malformed tool-call argument payload, re-enter the runner
	// with a corrective user message instead of aborting the delegation.
	// The degeneration guard (repetition / text-only bloat) cancels the run
	// when the sub-agent starts looping, independent of the watchdog.
	guard := guardDefaults(currentGuard)
	guardCtx, guardCancel := context.WithCancel(runCtx)
	defer guardCancel()
	attempt := 0
	for {
		repaired := false
		for ev, runErr := range subRunner.Run(guardCtx, "delegator", taskID, msg, agent.RunConfig{}) {
			if runErr != nil {
				if isToolCallJSONErr(runErr) && attempt < maxToolCallRepairAttempts {
					debugWarn("tool_call_repair", "agent", agentLabel, "attempt", attempt+1, "error", runErr)
					msg = toolCallRepairMessage(runErr, attempt)
					attempt++
					repaired = true
					break
				}
				finalErr = runErr
				break
			}
			if ev == nil {
				continue
			}
			touchActivity()
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						summary.WriteString(part.Text)
						debugEvent("subagent_text", "task_id", taskID, "agent", agentLabel, "thought", part.Thought, "text", part.Text)
						if !part.Thought {
							if reason := guard.feed(part.FunctionCall != nil, part.Text); reason != "" {
								guardCancel()
								debugWarn("subagent_guard_abort", "task_id", taskID, "agent", agentLabel, "reason", reason, "error", guardReasonLog(reason))
								finalErr = fmt.Errorf("sub-agent %s aborted: %s", agentLabel, reason)
								break
							}
						}
						if part.Thought {
							reporter.thought(part.Text)
						} else {
							reporter.text(part.Text)
						}
					}
					if part.FunctionCall != nil {
						if isFileOpTool(part.FunctionCall.Name) {
							filesModified = append(filesModified, extractFilePath(part.FunctionCall.Args))
						}
						reporter.toolCall(part.FunctionCall.Name, part.FunctionCall.Args)
						debugEvent("subagent_tool_call", "task_id", taskID, "agent", agentLabel, "tool", part.FunctionCall.Name, "args", part.FunctionCall.Args)
					}
					if part.FunctionResponse != nil {
						reporter.toolResult(part.FunctionResponse.Name)
						debugEvent("subagent_tool_response", "task_id", taskID, "agent", agentLabel, "tool", part.FunctionResponse.Name, "response", part.FunctionResponse.Response)
					}
				}
			}
		}
		if !repaired {
			break
		}
	}
	close(done)

	status := "completed"
	if watchdogActive && runCtx.Err() != nil {
		// Watchdog (inactivity or hard ceiling) canceled the run.
		status = "timed_out"
		finalErr = fmt.Errorf("sub-agent %s did not complete within %v", agentLabel, delegateTimeout)
	} else if finalErr != nil {
		status = "failed"
	}
	if status == "timed_out" {
		debugWarn("delegation_timed_out", "task_id", taskID, "agent", agentLabel, "error", finalErr)
	} else if status == "failed" {
		debugError("delegation_failed", "task_id", taskID, "agent", agentLabel, "error", finalErr)
	}

	// 5. Clean up session entry
	sessionMgr.CleanupInactive(0)

	reporter.finish(status, finalErr, strings.TrimSpace(summary.String()))

	result := DelegateTaskResult{
		TaskID:        taskID,
		Status:        status,
		Summary:       strings.TrimSpace(summary.String()),
		FilesModified: filesModified,
		Error:         fmt.Sprintf("%v", finalErr),
	}
	delegationCachePut(normGoal, result)
	return result, nil
}

// filterBlockedTools removes blocked tools from a tool list.
func filterBlockedTools(tools []tool.Tool) []tool.Tool {
	var filtered []tool.Tool
	for _, t := range tools {
		name := toolName(t)
		if !blockedTools[name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// toolName extracts the tool name from a tool.Tool.
func toolName(t tool.Tool) string {
	return t.Name()
}

// isFileOpTool reports whether a tool name corresponds to a file operation.
func isFileOpTool(name string) bool {
	switch name {
	case "read_file", "write_file", "patch", "search_files", "save_skill":
		return true
	default:
		return false
	}
}

// extractFilePath attempts to extract a file path from tool call args.
func extractFilePath(args map[string]interface{}) string {
	if path, ok := args["path"].(string); ok {
		return path
	}
	return ""
}

// buildSubAgentTools returns the appropriate toolset and toolsets for a
// sub-agent type. parentEnv is the captured parent process environment passed
// through so the sub-agent's tools can use it even if the ADK runner strips
// env vars.
func buildSubAgentTools(agentName string, parentEnv []string, log LogFunc) ([]tool.Tool, []tool.Toolset) {
	switch agentName {
	case "code_interpreter":
		pyTool, _ := createPythonTool(log, parentEnv)
		return []tool.Tool{pyTool}, nil
	case "web_researcher":
		dlTool, _ := createDownloadTool()
		return []tool.Tool{dlTool}, []tool.Toolset{currentMCPToolset}
	case "general_purpose":
		fileTools, _ := createFileOpsTools(log, nil, "")
		return fileTools, nil
	default:
		allTools, _ := createAllTools(log, parentEnv)
		return filterBlockedTools(allTools), []tool.Toolset{currentMCPToolset}
	}
}

// createAllTools returns all available tools (used as fallback for unknown agent types).
func createAllTools(log LogFunc, parentEnv []string) ([]tool.Tool, error) {
	var tools []tool.Tool

	dlTool, err := createDownloadTool()
	if err == nil {
		tools = append(tools, dlTool)
	}

	pyTool, err := createPythonTool(log, parentEnv)
	if err == nil {
		tools = append(tools, pyTool)
	}

	fileTools, err := createFileOpsTools(log, nil, "")
	if err == nil {
		tools = append(tools, fileTools...)
	}

	sysTools, err := createSystemExecTools(log, nil, "")
	if err == nil {
		tools = append(tools, sysTools...)
	}

	return tools, nil
}

// buildSubAgentInstruction returns a system instruction for a sub-agent
// based on its type, with context from the parent orchestrator.
func buildSubAgentInstruction(agentName string, context string) string {
	base := fmt.Sprintf("You are a delegated sub-agent of type %s. Execute the given task and return a concise result.\n", agentName)
	if context != "" {
		base += fmt.Sprintf("Context provided by the orchestrator:\n%s\n\n", context)
	}
	base += "Return your result as a concise summary. Do not call delegate_task, clarify, memory, send_message, or cronjob."
	return base
}

// registerDelegateTaskTool registers the delegate_task tool on the
// orchestrator agent and returns the tool for inclusion in the agent's
// tool list.
func registerDelegateTaskTool(log LogFunc) (tool.Tool, error) {
	return newDocTool(functiontool.Config{
		Name:        "delegate_task",
		Description: "Delegates a task to an isolated sub-agent with its own task-scoped session, restricted toolset, and environment isolation. Use when a task requires a different specialist agent or when you want to run work in an isolated context. The sub-agent cannot call delegate_task, clarify, memory, send_message, or cronjob.",
	}, delegateTaskHandler)
}
