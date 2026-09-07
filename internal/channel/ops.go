package channel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/cli"
)

// TasksText renders the task board for a chat message. statusFilter is a
// comma-separated status filter ("", "open", or explicit statuses like
// "in_progress,blocked").
func TasksText(statusFilter string) string {
	input := hakaseagent.ListTasksInput{}
	if f := parseStatusFilter(statusFilter); len(f) > 0 {
		input.Status = f
	}
	tasks, err := hakaseagent.ListTasks(input)
	if err != nil {
		return "📋 " + err.Error()
	}
	if len(tasks) == 0 {
		return "📋 Task board is empty."
	}
	sort.Slice(tasks, func(i, j int) bool {
		pi, pj := taskPriorityRank(tasks[i].Priority), taskPriorityRank(tasks[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📋 Tasks (%d)\n", len(tasks)))
	for _, t := range tasks {
		b.WriteString(fmt.Sprintf("%s %s [%s]", statusEmoji(string(t.Status)), t.Title, t.Status))
		if t.Assignee != "" {
			b.WriteString(" · " + t.Assignee)
		}
		b.WriteString("\n<code>" + t.ID + "</code>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// taskPriorityRank orders priorities (lower sorts first).
func taskPriorityRank(p hakaseagent.TaskPriority) int {
	switch p {
	case hakaseagent.TaskPriorityCritical:
		return 0
	case hakaseagent.TaskPriorityHigh:
		return 1
	case hakaseagent.TaskPriorityMedium:
		return 2
	case hakaseagent.TaskPriorityLow:
		return 3
	default:
		return 2
	}
}

// parseStatusFilter maps "open" to the actionable statuses; any other tokens
// are taken as literal status names.
func parseStatusFilter(raw string) []hakaseagent.TaskStatus {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []hakaseagent.TaskStatus
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "open":
			out = append(out,
				hakaseagent.TaskStatusPending,
				hakaseagent.TaskStatusInProgress,
				hakaseagent.TaskStatusBlocked,
			)
		case "":
		default:
			out = append(out, hakaseagent.TaskStatus(strings.ToLower(strings.TrimSpace(part))))
		}
	}
	return out
}

// statusEmoji maps a task status to a glyph.
func statusEmoji(status string) string {
	switch status {
	case "pending":
		return "⚪"
	case "in_progress":
		return "🔵"
	case "completed":
		return "✅"
	case "failed":
		return "❌"
	case "cancelled":
		return "🚫"
	case "blocked":
		return "⛔"
	case "skipped":
		return "⏭️"
	default:
		return "▫️"
	}
}

// CronText renders the cron job registry.
func CronText() string {
	reg, err := cli.CronLoadRegistry()
	if err != nil {
		return "⏰ " + err.Error()
	}
	if len(reg.Jobs) == 0 {
		return "⏰ No cron jobs configured."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("⏰ Cron jobs (%d)\n", len(reg.Jobs)))
	for _, j := range reg.Jobs {
		b.WriteString(fmt.Sprintf("• %s — state: %s", cronJobLabel(j), string(j.State)))
		if j.LastStatus != "" {
			b.WriteString(", last: " + j.LastStatus)
		}
		b.WriteString("\n")
		if j.NextRunAt != nil {
			b.WriteString("  next: " + j.NextRunAt.Local().Format(time.RFC1123) + "\n")
		}
		b.WriteString("  <code>" + j.ID + "</code>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func cronJobLabel(j cli.CronJob) string {
	if j.Name != "" {
		return j.Name
	}
	return j.ID
}

// CronActionText runs a cron management action (run/pause/resume) by id or
// name, mirroring the web handler semantics.
func CronActionText(action, idOrName string) string {
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return "Usage: /cron " + action + " <id-or-name>"
	}
	switch action {
	case "run":
		job, err := cli.CronTriggerJob(idOrName)
		if err != nil {
			return "⏰ " + err.Error()
		}
		return fmt.Sprintf("▶️ Triggered %q (%s). I'll notify you when it finishes — use /notify on if you want the result pushed.", cronJobLabel(job), job.ID)
	case "pause", "resume":
		return cronPauseResume(action, idOrName)
	default:
		return "Unknown cron action: " + action
	}
}

func cronPauseResume(action, idOrName string) string {
	reg, err := cli.CronLoadRegistry()
	if err != nil {
		return "⏰ " + err.Error()
	}
	job, err := cli.CronGetJob(reg, idOrName)
	if err != nil {
		return "⏰ " + err.Error()
	}
	if action == "pause" {
		job.State = cli.CronStatePaused
		job.Enabled = false
		job.NextRunAt = nil
	} else {
		if job.State == cli.CronStateCompleted {
			return "⏰ Cannot resume a completed job: " + cronJobLabel(*job)
		}
		next, err := cli.CronParseSchedule(job.Schedule)
		if err != nil {
			return "⏰ cannot parse stored schedule: " + err.Error()
		}
		job.State = cli.CronStateScheduled
		job.Enabled = true
		job.NextRunAt = &next
	}
	job.UpdatedAt = time.Now().UTC()
	if err := cli.CronSaveRegistry(reg); err != nil {
		return "⏰ " + err.Error()
	}
	verb := "Paused"
	if action == "resume" {
		verb = "Resumed"
	}
	return fmt.Sprintf("%s %s (%s)", verb, cronJobLabel(*job), job.ID)
}
