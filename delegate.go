package main

import (
	"fmt"
	"os"
	"strings"
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

// DelegateTaskArgs is the input schema for the delegate_task tool.
type DelegateTaskArgs struct {
	Goal      string `json:"goal"                doc:"The objective the sub-agent should accomplish"`
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
	// 1. Generate or resolve task_id
	taskID := input.TaskID
	if taskID == "" {
		taskID = GenerateTaskID()
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

	// 3. Build a restricted sub-agent via llmagent.New() with
	// blocked tools stripped from the toolset.
	subAgentTools, subAgentToolsets := buildSubAgentTools(input.AgentName, parentEnv)
	subAgentTools = filterBlockedTools(subAgentTools)

	genCfg := buildGenerationConfig("")

	subAgent, err := llmagent.New(llmagent.Config{
		Name:                  fmt.Sprintf("delegate_%s", input.AgentName),
		Description:           fmt.Sprintf("Delegated sub-agent for %s tasks", input.AgentName),
		Instruction:           buildSubAgentInstruction(input.AgentName, input.Context),
		Model:                 currentModel,
		Tools:                 subAgentTools,
		Toolsets:              subAgentToolsets,
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		return DelegateTaskResult{
			TaskID: taskID,
			Status: "failed",
			Error:  fmt.Sprintf("failed to create sub-agent: %v", err),
		}, err
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
		return DelegateTaskResult{
			TaskID: taskID,
			Status: "failed",
			Error:  fmt.Sprintf("failed to create sub-agent runner: %v", err),
		}, err
	}

	for ev, runErr := range subRunner.Run(ctx, "delegator", taskID, msg, agent.RunConfig{}) {
		if runErr != nil {
			finalErr = runErr
			break
		}
		if ev == nil {
			continue
		}
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					summary.WriteString(part.Text)
				}
				if part.FunctionCall != nil {
					if isFileOpTool(part.FunctionCall.Name) {
						filesModified = append(filesModified, extractFilePath(part.FunctionCall.Args))
					}
				}
			}
		}
	}

	status := "completed"
	if finalErr != nil {
		status = "failed"
	}

	// 5. Clean up session entry
	sessionMgr.CleanupInactive(0)

	return DelegateTaskResult{
		TaskID:        taskID,
		Status:        status,
		Summary:       strings.TrimSpace(summary.String()),
		FilesModified: filesModified,
		Error:         fmt.Sprintf("%v", finalErr),
	}, nil
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
func buildSubAgentTools(agentName string, parentEnv []string) ([]tool.Tool, []tool.Toolset) {
	switch agentName {
	case "code_interpreter":
		pyTool, _ := createPythonTool(nil, parentEnv)
		return []tool.Tool{pyTool}, nil
	case "web_researcher":
		dlTool, _ := createDownloadTool()
		return []tool.Tool{dlTool}, []tool.Toolset{currentMCPToolset}
	case "general_purpose":
		fileTools, _ := createFileOpsTools(nil, nil, "")
		return fileTools, nil
	default:
		allTools, _ := createAllTools(nil, parentEnv)
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
