// Package agent provides the core AI agent orchestration: ADK setup,
// sub-agent delegation, model providers, approval/clarify gates, tool
// management, and task tracking.
package agent

import (
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
	"context"
	"sync"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Deps bundles every external dependency the agent package needs during
// SetupRunner. It replaces the package-level global variables that formerly
// lived in the root package.
type Deps struct {
	Config         *config.Config
	Log            interfaces.LogFunc
	SessionService *hakasesession.SessionService

	// Sandbox config resolved during setup.
	SandboxConfig *sandbox.SandboxConfig

	// Gate configuration (from config.json).
	ApprovalCfg config.ApprovalConfig
	ClarifyCfg  config.ClarifyConfig
	LoopGuard   config.LoopGuardConfig

	// Delegation timeout from config (0 = use default 300s).
	DelegateTimeout time.Duration

	// The ADK model created from the provider.
	Model model.LLM

	// MCP server manager (created during setup by root; stored as any until
	// the mcp package is migrated in task 9). Implements tool.Toolset.
	MCPManager any

	// HistoryBuilder for context management.
	HistoryBuilder *hctx.HistoryBuilder

	// Knowledge enrichment / query expansion callbacks (model-backed).
	EnrichKnowledgeFn func(ctx context.Context, prompt string) (string, error)
	ExpandQueryFn      func(ctx context.Context, query string) ([]string, error)
	EvolveMutateFn     func(ctx context.Context, prompt string) (string, error)

	// --- Bridge factories for root functions (tasks 8-10 will eliminate these) ---

	// NewMCPServerManagerFn creates the MCP server manager from the given config.
	NewMCPServerManagerFn func(cfg any, log interfaces.LogFunc) (any, error)

	// DiscoverMarkdownSkillsFn returns all discovered markdown skills (stored as any
	// until skills_md.go migrates in task 8).
	DiscoverMarkdownSkillsFn func(cwd string, extraDirs []string, log interfaces.LogFunc) any // []MarkdownSkill

	// CreateKnowledgeToolsFn creates knowledge base tools.
	CreateKnowledgeToolsFn func(log interfaces.LogFunc, knowledgeDir string, searchExpansion bool) ([]tool.Tool, error)

	// CreateCronjobToolFn creates the cronjob tool.
	CreateCronjobToolFn func(log interfaces.LogFunc) (tool.Tool, error)

	// StartCronSchedulerFn starts the background cron scheduler.
	StartCronSchedulerFn func(log interfaces.LogFunc)

	// BuildQueryExpansionPromptFn builds the prompt for HyDE-lite query expansion.
	BuildQueryExpansionPromptFn func(query string) string

	// ParseQueryExpansionsFn parses the LLM response for query expansions.
	ParseQueryExpansionsFn func(raw string) []string

	// ResolveVisionProviderFn selects the provider for the vision model.
	ResolveVisionProviderFn func(mainProvider LLMProvider, cfg *config.Config) LLMProvider

	// CreateMediaToolsFn creates media generation tools (image/video/audio).
	CreateMediaToolsFn func(log interfaces.LogFunc) ([]tool.Tool, error)
}

// Runtime holds interactive gate implementations that are wired AFTER
// SetupRunner returns (they depend on the tea.Program pointer which does not
// exist at setup time). All tools close over a *Runtime so nil gates degrade
// to fail-closed.
type Runtime struct {
	mu sync.RWMutex

	approvalGate  interfaces.ApprovalGate
	clarifyGate   interfaces.ClarifyGate
	eventNotifier interfaces.EventNotifier
	modelInfo     interfaces.ModelInfoProvider
}

// SetApprovalGate installs the interactive approval gate (call from main.go).
func (r *Runtime) SetApprovalGate(g interfaces.ApprovalGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalGate = g
}

// ApprovalGate returns the current gate, or nil before wiring.
func (r *Runtime) ApprovalGate() interfaces.ApprovalGate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.approvalGate
}

// SetClarifyGate installs the interactive clarify gate (call from main.go).
func (r *Runtime) SetClarifyGate(g interfaces.ClarifyGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clarifyGate = g
}

// ClarifyGate returns the current gate, or nil before wiring.
func (r *Runtime) ClarifyGate() interfaces.ClarifyGate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clarifyGate
}

// SetEventNotifier installs the event notifier (call from main.go).
func (r *Runtime) SetEventNotifier(n interfaces.EventNotifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventNotifier = n
}

// EventNotifier returns the current notifier, or nil before wiring.
func (r *Runtime) EventNotifier() interfaces.EventNotifier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.eventNotifier
}

// SetModelInfoProvider installs the model info provider (call from main.go).
func (r *Runtime) SetModelInfoProvider(p interfaces.ModelInfoProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelInfo = p
}

// ModelInfo returns the current model info, or nil before wiring.
func (r *Runtime) ModelInfo() *interfaces.ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.modelInfo == nil {
		return nil
	}
	return r.modelInfo.ModelInfo()
}
