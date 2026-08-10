package agent

import (
	"time"
)

// Task management types - extracted from agent.go so both the TUI task pane
// and web task board can import them without pulling in the full agent package.

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
	TaskStatusSkipped    TaskStatus = "skipped"
	TaskStatusBlocked    TaskStatus = "blocked"
	TaskStatusArchived   TaskStatus = "archived"
)

var ValidTransitions = map[TaskStatus][]TaskStatus{
	TaskStatusPending:    {TaskStatusInProgress, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled, TaskStatusSkipped},
	TaskStatusInProgress: {TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled, TaskStatusBlocked},
	TaskStatusBlocked:    {TaskStatusInProgress, TaskStatusCancelled},
	TaskStatusCompleted:  {TaskStatusArchived},
	TaskStatusFailed:     {},
	TaskStatusCancelled:  {},
	TaskStatusSkipped:    {},
	TaskStatusArchived:   {},
}

type TaskPriority string

const (
	TaskPriorityCritical TaskPriority = "critical"
	TaskPriorityHigh     TaskPriority = "high"
	TaskPriorityMedium   TaskPriority = "medium"
	TaskPriorityLow      TaskPriority = "low"
)

type TaskMeta struct {
	ID           string         `json:"id"`
	Version      int            `json:"version"`
	Title        string         `json:"title"`
	Description  string         `json:"description,omitempty"`
	Status       TaskStatus     `json:"status"`
	Priority     TaskPriority   `json:"priority"`
	Owner        string         `json:"owner,omitempty"`
	Assignee     string         `json:"assignee,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	BlockedBy    []string       `json:"blocked_by,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	StartedAt    *time.Time     `json:"started_at,omitempty"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
	DueAt        *time.Time     `json:"due_at,omitempty"`
	Attempts     int            `json:"attempts"`
	MaxAttempts  int            `json:"max_attempts"`
	LastError    string         `json:"last_error,omitempty"`
	Result       any            `json:"result,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	ParentID     string         `json:"parent_id,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
}

type TaskRegistry struct {
	Tasks []TaskMeta `json:"tasks"`
}

// Task tool input/output types

type CreateTaskInput struct {
	Title        string       `json:"title" doc:"Task title"`
	Description  string       `json:"description,omitempty" doc:"Task description"`
	Priority     TaskPriority `json:"priority,omitempty" doc:"Task priority"`
	Dependencies []string     `json:"dependencies,omitempty" doc:"Task IDs that must complete first"`
	Assignee     string       `json:"assignee,omitempty" doc:"Agent to assign"`
	ParentID     string       `json:"parent_id,omitempty" doc:"Parent task ID for hierarchy"`
	Tags         []string     `json:"tags,omitempty" doc:"Task tags"`
}

type CreateTaskOutput struct {
	Task TaskMeta `json:"task" doc:"Created task"`
}

type UpdateTaskInput struct {
	ID          string       `json:"id" doc:"Task ID"`
	Title       string       `json:"title,omitempty" doc:"New title"`
	Description string       `json:"description,omitempty" doc:"New description"`
	Status      TaskStatus   `json:"status,omitempty" doc:"New status (transition validated)"`
	Priority    TaskPriority `json:"priority,omitempty" doc:"New priority"`
	Assignee    string       `json:"assignee,omitempty" doc:"New assignee"`
	Result      any          `json:"result,omitempty" doc:"Execution result"`
	Error       string       `json:"error,omitempty" doc:"Error message if failed"`
}

type UpdateTaskOutput struct {
	Task TaskMeta `json:"task" doc:"Updated task"`
}

type ListTasksInput struct {
	Status   []TaskStatus `json:"status,omitempty" doc:"Filter by status"`
	Assignee string       `json:"assignee,omitempty" doc:"Filter by assignee"`
	Tags     []string     `json:"tags,omitempty" doc:"Filter by tags"`
	ParentID string       `json:"parent_id,omitempty" doc:"Filter by parent"`
}

type ListTasksOutput struct {
	Tasks []TaskMeta `json:"tasks" doc:"Matching tasks"`
}

type GetTaskInput struct {
	ID string `json:"id" doc:"Task ID"`
}

type GetTaskOutput struct {
	Task *TaskMeta `json:"task" doc:"Task or null if not found"`
}

type DeleteTaskInput struct {
	ID string `json:"id" doc:"Task ID"`
}

type DeleteTaskOutput struct {
	Success bool `json:"success" doc:"Whether deletion succeeded"`
}

type ArchiveTaskInput struct {
	ID string `json:"id" doc:"Task ID"`
}

type ArchiveTaskOutput struct {
	Task TaskMeta `json:"task" doc:"Archived task"`
}
