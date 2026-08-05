// task_slash.go - the /board slash command: a TUI-facing mirror of the
// `hakase task` CLI (task_cli.go). All output goes to the log pane via
// m.appendLog (never stdout/stderr), and mutations refresh the task pane
// via m.refreshTaskBoard(). Mutating subcommands are blocked while the
// agent is processing (m.isProcessing), matching the convention used by
// /compact, /new, and /sessions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runBoardCommand dispatches a /board <subcommand> [args] line to the
// matching handler. It always returns nil (synchronous handlers); it
// never returns tea.Quit.
func runBoardCommand(m *appModel, args string) tea.Cmd {
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
		m.appendLog(fmt.Sprintf("unknown /board subcommand %q (try: summary, list, new, get, update, done, fail, cancel, delete, archive, claim)", sub))
		return nil
	}
}

// boardBlockWarn logs the standard "agent is working" warning and returns nil
// for mutating subcommands invoked while m.isProcessing is true.
func boardBlockWarn(m *appModel) tea.Cmd {
	m.appendLog("⚠ cannot modify the task board while the agent is working")
	return nil
}

// boardSummary mirrors runTaskSummary: per-status counts using statusSymbol,
// in the same statusOrder, then a total line.
func boardSummary(m *appModel) tea.Cmd {
	registry, err := loadTaskRegistry()
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to load tasks: %v", err))
		return nil
	}
	summary := map[TaskStatus]int{
		TaskStatusPending:    0,
		TaskStatusInProgress: 0,
		TaskStatusCompleted:  0,
		TaskStatusFailed:     0,
		TaskStatusCancelled:  0,
		TaskStatusSkipped:    0,
		TaskStatusBlocked:    0,
		TaskStatusArchived:   0,
	}
	for _, task := range registry.Tasks {
		summary[task.Status]++
	}
	m.appendLog("📋 Task Board")
	statusOrder := []TaskStatus{
		TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted,
		TaskStatusFailed, TaskStatusCancelled, TaskStatusSkipped,
		TaskStatusBlocked, TaskStatusArchived,
	}
	for _, status := range statusOrder {
		count := summary[status]
		symbol := statusSymbol(status)
		m.appendLog(fmt.Sprintf("  %s %s: %d", symbol, status, count))
	}
	m.appendLog(fmt.Sprintf("Total: %d", len(registry.Tasks)))
	return nil
}

// boardList mirrors runTaskList, with flag.FlagSet parsing like the CLI.
func boardList(m *appModel, args string) tea.Cmd {
	fs := flag.NewFlagSet("board list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var statusFlag, assigneeFlag, tagsFlag, parentFlag string
	fs.StringVar(&statusFlag, "status", "", "filter by status (comma-separated)")
	fs.StringVar(&assigneeFlag, "assignee", "", "filter by assignee")
	fs.StringVar(&tagsFlag, "tags", "", "filter by tags (comma-separated)")
	fs.StringVar(&parentFlag, "parent", "", "filter by parent task ID")

	if err := fs.Parse(strings.Fields(args)); err != nil {
		m.appendLog(fmt.Sprintf("⚠ invalid /board list arguments: %v", err))
		return nil
	}

	var statuses []TaskStatus
	if statusFlag != "" {
		for _, s := range strings.Split(statusFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				statuses = append(statuses, TaskStatus(s))
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

	input := ListTasksInput{
		Status:   statuses,
		Assignee: assigneeFlag,
		Tags:     tagList,
		ParentID: parentFlag,
	}
	tasks, err := listTasks(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to list tasks: %v", err))
		return nil
	}
	if len(tasks) == 0 {
		m.appendLog("No tasks found.")
		return nil
	}
	m.appendLog(fmt.Sprintf("Tasks (%d):", len(tasks)))
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
		m.appendLog(fmt.Sprintf("  %s %s %s%s%s", ssym, psym, task.Title, depsStr, assigneeStr))
		m.appendLog(fmt.Sprintf("    ID: %s | Status: %s | Priority: %s | Created: %s",
			task.ID, task.Status, task.Priority, task.CreatedAt.Format("2006-01-02 15:04:05")))
	}
	return nil
}

// boardNew mirrors runTaskCreate: title is the remaining positional args
// joined by space; flags --priority/--assignee/--description/--tags.
func boardNew(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		m.appendLog("Usage: /board new <title> [--priority <level>] [--assignee <id>] [--description <text>] [--tags <tags>]")
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
				m.appendLog("⚠ flag needs an argument: --description")
				return nil
			}
			description = v
		case strings.HasPrefix(arg, "--description="):
			description = strings.TrimPrefix(arg, "--description=")
		case arg == "--priority":
			v, ok := next()
			if !ok {
				m.appendLog("⚠ flag needs an argument: --priority")
				return nil
			}
			priority = v
		case strings.HasPrefix(arg, "--priority="):
			priority = strings.TrimPrefix(arg, "--priority=")
		case arg == "--assignee":
			v, ok := next()
			if !ok {
				m.appendLog("⚠ flag needs an argument: --assignee")
				return nil
			}
			assignee = v
		case strings.HasPrefix(arg, "--assignee="):
			assignee = strings.TrimPrefix(arg, "--assignee=")
		case arg == "--tags":
			v, ok := next()
			if !ok {
				m.appendLog("⚠ flag needs an argument: --tags")
				return nil
			}
			tags = v
		case strings.HasPrefix(arg, "--tags="):
			tags = strings.TrimPrefix(arg, "--tags=")
		case strings.HasPrefix(arg, "-"):
			m.appendLog(fmt.Sprintf("⚠ unknown flag %q", arg))
			return nil
		default:
			titleParts = append(titleParts, arg)
		}
	}

	title := strings.Join(titleParts, " ")
	if title == "" {
		m.appendLog("⚠ title is required")
		return nil
	}

	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priority != "" && !validPriorities[priority] {
		m.appendLog(fmt.Sprintf("⚠ invalid priority %q (valid: critical, high, medium, low)", priority))
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

	input := CreateTaskInput{
		Title:       title,
		Description: description,
		Priority:    TaskPriority(priority),
		Assignee:    assignee,
		Tags:        tagList,
	}
	task, err := createTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to create task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Created task %s: %s", task.ID, task.Title))
	m.refreshTaskBoard()
	return nil
}

// boardGet mirrors runTaskGet + printTaskDetail, compacted for the TUI log.
func boardGet(m *appModel, args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.appendLog("Usage: /board get <id>")
		return nil
	}
	id := fields[0]
	task, err := getTask(id)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to get task: %v", err))
		return nil
	}
	if task == nil {
		m.appendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	m.appendLog(fmt.Sprintf("ID: %s", task.ID))
	m.appendLog(fmt.Sprintf("Title: %s", task.Title))
	if task.Description != "" {
		m.appendLog(fmt.Sprintf("Description: %s", task.Description))
	}
	m.appendLog(fmt.Sprintf("Status: %s", task.Status))
	m.appendLog(fmt.Sprintf("Priority: %s", task.Priority))
	if task.Assignee != "" {
		m.appendLog(fmt.Sprintf("Assignee: %s", task.Assignee))
	}
	if len(task.Dependencies) > 0 {
		m.appendLog(fmt.Sprintf("Dependencies: %s", strings.Join(task.Dependencies, ", ")))
	}
	m.appendLog(fmt.Sprintf("CreatedAt: %s", task.CreatedAt.Format(time.RFC3339)))
	return nil
}

// boardUpdate mirrors runTaskUpdate.
func boardUpdate(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.appendLog("Usage: /board update <id> [--status <s>] [--priority <p>] [--assignee <id>] [--title <t>] [--description <t>] [--error <t>]")
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
		m.appendLog(fmt.Sprintf("⚠ invalid /board update arguments: %v", err))
		return nil
	}

	validStatuses := map[string]bool{
		"pending": true, "in_progress": true, "completed": true,
		"failed": true, "cancelled": true, "skipped": true,
		"blocked": true, "archived": true,
	}
	if statusFlag != "" && !validStatuses[statusFlag] {
		m.appendLog(fmt.Sprintf("⚠ invalid status %q", statusFlag))
		return nil
	}
	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priorityFlag != "" && !validPriorities[priorityFlag] {
		m.appendLog(fmt.Sprintf("⚠ invalid priority %q", priorityFlag))
		return nil
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			m.appendLog(fmt.Sprintf("⚠ invalid JSON for --result: %v", err))
			return nil
		}
	}

	input := UpdateTaskInput{ID: id}
	if statusFlag != "" {
		input.Status = TaskStatus(statusFlag)
	}
	if priorityFlag != "" {
		input.Priority = TaskPriority(priorityFlag)
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

	task, err := updateTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to update task: %v", err))
		return nil
	}
	statusStr := string(task.Status)
	if statusFlag == "" {
		// status unchanged; report the actual current status
		statusStr = string(task.Status)
	}
	m.appendLog(fmt.Sprintf("Updated task %s: %s (status: %s)", task.ID, task.Title, statusStr))
	m.refreshTaskBoard()
	return nil
}

// boardDone mirrors runTaskComplete.
func boardDone(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.appendLog("Usage: /board done <id> [--result <json>]")
		return nil
	}
	id := tokens[0]

	var resultFlag string
	fs := flag.NewFlagSet("board done", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&resultFlag, "result", "", "result as JSON string")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.appendLog(fmt.Sprintf("⚠ invalid /board done arguments: %v", err))
		return nil
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			m.appendLog(fmt.Sprintf("⚠ invalid JSON for --result: %v", err))
			return nil
		}
	}

	input := UpdateTaskInput{ID: id, Status: TaskStatusCompleted}
	if result != nil {
		input.Result = result
	}
	task, err := updateTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to complete task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Completed task %s: %s", task.ID, task.Title))
	m.refreshTaskBoard()
	return nil
}

// boardFail mirrors runTaskFail.
func boardFail(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.appendLog("Usage: /board fail <id> [--error <text>]")
		return nil
	}
	id := tokens[0]

	var errorFlag string
	fs := flag.NewFlagSet("board fail", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&errorFlag, "error", "task failed", "error message")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.appendLog(fmt.Sprintf("⚠ invalid /board fail arguments: %v", err))
		return nil
	}

	input := UpdateTaskInput{ID: id, Status: TaskStatusFailed, Error: errorFlag}
	task, err := updateTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to fail task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Failed task %s: %s", task.ID, task.Title))
	m.refreshTaskBoard()
	return nil
}

// boardCancel mirrors runTaskCancel.
func boardCancel(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.appendLog("Usage: /board cancel <id>")
		return nil
	}
	id := fields[0]
	input := UpdateTaskInput{ID: id, Status: TaskStatusCancelled}
	task, err := updateTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to cancel task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Cancelled task %s: %s", task.ID, task.Title))
	m.refreshTaskBoard()
	return nil
}

// boardDelete mirrors runTaskDelete.
func boardDelete(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.appendLog("Usage: /board delete <id>")
		return nil
	}
	id := fields[0]
	success, err := deleteTask(id)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to delete task: %v", err))
		return nil
	}
	if !success {
		m.appendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	m.appendLog(fmt.Sprintf("Deleted task %s", id))
	m.refreshTaskBoard()
	return nil
}

// boardArchive mirrors runTaskArchive.
func boardArchive(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.appendLog("Usage: /board archive <id>")
		return nil
	}
	id := fields[0]
	task, err := archiveTask(id)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to archive task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Archived task %s: %s", task.ID, task.Title))
	m.refreshTaskBoard()
	return nil
}

// boardClaim mirrors runTaskClaim.
func boardClaim(m *appModel, args string) tea.Cmd {
	if m.isProcessing {
		return boardBlockWarn(m)
	}
	tokens := strings.Fields(args)
	if len(tokens) < 1 {
		m.appendLog("Usage: /board claim <id> [--assignee <id>]")
		return nil
	}
	id := tokens[0]

	var assigneeFlag string
	fs := flag.NewFlagSet("board claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&assigneeFlag, "assignee", "agent", "agent/user ID to assign")
	if err := fs.Parse(tokens[1:]); err != nil {
		m.appendLog(fmt.Sprintf("⚠ invalid /board claim arguments: %v", err))
		return nil
	}

	task, err := getTask(id)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to get task: %v", err))
		return nil
	}
	if task == nil {
		m.appendLog(fmt.Sprintf("task not found: %s", id))
		return nil
	}
	if task.Status != TaskStatusPending && task.Status != TaskStatusBlocked {
		m.appendLog(fmt.Sprintf("⚠ task %s cannot be claimed (status: %s, must be pending or blocked)", id, task.Status))
		return nil
	}

	input := UpdateTaskInput{ID: id, Status: TaskStatusInProgress, Assignee: assigneeFlag}
	updatedTask, err := updateTask(input)
	if err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to claim task: %v", err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Claimed task %s by %s", updatedTask.ID, assigneeFlag))
	m.refreshTaskBoard()
	return nil
}