// task.go - the `hakase task` CLI: create, list, get, update, delete, claim, complete, fail, cancel, summary.
// Migrated from root task_cli.go (plan task 10).
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"amurru/hakase/internal/agent"
)

// TaskDescriptionPlaceholder is the default description for newly created tasks.
const TaskDescriptionPlaceholder = "TODO: Describe what this task does and when to use it."

// RunTaskCLI is the entry point for the `hakase task` subcommand.
func RunTaskCLI(args []string) int {
	if len(args) == 0 {
		taskCLIUsage()
		return 2
	}
	switch args[0] {
	case "create":
		return runTaskCreate(args[1:])
	case "list":
		return runTaskList(args[1:])
	case "get":
		return runTaskGet(args[1:])
	case "update":
		return runTaskUpdate(args[1:])
	case "delete":
		return runTaskDelete(args[1:])
	case "archive":
		return runTaskArchive(args[1:])
	case "claim":
		return runTaskClaim(args[1:])
	case "complete":
		return runTaskComplete(args[1:])
	case "fail":
		return runTaskFail(args[1:])
	case "cancel":
		return runTaskCancel(args[1:])
	case "summary":
		return runTaskSummary(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown task subcommand %q\n\n", args[0])
		taskCLIUsage()
		return 2
	}
}

func taskCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase task <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  create     create a new task")
	fmt.Fprintln(os.Stderr, "  list       list tasks with optional filters")
	fmt.Fprintln(os.Stderr, "  get        get task details by ID")
	fmt.Fprintln(os.Stderr, "  update     update task fields")
	fmt.Fprintln(os.Stderr, "  delete     delete a task")
	fmt.Fprintln(os.Stderr, "  archive    archive a completed task")
	fmt.Fprintln(os.Stderr, "  claim      claim a task for execution")
	fmt.Fprintln(os.Stderr, "  complete   mark a task as completed")
	fmt.Fprintln(os.Stderr, "  fail       mark a task as failed")
	fmt.Fprintln(os.Stderr, "  cancel     cancel a task")
	fmt.Fprintln(os.Stderr, "  summary    show task board summary")
}

func runTaskCreate(args []string) int {
	var (
		description string
		priority    string
		deps        string
		assignee    string
		parent      string
		tags        string
		title       string
	)

	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage: hakase task create <title> [--description <text>] [--priority <level>] [--deps <ids>] [--assignee <id>] [--parent <id>] [--tags <tags>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  --description <text>  task description")
		fmt.Fprintln(os.Stderr, "  --priority <level>    priority: critical, high, medium, low (default: medium)")
		fmt.Fprintln(os.Stderr, "  --deps <ids>          comma-separated task IDs that must complete first")
		fmt.Fprintln(os.Stderr, "  --assignee <id>       agent/user ID to assign")
		fmt.Fprintln(os.Stderr, "  --parent <id>         parent task ID for hierarchy")
		fmt.Fprintln(os.Stderr, "  --tags <tags>         comma-separated tags")
		fmt.Fprintln(os.Stderr, "  -h, --help            show this help")
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case arg == "-h" || arg == "--help":
			usage()
			return 0
		case arg == "--description":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --description")
				usage()
				return 2
			}
			description = v
		case strings.HasPrefix(arg, "--description="):
			description = strings.TrimPrefix(arg, "--description=")
		case arg == "--priority":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --priority")
				usage()
				return 2
			}
			priority = v
		case strings.HasPrefix(arg, "--priority="):
			priority = strings.TrimPrefix(arg, "--priority=")
		case arg == "--deps":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --deps")
				usage()
				return 2
			}
			deps = v
		case strings.HasPrefix(arg, "--deps="):
			deps = strings.TrimPrefix(arg, "--deps=")
		case arg == "--assignee":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --assignee")
				usage()
				return 2
			}
			assignee = v
		case strings.HasPrefix(arg, "--assignee="):
			assignee = strings.TrimPrefix(arg, "--assignee=")
		case arg == "--parent":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --parent")
				usage()
				return 2
			}
			parent = v
		case strings.HasPrefix(arg, "--parent="):
			parent = strings.TrimPrefix(arg, "--parent=")
		case arg == "--tags":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --tags")
				usage()
				return 2
			}
			tags = v
		case strings.HasPrefix(arg, "--tags="):
			tags = strings.TrimPrefix(arg, "--tags=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			usage()
			return 2
		default:
			if title != "" {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				usage()
				return 2
			}
			title = arg
		}
	}

	if title == "" {
		fmt.Fprintln(os.Stderr, "title is required")
		usage()
		return 2
	}

	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priority != "" && !validPriorities[priority] {
		fmt.Fprintf(os.Stderr, "invalid priority %q (valid: critical, high, medium, low)\n", priority)
		return 2
	}
	if priority == "" {
		priority = "medium"
	}

	var depIDs []string
	if deps != "" {
		depIDs = strings.Split(deps, ",")
		for i := range depIDs {
			depIDs[i] = strings.TrimSpace(depIDs[i])
		}
	}

	var tagList []string
	if tags != "" {
		tagList = strings.Split(tags, ",")
		for i := range tagList {
			tagList[i] = strings.TrimSpace(tagList[i])
		}
	}

	input := agent.CreateTaskInput{
		Title:        title,
		Description:  description,
		Priority:     agent.TaskPriority(priority),
		Dependencies: depIDs,
		Assignee:     assignee,
		ParentID:     parent,
		Tags:         tagList,
	}

	task, err := agent.CreateTask(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create task: %v\n", err)
		return 1
	}

	fmt.Printf("Created task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var statusFlag string
	var assigneeFlag string
	var tagsFlag string
	var parentFlag string

	fs.StringVar(&statusFlag, "status", "", "filter by status (comma-separated: pending,in_progress,completed,failed,cancelled,skipped,blocked,archived)")
	fs.StringVar(&assigneeFlag, "assignee", "", "filter by assignee")
	fs.StringVar(&tagsFlag, "tags", "", "filter by tags (comma-separated)")
	fs.StringVar(&parentFlag, "parent", "", "filter by parent task ID")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
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
		tagList = strings.Split(tagsFlag, ",")
		for i := range tagList {
			tagList[i] = strings.TrimSpace(tagList[i])
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
		fmt.Fprintf(os.Stderr, "failed to list tasks: %v\n", err)
		return 1
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return 0
	}

	fmt.Printf("Tasks (%d):\n", len(tasks))
	for _, task := range tasks {
		psym := PrioritySymbol(task.Priority)
		ssym := StatusSymbol(task.Status)
		depsStr := ""
		if len(task.Dependencies) > 0 {
			depsStr = fmt.Sprintf(" [deps: %s]", strings.Join(task.Dependencies, ","))
		}
		assigneeStr := ""
		if task.Assignee != "" {
			assigneeStr = fmt.Sprintf(" (assignee: %s)", task.Assignee)
		}
		fmt.Printf("  %s %s %s%s%s\n", ssym, psym, task.Title, depsStr, assigneeStr)
		fmt.Printf("    ID: %s | Status: %s | Priority: %s | Created: %s\n",
			task.ID, task.Status, task.Priority, task.CreatedAt.Format("2006-01-02 15:04:05"))
		if task.Description != "" {
			fmt.Printf("    Description: %s\n", task.Description)
		}
		if len(task.Tags) > 0 {
			fmt.Printf("    Tags: %s\n", strings.Join(task.Tags, ", "))
		}
		fmt.Println()
	}

	return 0
}

func runTaskGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task get <id>")
		return 2
	}

	task, err := agent.GetTask(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get task: %v\n", err)
		return 1
	}

	if task == nil {
		fmt.Fprintf(os.Stderr, "task not found: %s\n", args[0])
		return 1
	}

	printTaskDetail(task)
	return 0
}

func runTaskUpdate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task update <id> [--status <status>] [--priority <level>] [--assignee <id>] [--title <text>] [--description <text>] [--result <json>] [--error <text>]")
		return 2
	}

	id := args[0]

	var statusFlag string
	var priorityFlag string
	var assigneeFlag string
	var titleFlag string
	var descriptionFlag string
	var resultFlag string
	var errorFlag string

	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&statusFlag, "status", "", "new status")
	fs.StringVar(&priorityFlag, "priority", "", "new priority")
	fs.StringVar(&assigneeFlag, "assignee", "", "new assignee")
	fs.StringVar(&titleFlag, "title", "", "new title")
	fs.StringVar(&descriptionFlag, "description", "", "new description")
	fs.StringVar(&resultFlag, "result", "", "result as JSON string")
	fs.StringVar(&errorFlag, "error", "", "error message")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	validStatuses := map[string]bool{"pending": true, "in_progress": true, "completed": true, "failed": true, "cancelled": true, "skipped": true, "blocked": true, "archived": true}
	if statusFlag != "" && !validStatuses[statusFlag] {
		fmt.Fprintf(os.Stderr, "invalid status %q\n", statusFlag)
		return 2
	}

	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if priorityFlag != "" && !validPriorities[priorityFlag] {
		fmt.Fprintf(os.Stderr, "invalid priority %q\n", priorityFlag)
		return 2
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			fmt.Fprintf(os.Stderr, "invalid JSON for --result: %v\n", err)
			return 1
		}
	}

	input := agent.UpdateTaskInput{
		ID: id,
	}
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
		fmt.Fprintf(os.Stderr, "failed to update task: %v\n", err)
		return 1
	}

	fmt.Printf("Updated task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskDelete(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task delete <id>")
		return 2
	}

	success, err := agent.DeleteTask(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to delete task: %v\n", err)
		return 1
	}

	if !success {
		fmt.Fprintf(os.Stderr, "task not found: %s\n", args[0])
		return 1
	}

	fmt.Printf("Deleted task %s\n", args[0])
	return 0
}

func runTaskArchive(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task archive <id>")
		return 2
	}

	task, err := agent.ArchiveTask(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to archive task: %v\n", err)
		return 1
	}

	fmt.Printf("Archived task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskClaim(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task claim <id> [--assignee <id>]")
		return 2
	}

	id := args[0]

	var assigneeFlag string
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&assigneeFlag, "assignee", "agent", "agent/user ID to assign")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	task, err := agent.GetTask(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get task: %v\n", err)
		return 1
	}
	if task == nil {
		fmt.Fprintf(os.Stderr, "task not found: %s\n", id)
		return 1
	}

	if task.Status != agent.TaskStatusPending && task.Status != agent.TaskStatusBlocked {
		fmt.Fprintf(os.Stderr, "task %s cannot be claimed (status: %s, must be pending or blocked)\n", id, task.Status)
		return 1
	}

	input := agent.UpdateTaskInput{
		ID:       id,
		Status:   agent.TaskStatusInProgress,
		Assignee: assigneeFlag,
	}

	updatedTask, err := agent.UpdateTask(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to claim task: %v\n", err)
		return 1
	}

	fmt.Printf("Claimed task %s by %s\n", updatedTask.ID, assigneeFlag)
	return 0
}

func runTaskComplete(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task complete <id> [--result <json>]")
		return 2
	}

	id := args[0]

	var resultFlag string
	fs := flag.NewFlagSet("complete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&resultFlag, "result", "", "result as JSON string")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	var result interface{}
	if resultFlag != "" {
		if err := json.Unmarshal([]byte(resultFlag), &result); err != nil {
			fmt.Fprintf(os.Stderr, "invalid JSON for --result: %v\n", err)
			return 1
		}
	}

	input := agent.UpdateTaskInput{
		ID:     id,
		Status: agent.TaskStatusCompleted,
	}
	if result != nil {
		input.Result = result
	}

	task, err := agent.UpdateTask(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to complete task: %v\n", err)
		return 1
	}

	fmt.Printf("Completed task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskFail(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task fail <id> [--error <text>]")
		return 2
	}

	id := args[0]

	var errorFlag string
	fs := flag.NewFlagSet("fail", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&errorFlag, "error", "task failed", "error message")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	input := agent.UpdateTaskInput{
		ID:    id,
		Status: agent.TaskStatusFailed,
		Error: errorFlag,
	}

	task, err := agent.UpdateTask(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fail task: %v\n", err)
		return 1
	}

	fmt.Printf("Failed task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskCancel(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase task cancel <id>")
		return 2
	}

	input := agent.UpdateTaskInput{
		ID:     args[0],
		Status: agent.TaskStatusCancelled,
	}

	task, err := agent.UpdateTask(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to cancel task: %v\n", err)
		return 1
	}

	fmt.Printf("Cancelled task %s: %s\n", task.ID, task.Title)
	return 0
}

func runTaskSummary(args []string) int {
	registry, err := agent.LoadTaskRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load tasks: %v\n", err)
		return 1
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

	fmt.Println("Task Board Summary")
	fmt.Println("==================")
	statusOrder := []agent.TaskStatus{agent.TaskStatusPending, agent.TaskStatusInProgress, agent.TaskStatusCompleted, agent.TaskStatusFailed, agent.TaskStatusCancelled, agent.TaskStatusSkipped, agent.TaskStatusBlocked, agent.TaskStatusArchived}
	for _, status := range statusOrder {
		count := summary[status]
		symbol := StatusSymbol(status)
		fmt.Printf("  %s %s: %d\n", symbol, status, count)
	}
	fmt.Printf("\nTotal: %d\n", len(registry.Tasks))

	return 0
}

func printTaskDetail(task *agent.TaskMeta) {
	fmt.Printf("ID:          %s\n", task.ID)
	fmt.Printf("Title:       %s\n", task.Title)
	fmt.Printf("Description: %s\n", task.Description)
	fmt.Printf("Status:      %s\n", task.Status)
	fmt.Printf("Priority:    %s\n", task.Priority)
	fmt.Printf("Owner:       %s\n", task.Owner)
	fmt.Printf("Assignee:    %s\n", task.Assignee)
	fmt.Printf("Dependencies: %s\n", strings.Join(task.Dependencies, ", "))
	fmt.Printf("BlockedBy:    %s\n", strings.Join(task.BlockedBy, ", "))
	fmt.Printf("CreatedAt:    %s\n", task.CreatedAt.Format(time.RFC3339))
	fmt.Printf("UpdatedAt:    %s\n", task.UpdatedAt.Format(time.RFC3339))
	if task.StartedAt != nil {
		fmt.Printf("StartedAt:    %s\n", task.StartedAt.Format(time.RFC3339))
	}
	if task.CompletedAt != nil {
		fmt.Printf("CompletedAt:  %s\n", task.CompletedAt.Format(time.RFC3339))
	}
	if task.DueAt != nil {
		fmt.Printf("DueAt:        %s\n", task.DueAt.Format(time.RFC3339))
	}
	fmt.Printf("Attempts:     %d/%d\n", task.Attempts, task.MaxAttempts)
	if task.LastError != "" {
		fmt.Printf("LastError:    %s\n", task.LastError)
	}
	if task.Result != nil {
		resultJSON, _ := json.MarshalIndent(task.Result, "", "  ")
		fmt.Printf("Result:       %s\n", string(resultJSON))
	}
	if len(task.Metadata) > 0 {
		metaJSON, _ := json.MarshalIndent(task.Metadata, "", "  ")
		fmt.Printf("Metadata:     %s\n", string(metaJSON))
	}
	if task.ParentID != "" {
		fmt.Printf("ParentID:     %s\n", task.ParentID)
	}
	if len(task.Tags) > 0 {
		fmt.Printf("Tags:         %s\n", strings.Join(task.Tags, ", "))
	}
}

// PrioritySymbol returns an emoji symbol for the given task priority.
func PrioritySymbol(p agent.TaskPriority) string {
	switch p {
	case agent.TaskPriorityCritical:
		return "🔴"
	case agent.TaskPriorityHigh:
		return "🟠"
	case agent.TaskPriorityMedium:
		return "🟡"
	case agent.TaskPriorityLow:
		return "🟢"
	default:
		return "⚪"
	}
}

// StatusSymbol returns an emoji symbol for the given task status.
func StatusSymbol(s agent.TaskStatus) string {
	switch s {
	case agent.TaskStatusPending:
		return "⏳"
	case agent.TaskStatusInProgress:
		return "▶️"
	case agent.TaskStatusCompleted:
		return "✅"
	case agent.TaskStatusFailed:
		return "❌"
	case agent.TaskStatusCancelled:
		return "🚫"
	case agent.TaskStatusSkipped:
		return "⏭️"
	case agent.TaskStatusBlocked:
		return "🔒"
	case agent.TaskStatusArchived:
		return "🗄️"
	default:
		return "❓"
	}
}
