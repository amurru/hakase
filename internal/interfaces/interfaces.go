// Package interfaces defines the contract types that replace package-level
// global variable coupling in the root package. Each interface corresponds to
// one or more globals currently wired in main.go:58-142 and setupRunner.
//
// These interfaces are the CONTRACT that tasks 4-14 implement against.
// Accuracy of signatures matters more than completeness of prose.
package interfaces

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// 1. Shared contract types (mirror the real types currently in package main)
// ---------------------------------------------------------------------------

// ApprovalRequest describes a tool invocation needing user approval.
// Mirrors approval.go:ApprovalRequest.
type ApprovalRequest struct {
	Tool      string    // "system_exec" | "python_interpreter"
	Command   string    // full command line (or code preview, truncated to 2000 runes)
	Args      []string  // explicit args (when not using shell parsing)
	Risk      string    // Risk* name
	Reason    string    // why approval is required
	Source    string    // "direct" | "delegated"
	ExpiresAt time.Time // deadline for user response
}

// ApprovalConfig tunes the interactive approval gate.
// Mirrors config.go:ApprovalConfig.
type ApprovalConfig struct {
	Mode          string // "interactive" (default) | "deny" | "allow"
	ExpirySeconds int    // default 60
}

// ClarifyRequest describes a mid-task question for the user.
// Mirrors clarify.go:ClarifyRequest.
type ClarifyRequest struct {
	Question    string   // question text
	Choices     []string // optional answer choices (max 4)
	MultiSelect bool     // allow multiple choices (only with Choices)
}

// ClarifyResponse is the user's answer, plus cancel/timeout signals.
// Mirrors clarify.go:ClarifyResponse.
type ClarifyResponse struct {
	Answer   []string // selected choice(s) or free-text answer (1 element)
	Canceled bool     // user pressed Esc
	TimedOut bool     // no answer within the expiry window
}

// ClarifyConfig tunes the interactive clarify gate.
// Mirrors config.go:ClarifyConfig.
type ClarifyConfig struct {
	ExpirySeconds int // default 120
}

// TaskStatus enumerates the lifecycle states of a tracked task.
// Mirrors agent.go:TaskStatus.
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

// TaskPriority indicates urgency.
// Mirrors agent.go:TaskPriority.
type TaskPriority string

const (
	TaskPriorityCritical TaskPriority = "critical"
	TaskPriorityHigh     TaskPriority = "high"
	TaskPriorityMedium   TaskPriority = "medium"
	TaskPriorityLow      TaskPriority = "low"
)

// TaskMeta is the task board's core record.
// Mirrors agent.go:TaskMeta.
type TaskMeta struct {
	ID           string     `json:"id"`
	Version      int        `json:"version"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Status       TaskStatus `json:"status"`
	Priority     TaskPriority `json:"priority"`
	Owner        string     `json:"owner,omitempty"`
	Assignee     string     `json:"assignee,omitempty"`
	Dependencies []string   `json:"dependencies,omitempty"`
	BlockedBy    []string   `json:"blocked_by,omitempty"`
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

// ModelInfo describes the selected model's capabilities as reported by its provider.
// Mirrors modelinfo.go:ModelInfo.
type ModelInfo struct {
	Name            string // provider-reported model name
	ContextWindow   int64  // total context budget (tokens)
	MaxInputTokens  int64  // max input-token allowance (0 when not reported separately)
	ThinkingEnabled bool   // whether thinking/CoT is supported
	ThinkingLevel   string // provider string: "maximum", "xhigh", etc.
	SupportsVision  *bool  // whether model accepts image input; nil = unknown
	Source          string // "gemini" | "openai" | "openai-compatible"
}

// SandboxMode selects the sandboxing strategy.
// Mirrors sandbox.go:SandboxMode.
type SandboxMode string

const (
	SandboxModeOff        SandboxMode = "off"
	SandboxModePaths      SandboxMode = "paths"
	SandboxModeBubblewrap SandboxMode = "bubblewrap"
	SandboxModeLandlock   SandboxMode = "landlock"
)

// SandboxConfig is the resolved, normalized sandbox configuration.
// Mirrors sandbox.go:SandboxConfig.
type SandboxConfig struct {
	Mode            SandboxMode
	WorkspaceRoots  []string
	ReadRoots       []string
	DenyRoots       []string
	AllowNetwork    bool
	AllowPipInstall bool
	Permissions     map[string]string
	AllowedCommands []string
	DenyPatterns    []string
	RiskThreshold   string
	AllowFallback   bool
}

// LoopGuardConfig tunes anti-degeneration guardrails.
// Mirrors config.go:LoopGuardConfig.
type LoopGuardConfig struct {
	MaxOutputTokens     int32
	RepetitionLimit     int
	MaxTextWithoutTool  int
}

// LogFunc is a thread-safe callback to send status messages to the UI/log.
// Mirrors agent.go:LogFunc.
type LogFunc func(msg string)

// MCPServerStatus is a snapshot of one MCP server's runtime state.
// Mirrors mcp_servers.go:MCPServerStatus.
type MCPServerStatus struct {
	Name      string
	Type      string // "stdio" | "http"
	Transport string // endpoint URL or command line
	Disabled  bool
	ToolCount int
	Status    string // "idle" | "connected" | "failed" | "disabled"
	Error     string
}

// SystemInfo is the detected runtime environment of the host machine.
// Mirrors env.go:SystemInfo, stripped to the portable subset.
type SystemInfo struct {
	OS                string
	Architecture      string
	KernelVersion     string
	DistroID          string
	DistroVersion     string
	DistroCodename    string
	DistroPretty      string
	PackageManager    string
	Shell             string
	Locale            string
	Timezone          string
	TZOffset          string
	Username          string
	HomeDir           string
	Hostname          string
	WorkspaceRoot     string
	DiskFreeHuman     string
	MemoryTotalHuman  string
	MemoryAvailHuman  string
	ExecSandbox       string
}

// ---------------------------------------------------------------------------
// 2. Interface definitions
// ---------------------------------------------------------------------------

// ApprovalGate replaces the askApproval function global (approval.go:22).
// It provides an interactive approval gate for tool invocations needing user
// confirmation. The TUI installs an implementation at startup; headless mode
// returns a fail-closed implementation (nil gate = deny).
type ApprovalGate interface {
	// AskApproval blocks until the user approves or denies the request, or the
	// expiry deadline is reached. Returns (approved, error). Fail-closed:
	// when the implementation is nil, the caller treats it as denied.
	AskApproval(req ApprovalRequest) (bool, error)

	// ApprovalConfig returns the runtime approval configuration (mode, expiry).
	ApprovalConfig() ApprovalConfig

	// ApprovalExpiry returns the configured expiry as a time.Duration.
	// Default 60s when ExpirySeconds <= 0.
	ApprovalExpiry() time.Duration
}

// ClarifyGate replaces the askClarify function global (clarify.go:45).
// It provides an interactive gate for mid-task user questions via the TUI
// clarify modal. The TUI installs an implementation at startup; headless
// mode returns a fail-closed implementation.
type ClarifyGate interface {
	// AskClarify blocks until the user answers the question or the expiry
	// deadline is reached. Returns the response (which may carry Canceled or
	// TimedOut flags). Fail-closed: nil gate returns an error.
	AskClarify(req ClarifyRequest) (ClarifyResponse, error)

	// ClarifyConfig returns the runtime clarify configuration (expiry).
	ClarifyConfig() ClarifyConfig

	// ClarifyExpiry returns the configured expiry as a time.Duration.
	// Default 120s when ExpirySeconds <= 0.
	ClarifyExpiry() time.Duration
}

// ApprovalResponder resolves a pending approval request by ID. Implemented by
// the web approval gate so external surfaces (e.g. the Telegram channel) can
// answer the same prompts the web UI sees. Returns false when the ID is
// unknown or the request already timed out or was answered elsewhere - first
// responder wins, whoever that is.
type ApprovalResponder interface {
	RespondApproval(approvalID string, approved bool) bool
}

// ClarifyResponder resolves a pending clarification by ID with a free-text or
// single-choice answer. Implemented by the web clarify gate; see
// ApprovalResponder for the first-responder-wins contract.
type ClarifyResponder interface {
	RespondClarify(clarifyID string, response ClarifyResponse) bool
}

// EventNotifier replaces three function globals: taskBoardNotify (agent.go:529),
// delegationProgressNotify (delegate.go:90), and cronJobNotify (cronjob.go:408).
// It pushes structured events from tool handlers and the background scheduler
// to the TUI's Bubble Tea message loop.
type EventNotifier interface {
	// TaskUpdate pushes a task board mutation to the TUI.
	// action: "created", "updated", "completed", "failed", "claimed"
	TaskUpdate(action string, task TaskMeta)

	// DelegationProgress streams sub-agent progress (text, tool calls, status)
	// to the TUI delegation view.
	// status: "started", "running", "thinking", "tool_call", "tool_result",
	//   "log", "completed", "failed", "timed_out"
	DelegationProgress(status, taskID, agent, message string)

	// CronJobEvent pushes a cron job lifecycle event to the TUI.
	// status: "scheduled", "started", "completed", "failed", "silent", "triggered"
	CronJobEvent(status, jobID, name, summary, outputPath string)

	// SidekickEvent pushes a sidekick advisory note to the TUI.
	// severity: "info" | "suggestion" | "warning" | "critical"
	SidekickEvent(sessionID, severity, text string)
}

// ModelInfoProvider replaces the currentModelInfo pointer global (vision.go:103).
// It holds the provider-reported model capabilities fetched asynchronously at
// startup and consumed by the vision tool and context compaction budget logic.
type ModelInfoProvider interface {
	// ModelInfo returns the current model capabilities, or nil when the async
	// fetch has not yet completed.
	ModelInfo() *ModelInfo
}

// SandboxProvider replaces the currentSandbox pointer global (systemexec.go:28).
// It holds the resolved, normalized sandbox configuration set during setupRunner.
// A nil SandboxConfig means confinement is disabled.
type SandboxProvider interface {
	// Sandbox returns the current sandbox configuration, or nil when disabled.
	Sandbox() *SandboxConfig
}

// ConfigProvider replaces the currentConfig pointer global (agent.go:507).
// It holds the loaded *Config set during startup and provides read-only access.
type ConfigProvider interface {
	// Config returns the loaded configuration.
	Config() any // *Config; typed as any until internal/config is defined (task 4)
}

// HistoryBuilderFactory replaces the currentHistoryBuilder pointer global
// (agent.go:526). Note: this is a FACTORY interface because a new HistoryBuilder
// is created per session in setupRunner, but the running instance is wired
// into the orchestrator's BeforeModelCallback and receives ModelInfo updates
// asynchronously.
type HistoryBuilderFactory interface {
	// NewHistoryBuilder creates a HistoryBuilder bound to a session service.
	// svc may be nil (history injection is a no-op in that case).
	NewHistoryBuilder(svc any) any // returns *HistoryBuilder; typed as any until internal/context (task 9)

	// CurrentHistoryBuilder returns the currently active HistoryBuilder, or nil.
	CurrentHistoryBuilder() any // *HistoryBuilder
}

// MCPManagerProvider replaces the currentMCPManager pointer global (mcp_servers.go:35).
// It holds the live MCP server manager shared by the TUI (/mcp), sub-agent
// delegation, and headless cron runs.
type MCPManagerProvider interface {
	// MCPManager returns the current MCP server manager, or nil when no MCP config is usable.
	MCPManager() any // *MCPServerManager; typed as any until internal/mcp (task 13)

	// ListServers returns the current status of every configured server.
	ListServers() []MCPServerStatus

	// ServerStatus returns the status of one server by name.
	ServerStatus(name string) (MCPServerStatus, bool)

	// SetDisabled toggles a server at runtime and persists the change.
	SetDisabled(name string, disabled bool) error

	// Reconnect clears a server's status so its next tool fetch re-runs
	// connect and tools/list.
	Reconnect(name string) error
}

// ---------------------------------------------------------------------------
// 3. Additional interfaces (globals discovered beyond the plan's list)
// ---------------------------------------------------------------------------

// SystemInfoProvider replaces the currentSystemInfo pointer global (env.go:50).
// It holds the session's detected runtime environment, set once in setupRunner
// and read-only thereafter. Used by the environment block injection and
// staleness detection.
type SystemInfoProvider interface {
	// SystemInfo returns the detected system info, or nil when not yet detected.
	SystemInfo() *SystemInfo
}

// QueryExpander replaces the expandQueryFn function global (knowledge.go:547).
// It is a model-backed query-expansion callback set in setupRunner alongside
// KnowledgeEnricher. When nil, plain substring search is used.
type QueryExpander interface {
	// Expand returns 2-3 rephrasings of the query (paraphrase + hypothetical
	// answer, HyDE-lite style). On failure, callers fall back silently to
	// plain substring search.
	Expand(ctx context.Context, query string) ([]string, error)
}

// KnowledgeEnricher replaces the enrichKnowledgeFn function global (knowledge.go:1149).
// It is a model-backed enrichment callback set in setupRunner. When nil,
// save_knowledge uses only the deterministic extractors.
type KnowledgeEnricher interface {
	// Enrich produces structured enrichment JSON for a knowledge note.
	Enrich(ctx context.Context, prompt string) (string, error)
}

// EvolveMutator replaces the evolveMutateFn function global (evolver.go:29).
// It is a model-backed mutation callback for the darwinian-evolver skill
// evolution loop. Defaults to modelPromptFn so the engine works anywhere a
// model is available; tests override it with a deterministic fake.
type EvolveMutator interface {
	// Mutate proposes a fixed implementation for a failing skill.
	Mutate(ctx context.Context, prompt string) (string, error)
}

// ---------------------------------------------------------------------------
// 4. Composite / wiring interfaces
// ---------------------------------------------------------------------------

// ScopeGate bundles ApprovalGate + ClarifyGate for convenience. A single
// implementation (the TUI model) implements both, so callers that need both
// can depend on one interface.
type ScopeGate interface {
	ApprovalGate
	ClarifyGate
}

// ServiceRegistry is the top-level service locator that the TUI model and
// CLI commands use to access runtime services. During migration (tasks 4-14),
// each service is progressively moved into its own internal/ package and
// registered here.
type ServiceRegistry interface {
	ScopeGate
	EventNotifier
	ModelInfoProvider
	SandboxProvider
	ConfigProvider
	HistoryBuilderFactory
	MCPManagerProvider
	SystemInfoProvider
}
