// slash_commands.go - /board and /mcp slash command handlers for the TUI.
// These were formerly in root (task_slash.go, mcp_slash.go) and referenced
// root type aliases. Now they import internal packages directly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"amurru/hakase/internal/agent"
	mcp "amurru/hakase/internal/mcp"
	"amurru/hakase/internal/sidekick"
	"amurru/hakase/internal/tui"

	tea "charm.land/bubbletea/v2"
)

// =========================================================================
// /mcp command (formerly mcp_slash.go)
// =========================================================================

func runMCPCommand(m *tui.AppModel, args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return m.ToggleMCPList()
	}
	sub := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
	switch sub {
	case "list", "status", "ls":
		return mcpListCmd(m)
	case "enable", "on":
		return mcpSetEnabledCmd(m, rest, false)
	case "disable", "off":
		return mcpSetEnabledCmd(m, rest, true)
	case "reconnect":
		return mcpReconnectCmd(m, rest)
	case "help":
		m.AppendLog("/mcp [list] | /mcp enable <name> | /mcp disable <name> | /mcp reconnect <name>")
		return nil
	default:
		m.AppendLog(fmt.Sprintf("unknown /mcp subcommand %q (try: list, enable, disable, reconnect)", sub))
		return nil
	}
}

func mcpManager(m *tui.AppModel) *mcp.MCPServerManager {
	if mcp.MCPManager == nil {
		m.AppendLog("MCP manager is not available (no usable MCP config)")
		return nil
	}
	return mcp.MCPManager
}

func mcpListCmd(m *tui.AppModel) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	servers := mg.ListServers()
	if len(servers) == 0 {
		m.AppendLog("No MCP servers configured. Add an \"mcp\" block to config.json or ~/.hakase/mcp.json.")
		return nil
	}
	m.AppendLog("MCP Servers")
	for _, s := range servers {
		tools := "-"
		if s.Status == "connected" {
			tools = fmt.Sprintf("%d tools", s.ToolCount)
		}
		line := fmt.Sprintf("  %s %s  %s  %s", mcpStatusGlyph(s), s.Name, s.Transport, tools)
		if s.Status == "failed" && s.Error != "" {
			line += fmt.Sprintf("  (%s)", s.Error)
		}
		m.AppendLog(line)
	}
	return nil
}

func mcpSetEnabledCmd(m *tui.AppModel, args string, disable bool) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		verb := "enable"
		if disable {
			verb = "disable"
		}
		m.AppendLog(fmt.Sprintf("Usage: /mcp %s <name>", verb))
		return nil
	}
	name := fields[0]
	if err := mg.SetDisabled(name, disable); err != nil {
		m.AppendLog(fmt.Sprintf("failed to %s %q: %v", toggleVerb(disable), name, err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("MCP server %q %s", name, toggleVerb(disable)+"d"))
	m.RefreshMCPList()
	return nil
}

func mcpReconnectCmd(m *tui.AppModel, args string) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.AppendLog("Usage: /mcp reconnect <name>")
		return nil
	}
	name := fields[0]
	if err := mg.Reconnect(name); err != nil {
		m.AppendLog(fmt.Sprintf("failed to reconnect %q: %v", name, err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Reconnecting MCP server %q on next tool fetch", name))
	m.RefreshMCPList()
	return nil
}

func mcpStatusGlyph(s mcp.MCPServerStatus) string {
	switch s.Status {
	case "connected":
		return "*"
	case "disabled":
		return "o"
	case "failed":
		return "x"
	default:
		return "~"
	}
}

func toggleVerb(disable bool) string {
	if disable {
		return "disable"
	}
	return "enable"
}

// =========================================================================
// /board command (formerly task_slash.go)
// =========================================================================

func runBoardCommand(m *tui.AppModel, args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return boardSummary(m)
	}
	sub := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
	switch sub {
	case "list":
		return boardList(m, rest)
	case "new", "create", "add":
		return boardNew(m, rest)
	case "get":
		return boardGet(m, rest)
	case "update":
		return boardUpdate(m, rest)
	case "done", "complete":
		return boardDone(m, rest)
	case "fail":
		return boardFail(m, rest)
	case "cancel":
		return boardCancel(m, rest)
	case "delete", "rm":
		return boardDelete(m, rest)
	case "archive":
		return boardArchive(m, rest)
	case "claim":
		return boardClaim(m, rest)
	case "summary":
		return boardSummary(m)
	default:
		m.AppendLog(fmt.Sprintf("unknown /board subcommand %q (try: summary, list, new, get, update, done, fail, cancel, delete, archive, claim)", sub))
		return nil
	}
}

func boardBlockWarn(m *tui.AppModel) tea.Cmd {
	m.AppendLog("cannot modify the task board while the agent is working")
	return nil
}

func boardSummary(m *tui.AppModel) tea.Cmd {
	registry, err := agent.LoadTaskRegistry()
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to load tasks: %v", err))
		return nil
	}
	summary := map[agent.TaskStatus]int{
		agent.TaskStatusPending:    0,
		agent.TaskStatusInProgress: 0,
		agent.TaskStatusCompleted:  0,
		agent.TaskStatusFailed:     0,
		agent.TaskStatusCancelled:  0,
		agent.TaskStatusSkipped:    0,
		agent.TaskStatusBlocked:    0,
		agent.TaskStatusArchived:   0,
	}
	for _, task := range registry.Tasks {
		summary[task.Status]++
	}
	m.AppendLog("Task Board")
	statusOrder := []agent.TaskStatus{
		agent.TaskStatusPending, agent.TaskStatusInProgress, agent.TaskStatusCompleted,
		agent.TaskStatusFailed, agent.TaskStatusCancelled, agent.TaskStatusSkipped,
		agent.TaskStatusBlocked, agent.TaskStatusArchived,
	}
	for _, status := range statusOrder {
		count := summary[status]
		symbol := statusSymbol(status)
		m.AppendLog(fmt.Sprintf("  %s %s: %d", symbol, status, count))
	}
	m.AppendLog(fmt.Sprintf("Total: %d", len(registry.Tasks)))
	return nil
}

func boardList(m *tui.AppModel, args string) tea.Cmd {
	fs := flag.NewFlagSet("board list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var statusFlag, assigneeFlag, tagsFlag, parentFlag string
	fs.StringVar(&statusFlag, "status", "", "filter by status (comma-separated)")
	fs.StringVar(&assigneeFlag, "assignee", "", "filter by assignee")
	fs.StringVar(&tagsFlag, "tags", "", "filter by tags (comma-separated)")
	fs.StringVar(&parentFlag, "parent", "", "filter by parent task ID")

	if err := fs.Parse(strings.Fields(args)); err != nil {
		m.AppendLog(fmt.Sprintf("invalid /board list arguments: %v", err))
		return nil
	}

	var statuses []agent.TaskStatus
	if statusFlag != "" {
		for _, s := range strings.Split(statusFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				statuses = append(statuses, agent.TaskStatus(s))
			}
		}
	}
	var tagList []string
	if tagsFlag != "" {
		for _, t := range strings.Split(tagsFlag, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tagList = append(tagList, t)
			}
		}
	}

	input := agent.ListTasksInput{
		Status:   statuses,
		Assignee: assigneeFlag,
		Tags:     tagList,
		ParentID: parentFlag,
	}
	tasks, err := agent.ListTasks(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to list tasks: %v", err))
		return nil
	}
	if len(tasks) == 0 {
		m.AppendLog("No tasks found.")
		return nil
	}
	m.AppendLog(fmt.Sprintf("Tasks (%d):", len(tasks)))
	for _, task := range tasks {
		psym := prioritySymbol(task.Priority)
		ssym := statusSymbol(task.Status)
		depsStr := ""
		if len(task.Dependencies) > 0 {
			depsStr = fmt.Sprintf(" [deps: %s]", strings.Join(task.Dependencies, ","))
		}
		assigneeStr := ""
		if task.Assignee != "" {
			assigneeStr = fmt.Sprintf(" (assignee: %s)", task.Assignee)
		}
		m.AppendLog(fmt.Sprintf("  %s %s %s%s%s", ssym, psym, task.Title, depsStr, assigneeStr))
		m.AppendLog(fmt.Sprintf("    ID: %s | Status: %s | Priority: %s | Created: %s",
			task.ID, task.Status, task.Priority, task.CreatedAt.Format("2006-01-02 15:04:05")))
	}
	return nil
}

func boardNew(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		m.AppendLog("Usage: /board new <title> [--priority <level>] [--assignee <id>] [--description <text>] [--tags <tags>]")
		return nil
	}

	var (
		description string
		priority    string
		assignee    string
		tags        string
		titleParts  []string
	)

	for i := 0; i < len(tokens); i++ {
		arg := tokens[i]
		next := func() (string, bool) {
			if i+1 >= len(tokens) {
				return "", false
			}
			i++
			return tokens[i], true
		}
		switch {
		case arg == "--description":
			v, ok := next()
			if !ok {
				m.AppendLog("flag needs an argument: --description")
				return nil
			}
			description = v
		case strings.HasPrefix(arg, "--description="):
			description = strings.TrimPrefix(arg, "--description=")
		case arg == "--priority":
			v, ok := next()
			if !ok {
				m.AppendLog("flag needs an argument: --priority")
				return nil
			}
			priority = v
		case strings.HasPrefix(arg, "--priority="):
			priority = strings.TrimPrefix(arg, "--priority=")
		case arg == "--assignee":
			v, ok := next()
			if !ok {
				m.AppendLog("flag needs an argument: --assignee")
				return nil
			}
			assignee = v
		case strings.HasPrefix(arg, "--assignee="):
			assignee = strings.TrimPrefix(arg, "--assignee=")
		case arg == "--tags":
			v, ok := next()
			if !ok {
				m.AppendLog("flag needs an argument: --tags")
				return nil
			}
			tags = v
		case strings.HasPrefix(arg, "--tags="):
			tags = strings.TrimPrefix(arg, "--tags=")
		case strings.HasPrefix(arg, "-"):
			m.AppendLog(fmt.Sprintf("unknown flag %q", arg))
			return nil
		default:
			titleParts = append(titleParts, arg)
		}
	}

	title := strings.Join(titleParts, " ")
	if title == "" {
		m.AppendLog("title is required")
		return nil
	}

	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priority != "" && !validPriorities[priority] {
		m.AppendLog(fmt.Sprintf("invalid priority %q (valid: critical, high, medium, low)", priority))
		return nil
	}
	if priority == "" {
		priority = "medium"
	}

	var tagList []string
	if tags != "" {
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tagList = append(tagList, t)
			}
		}
	}

	input := agent.CreateTaskInput{
		Title:       title,
		Description: description,
		Priority:    agent.TaskPriority(priority),
		Assignee:    assignee,
		Tags:        tagList,
	}
	task, err := agent.CreateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to create task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Created task %s: %s", task.ID, task.Title))
	m.RefreshTaskBoard()
	return nil
}

func boardGet(m *tui.AppModel, args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.AppendLog("Usage: /board get <id>")
		return nil
	}
	id := fields[0]
	task, err := agent.GetTask(id)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to get task: %v", err))
		return nil
	}
	if task == nil {
		m.AppendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	m.AppendLog(fmt.Sprintf("ID: %s", task.ID))
	m.AppendLog(fmt.Sprintf("Title: %s", task.Title))
	if task.Description != "" {
		m.AppendLog(fmt.Sprintf("Description: %s", task.Description))
	}
	m.AppendLog(fmt.Sprintf("Status: %s", task.Status))
	m.AppendLog(fmt.Sprintf("Priority: %s", task.Priority))
	if task.Assignee != "" {
		m.AppendLog(fmt.Sprintf("Assignee: %s", task.Assignee))
	}
	if len(task.Dependencies) > 0 {
		m.AppendLog(fmt.Sprintf("Dependencies: %s", strings.Join(task.Dependencies, ", ")))
	}
	m.AppendLog(fmt.Sprintf("CreatedAt: %s", task.CreatedAt.Format(time.RFC3339)))
	return nil
}

func boardUpdate(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.AppendLog("Usage: /board update <id> [--status <s>] [--priority <p>] [--assignee <id>] [--title <t>] [--description <t>] [--error <t>]")
		return nil
	}
	id := tokens[0]

	var statusFlag, priorityFlag, assigneeFlag, titleFlag, descriptionFlag, resultFlag, errorFlag string
	fs := flag.NewFlagSet("board update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&statusFlag, "status", "", "new status")
	fs.StringVar(&priorityFlag, "priority", "", "new priority")
	fs.StringVar(&assigneeFlag, "assignee", "", "new assignee")
	fs.StringVar(&titleFlag, "title", "", "new title")
	fs.StringVar(&descriptionFlag, "description", "", "new description")
	fs.StringVar(&resultFlag, "result", "", "result as JSON string")
	fs.StringVar(&errorFlag, "error", "", "error message")

	if err := fs.Parse(tokens[1:]); err != nil {
		m.AppendLog(fmt.Sprintf("invalid /board update arguments: %v", err))
		return nil
	}

	validStatuses := map[string]bool{
		"pending": true, "in_progress": true, "completed": true,
		"failed": true, "cancelled": true, "skipped": true,
		"blocked": true, "archived": true,
	}
	if statusFlag != "" && !validStatuses[statusFlag] {
		m.AppendLog(fmt.Sprintf("invalid status %q", statusFlag))
		return nil
	}
	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priorityFlag != "" && !validPriorities[priorityFlag] {
		m.AppendLog(fmt.Sprintf("invalid priority %q", priorityFlag))
		return nil
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			m.AppendLog(fmt.Sprintf("invalid JSON for --result: %v", err))
			return nil
		}
	}

	input := agent.UpdateTaskInput{ID: id}
	if statusFlag != "" {
		input.Status = agent.TaskStatus(statusFlag)
	}
	if priorityFlag != "" {
		input.Priority = agent.TaskPriority(priorityFlag)
	}
	if assigneeFlag != "" {
		input.Assignee = assigneeFlag
	}
	if titleFlag != "" {
		input.Title = titleFlag
	}
	if descriptionFlag != "" {
		input.Description = descriptionFlag
	}
	if result != nil {
		input.Result = result
	}
	if errorFlag != "" {
		input.Error = errorFlag
	}

	task, err := agent.UpdateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to update task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Updated task %s: %s (status: %s)", task.ID, task.Title, task.Status))
	m.RefreshTaskBoard()
	return nil
}

func boardDone(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.AppendLog("Usage: /board done <id> [--result <json>]")
		return nil
	}
	id := tokens[0]

	var resultFlag string
	fs := flag.NewFlagSet("board done", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&resultFlag, "result", "", "result as JSON string")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.AppendLog(fmt.Sprintf("invalid /board done arguments: %v", err))
		return nil
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			m.AppendLog(fmt.Sprintf("invalid JSON for --result: %v", err))
			return nil
		}
	}

	input := agent.UpdateTaskInput{ID: id, Status: agent.TaskStatusCompleted}
	if result != nil {
		input.Result = result
	}
	task, err := agent.UpdateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to complete task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Completed task %s: %s", task.ID, task.Title))
	m.RefreshTaskBoard()
	return nil
}

func boardFail(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.AppendLog("Usage: /board fail <id> [--error <text>]")
		return nil
	}
	id := tokens[0]

	var errorFlag string
	fs := flag.NewFlagSet("board fail", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&errorFlag, "error", "task failed", "error message")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.AppendLog(fmt.Sprintf("invalid /board fail arguments: %v", err))
		return nil
	}

	input := agent.UpdateTaskInput{ID: id, Status: agent.TaskStatusFailed, Error: errorFlag}
	task, err := agent.UpdateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to fail task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Failed task %s: %s", task.ID, task.Title))
	m.RefreshTaskBoard()
	return nil
}

func boardCancel(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.AppendLog("Usage: /board cancel <id>")
		return nil
	}
	id := fields[0]
	input := agent.UpdateTaskInput{ID: id, Status: agent.TaskStatusCancelled}
	task, err := agent.UpdateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to cancel task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Cancelled task %s: %s", task.ID, task.Title))
	m.RefreshTaskBoard()
	return nil
}

func boardDelete(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.AppendLog("Usage: /board delete <id>")
		return nil
	}
	id := fields[0]
	success, err := agent.DeleteTask(id)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to delete task: %v", err))
		return nil
	}
	if !success {
		m.AppendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Deleted task %s", id))
	m.RefreshTaskBoard()
	return nil
}

func boardArchive(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.AppendLog("Usage: /board archive <id>")
		return nil
	}
	id := fields[0]
	task, err := agent.ArchiveTask(id)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to archive task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Archived task %s: %s", task.ID, task.Title))
	m.RefreshTaskBoard()
	return nil
}

func boardClaim(m *tui.AppModel, args string) tea.Cmd {
	if m.IsProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.AppendLog("Usage: /board claim <id> [--assignee <id>]")
		return nil
	}
	id := tokens[0]

	var assigneeFlag string
	fs := flag.NewFlagSet("board claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&assigneeFlag, "assignee", "agent", "agent/user ID to assign")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.AppendLog(fmt.Sprintf("invalid /board claim arguments: %v", err))
		return nil
	}

	task, err := agent.GetTask(id)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to get task: %v", err))
		return nil
	}
	if task == nil {
		m.AppendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	if task.Status != agent.TaskStatusPending && task.Status != agent.TaskStatusBlocked {
		m.AppendLog(fmt.Sprintf("task %s cannot be claimed (status: %s, must be pending or blocked)", id, task.Status))
		return nil
	}

	input := agent.UpdateTaskInput{ID: id, Status: agent.TaskStatusInProgress, Assignee: assigneeFlag}
	updatedTask, err := agent.UpdateTask(input)
	if err != nil {
		m.AppendLog(fmt.Sprintf("failed to claim task: %v", err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Claimed task %s by %s", updatedTask.ID, assigneeFlag))
	m.RefreshTaskBoard()
	return nil
}

// =========================================================================
// /sidekick command (SK-008)
// =========================================================================

// runSidekickCommand handles the /sidekick TUI command: ask the configured
// sidekick model a direct question and surface the answer as its own chat
// message (quiet inline chip, no notification dispatch). Both the question
// and the answer are recorded into the active session under kind "sidekick"
// so sessions/<id>.json carries the provenance of sidekick interactions.
func runSidekickCommand(m *tui.AppModel, args string, rt *agent.Runtime) tea.Cmd {
	question := strings.TrimSpace(args)
	if question == "" {
		m.AppendLog("Usage: /sidekick <question> — ask the sidekick model a direct question")
		return nil
	}
	sk := rt.Sidekick()
	if sk == nil || !sk.Enabled() {
		m.AppendLog("⚠ sidekick is not enabled (set sidekick.enabled in config.json)")
		return nil
	}

	// Best-effort audit trail against the open session (nil-safe).
	var skStore sidekick.TranscriptStore
	var sid string
	if svc := m.SessionService(); svc != nil {
		skStore = svc.Store()
		sid = svc.ActiveSessionID()
	}

	// Frame the question with recent session history (same budget as the
	// watchdog), BEFORE recording this turn so it is not duplicated.
	prompt := question
	if skStore != nil && sid != "" {
		if sess, err := skStore.Load(sid); err == nil && sess != nil {
			prompt = sidekick.BuildAskPrompt(
				sidekick.RecentTranscript(sess, sk.TranscriptWindow()),
				question,
			)
		}
	}
	sidekick.RecordQuestion(skStore, sid, question)

	// Run the sidekick Ask asynchronously so the TUI stays responsive.
	return func() tea.Msg {
		answer, err := sk.Ask(context.Background(), prompt)
		if err != nil {
			sidekick.RecordAnswer(skStore, sid, "[sidekick error] "+err.Error())
			return tui.SidekickAnswerMsg{SessionID: sid, Err: err, Question: question}
		}
		sidekick.RecordAnswer(skStore, sid, answer)
		return tui.SidekickAnswerMsg{SessionID: sid, Answer: answer, Question: question}
	}
}

func prioritySymbol(p agent.TaskPriority) string {
	switch p {
	case agent.TaskPriorityCritical:
		return "!"
	case agent.TaskPriorityHigh:
		return "H"
	case agent.TaskPriorityMedium:
		return "M"
	case agent.TaskPriorityLow:
		return "L"
	default:
		return "?"
	}
}

func statusSymbol(s agent.TaskStatus) string {
	switch s {
	case agent.TaskStatusPending:
		return "[ ]"
	case agent.TaskStatusInProgress:
		return "[>]"
	case agent.TaskStatusCompleted:
		return "[x]"
	case agent.TaskStatusFailed:
		return "[!]"
	case agent.TaskStatusCancelled:
		return "[-]"
	case agent.TaskStatusSkipped:
		return "[~]"
	case agent.TaskStatusBlocked:
		return "[B]"
	case agent.TaskStatusArchived:
		return "[A]"
	default:
		return "[?]"
	}
}
