// gates.go - Interface implementations: ApprovalGate, ClarifyGate, EventNotifier.
// These replace the closure-based globals in main.go. The AppModel implements
// all three interfaces directly.
package tui

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/adk/v2/runner"
)

// ---------------------------------------------------------------------------
// ApprovalGate implementation
// ---------------------------------------------------------------------------

// approvalConfig holds the runtime approval configuration, set via Run().
var approvalConfig interfaces.ApprovalConfig

// AskApproval blocks until the user approves or denies the request, or the
// expiry deadline is reached. Sends an internal approvalPromptMsg to the
// tea program and waits on the response channel.
func (m *AppModel) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
	if m.program == nil {
		return false, fmt.Errorf("no TUI program available")
	}
	resp := make(chan bool, 1)
	m.program.Send(approvalPromptMsg{
		Req: agent.ApprovalRequest{
			Tool:    req.Tool,
			Command: req.Command,
			Args:    req.Args,
			Risk:    req.Risk,
			Reason:  req.Reason,
			Source:  req.Source,
		},
		Resp: resp,
	})
	expiry := m.ApprovalExpiry()
	return waitForApproval(resp, expiry), nil
}

// ApprovalConfig returns the runtime approval configuration.
func (m *AppModel) ApprovalConfig() interfaces.ApprovalConfig {
	return approvalConfig
}

// ApprovalExpiry returns the configured expiry as a time.Duration.
func (m *AppModel) ApprovalExpiry() time.Duration {
	if approvalConfig.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(approvalConfig.ExpirySeconds) * time.Second
}

// SetApprovalConfig sets the runtime approval config.
func SetApprovalConfig(cfg interfaces.ApprovalConfig) {
	approvalConfig = cfg
}

// ---------------------------------------------------------------------------
// ClarifyGate implementation
// ---------------------------------------------------------------------------

// clarifyConfig holds the runtime clarify configuration.
var clarifyConfig interfaces.ClarifyConfig

// AskClarify blocks until the user answers or the deadline is reached.
func (m *AppModel) AskClarify(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
	if m.program == nil {
		return interfaces.ClarifyResponse{}, fmt.Errorf("no TUI program available")
	}
	// Use agent.ClarifyRequest/ClarifyResponse for the internal tea.Msg types.
	agentResp := make(chan agent.ClarifyResponse, 1)
	agentReq := agent.ClarifyRequest{
		Question:    req.Question,
		Choices:     req.Choices,
		MultiSelect: req.MultiSelect,
	}
	m.program.Send(clarifyPromptMsg{
		Req:  agentReq,
		Resp: agentResp,
	})
	expiry := m.ClarifyExpiry()
	go func() {
		time.Sleep(expiry)
		m.program.Send(clarifyTimeoutMsg{})
	}()
	res := waitForClarify(agentResp, expiry)
	return interfaces.ClarifyResponse{
		Answer:   res.Answer,
		Canceled: res.Canceled,
		TimedOut: res.TimedOut,
	}, nil
}

// ClarifyConfig returns the runtime clarify configuration.
func (m *AppModel) ClarifyConfig() interfaces.ClarifyConfig {
	return clarifyConfig
}

// ClarifyExpiry returns the configured expiry as a time.Duration.
func (m *AppModel) ClarifyExpiry() time.Duration {
	if clarifyConfig.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(clarifyConfig.ExpirySeconds) * time.Second
}

// SetClarifyConfig sets the runtime clarify config.
func SetClarifyConfig(cfg interfaces.ClarifyConfig) {
	clarifyConfig = cfg
}

// ---------------------------------------------------------------------------
// EventNotifier implementation
// ---------------------------------------------------------------------------

// TaskUpdate pushes a task board mutation to the TUI.
func (m *AppModel) TaskUpdate(action string, task interfaces.TaskMeta) {
	if m.program == nil {
		return
	}
	m.program.Send(TaskUpdateMsg{
		Task: agent.TaskMeta{
			ID:           task.ID,
			Version:      task.Version,
			Title:        task.Title,
			Description:  task.Description,
			Status:       agent.TaskStatus(task.Status),
			Priority:     agent.TaskPriority(task.Priority),
			Owner:        task.Owner,
			Assignee:     task.Assignee,
			Dependencies: task.Dependencies,
			BlockedBy:    task.BlockedBy,
			CreatedAt:    task.CreatedAt,
			UpdatedAt:    task.UpdatedAt,
			StartedAt:    task.StartedAt,
			CompletedAt:  task.CompletedAt,
			DueAt:        task.DueAt,
			Attempts:     task.Attempts,
			MaxAttempts:  task.MaxAttempts,
			LastError:    task.LastError,
			Result:       task.Result,
			Metadata:     task.Metadata,
			ParentID:     task.ParentID,
			Tags:         task.Tags,
		},
		Action: action,
	})
}

// DelegationProgress streams sub-agent progress to the TUI delegation view.
func (m *AppModel) DelegationProgress(status, taskID, agentName, message string) {
	if m.program != nil {
		m.program.Send(DelegationProgressMsg{
			TaskID:  taskID,
			Agent:   agentName,
			Status:  status,
			Message: message,
		})
	}
}

// CronJobEvent pushes a cron job lifecycle event to the TUI.
func (m *AppModel) CronJobEvent(status, jobID, name, summary, outputPath string) {
	if m.program != nil {
		m.program.Send(CronJobMsg{
			JobID:      jobID,
			Name:       name,
			Status:     status,
			Summary:    summary,
			OutputPath: outputPath,
		})
	}
}

// ---------------------------------------------------------------------------
// Exported constructors
// ---------------------------------------------------------------------------

// NewModel creates a new TUI model.
func NewModel(
	ctx context.Context,
	r *runner.Runner,
	sessionSvc *session.SessionService,
	chatBufferSize int,
	showThinking bool,
	modelName string,
	thinkingLevel string,
) AppModel {
	return newModel(ctx, r, sessionSvc, chatBufferSize, showThinking, modelName, thinkingLevel)
}

// SendStatusLog sends a StatusLogMsg to the program.
func (m *AppModel) SendStatusLog(msg string) {
	if m.program != nil {
		m.program.Send(StatusLogMsg{Text: msg})
	}
}

// SendModelInfo sends a ModelInfoMsg to the program.
func (m *AppModel) SendModelInfo(info *agent.ModelInfo) {
	if m.program != nil {
		m.program.Send(ModelInfoMsg{Info: info})
	}
}

// SetProgram stores the tea.Program reference so interface methods can send
// messages into the TUI event loop.
func (m *AppModel) SetProgram(p interface{}) {
	if prog, ok := p.(*tea.Program); ok {
		m.program = prog
	}
}

// PendingQueue returns the mid-run message queue so main.go can share it
// with the HistoryBuilder for user interjection steering.
func (m *AppModel) PendingQueue() *util.PendingQueue {
	return m.pendingQueue
}

// LogLines returns a copy of the log pane lines (test accessor).
func (m *AppModel) LogLines() []string {
	return append([]string(nil), m.logLines...)
}

// CompactSession runs the /compact slash command (test hook).
func (m *AppModel) CompactSession(focus string) tea.Cmd {
	return m.compactSession(focus)
}
