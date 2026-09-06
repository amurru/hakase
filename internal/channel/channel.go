// Package channel implements hakase's communication-channel subsystem: a
// transport-neutral core (pairing/auth, per-chat run state, bridge event
// routing, formatting, service lifecycle) with per-transport implementations
// in subpackages (internal/channel/telegram). Channels let a remote client -
// today a Telegram bot, tomorrow other chat surfaces - prompt the agent,
// watch progress, answer approval/clarify prompts, and manage tasks and cron
// jobs. The subsystem runs inside the web/serve process, which owns the
// runner, gates, bridge, and session service it needs.
package channel

import "context"

// LogFunc is the subsystem's logging seam.
type LogFunc func(format string, args ...any)

// Channel is one communication transport. Run blocks until ctx is cancelled
// (or the transport fails); the Service runs it in its own goroutine.
type Channel interface {
	// Name identifies the transport ("telegram"); used for state keys.
	Name() string
	// Run starts the transport and blocks until ctx is cancelled.
	Run(ctx context.Context) error
}

// PushHandler receives routed bridge events. The core Router parses the
// event payloads (approval/clarify prompts, cron/task/delegation lifecycle)
// and hands them to each registered transport, which decides which of its
// chats to notify and how to render them.
type PushHandler interface {
	// ApprovalPrompt asks the user to approve or deny a pending tool call.
	ApprovalPrompt(id, tool, risk, reason, command string)
	// ClarifyPrompt asks the user to answer a mid-task question, optionally
	// from a list of choices (multiSelect allows several).
	ClarifyPrompt(id, question string, choices []string, multiSelect bool)
	// CronEvent reports a cron job lifecycle transition (completed, failed,
	// silent) - only the user-relevant statuses are routed.
	CronEvent(status, jobID, name, summary, outputPath string)
	// TaskEvent reports a task-board mutation (completed, failed only).
	TaskEvent(action string, id, title, status string)
	// DelegationEvent reports a finished sub-agent delegation.
	DelegationEvent(status, taskID, agent, message string)
}
