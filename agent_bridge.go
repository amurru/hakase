// agent_bridge.go - Type aliases and function wrappers bridging root to internal/agent.
package main

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/skill"
)

// Type aliases for types moved to internal/agent.
type (
	ApprovalRequest   = agent.ApprovalRequest
	ClarifyRequest    = agent.ClarifyRequest
	ClarifyResponse   = agent.ClarifyResponse
	ModelInfo         = agent.ModelInfo
	TaskMeta          = agent.TaskMeta
	TaskStatus        = agent.TaskStatus
	TaskPriority      = agent.TaskPriority
	LogFunc           = agent.LogFunc
	LLMProvider       = agent.LLMProvider
	DegenerationGuard = agent.DegenerationGuard
)

// Skill types, moved to internal/skill/ (task 8).
type (
	SkillMeta              = skill.SkillMeta
	SkillRegistry          = skill.SkillRegistry
	MarkdownSkill          = skill.MarkdownSkill
	MarkdownSkillMeta      = skill.MarkdownSkillMeta
	MarkdownSkillFrontmatter = skill.MarkdownSkillFrontmatter
	EvolutionOptions       = skill.EvolutionOptions
)

// Skill function aliases (moved from root to internal/skill/).
var (
	ValidateSkillName    = skill.ValidateSkillName
	FindProjectRoot      = skill.FindProjectRoot
	DiscoverMarkdownSkills = skill.DiscoverMarkdownSkills
	ParseMarkdownSkill   = skill.ParseMarkdownSkill
	RunEvolutionPass     = skill.RunEvolutionPass
)

// Knowledge types, moved to internal/knowledge/ (task 8).
type (
	KnowledgeNote         = knowledge.KnowledgeNote
	KnowledgeIndex        = knowledge.KnowledgeIndex
	KnowledgeFrontmatter  = knowledge.KnowledgeFrontmatter
	KnowledgeSource       = knowledge.KnowledgeSource
	KnowledgeEnrichment   = knowledge.KnowledgeEnrichment
	KnowledgeLint         = knowledge.KnowledgeLint
	KnowledgeSummary      = knowledge.KnowledgeSummary
	KnowledgeSearchResult = knowledge.KnowledgeSearchResult
	KnowledgeSummaryNote  = knowledge.KnowledgeSummary
	SaveKnowledgeInput    = knowledge.SaveKnowledgeInput
	SaveKnowledgeOutput   = knowledge.SaveKnowledgeOutput
	RecallKnowledgeInput  = knowledge.RecallKnowledgeInput
	RecallKnowledgeOutput = knowledge.RecallKnowledgeOutput
	SearchKnowledgeInput  = knowledge.SearchKnowledgeInput
	SearchKnowledgeOutput = knowledge.SearchKnowledgeOutput
	UpdateKnowledgeInput  = knowledge.UpdateKnowledgeInput
	UpdateKnowledgeOutput = knowledge.UpdateKnowledgeOutput
	LinkKnowledgeInput    = knowledge.LinkKnowledgeInput
	LinkKnowledgeOutput   = knowledge.LinkKnowledgeOutput
	CiteKnowledgeInput    = knowledge.CiteKnowledgeInput
	CiteKnowledgeOutput   = knowledge.CiteKnowledgeOutput
	ListKnowledgeInput    = knowledge.ListKnowledgeInput
	ListKnowledgeOutput   = knowledge.ListKnowledgeOutput
	LintKnowledgeInput    = knowledge.LintKnowledgeInput
	LintKnowledgeOutput   = knowledge.LintKnowledgeOutput
	ScoredKnowledgeNote   = knowledge.ScoredKnowledgeNote
)

// Knowledge function aliases (moved from root to internal/knowledge/).
var (
	GetKnowledgeIndex     = knowledge.GetKnowledgeIndex
	BuildKnowledgeIndex   = knowledge.BuildKnowledgeIndex
	SearchKnowledge       = knowledge.SearchKnowledge
	SearchKnowledgeScored = knowledge.SearchKnowledgeScored
	ResolveTarget         = knowledge.ResolveTarget
	Slugify               = knowledge.Slugify
	ParseKnowledgeNote    = knowledge.ParseKnowledgeNote
	ExtractWikilinks      = knowledge.ExtractWikilinks
	SaveNote              = knowledge.SaveNote
	UpdateNote            = knowledge.UpdateNote
	UpdateIndexFile       = knowledge.UpdateIndexFile
	AppendLog             = knowledge.AppendLog
	KnowledgeDir          = knowledge.KnowledgeDir
	NotePath              = knowledge.NotePath
	InvalidateKnowledgeCache = knowledge.InvalidateKnowledgeCache
	ExpandQueryFn         = knowledge.ExpandQueryFn
	EnrichKnowledgeFn     = knowledge.EnrichKnowledgeFn
	BuildQueryExpansionPrompt = knowledge.BuildQueryExpansionPrompt
	ParseQueryExpansions  = knowledge.ParseQueryExpansions
	CreateKnowledgeTools  = knowledge.CreateKnowledgeTools
	ExpandSearchQuery     = knowledge.ExpandSearchQuery
	FirstSnippet          = knowledge.FirstSnippet
	SerializeNote         = knowledge.SerializeNote
	AppendAfterSection    = knowledge.AppendAfterSection
)

// Bench eval types (in skill package, used by knowledge CLI).
type (
	BenchSet   = skill.BenchSet
	BenchQuery = skill.BenchQuery
)
var LoadBenchSet = skill.LoadBenchSet

// Task status constants (values, not types).
const (
	TaskStatusPending    = agent.TaskStatusPending
	TaskStatusInProgress = agent.TaskStatusInProgress
	TaskStatusCompleted  = agent.TaskStatusCompleted
	TaskStatusFailed     = agent.TaskStatusFailed
	TaskStatusCancelled  = agent.TaskStatusCancelled
	TaskStatusSkipped    = agent.TaskStatusSkipped
	TaskStatusBlocked    = agent.TaskStatusBlocked
	TaskStatusArchived   = agent.TaskStatusArchived
)

// Task priority constants (values, not types).
const (
	TaskPriorityCritical = agent.TaskPriorityCritical
	TaskPriorityHigh     = agent.TaskPriorityHigh
	TaskPriorityMedium   = agent.TaskPriorityMedium
	TaskPriorityLow      = agent.TaskPriorityLow
)

// Function aliases - these reference the exported functions from internal/agent.
var (
	modelPromptFn            = agent.ModelPromptFn
	buildSubAgentTools       = agent.BuildSubAgentTools
	filterBlockedTools       = agent.FilterBlockedTools
	buildGenerationConfig    = agent.BuildGenerationConfig
	guardDefaults            = agent.GuardDefaults
	loopGuardConfig          = agent.LoopGuardConfig
	toolCallRepairMessage    = agent.ToolCallRepairMessage
	isToolCallJSONErr        = agent.IsToolCallJSONErr
	maxToolCallRepairAttempts = agent.MaxToolCallRepairAttempts
	truncate                 = agent.Truncate
	guardReasonLog           = agent.GuardReasonLog
	ProviderFactory          = agent.ProviderFactory
	FetchModelInfo           = agent.FetchModelInfo
	LoadTaskRegistry         = agent.LoadTaskRegistry
)

// Additional function exports for sandbox hooks
var (
	ApprovalExpiry   = agent.ApprovalExpiry
	ApproveExec      = agent.ApproveExec
	AuditCommandExec = agent.AuditCommandExec
	EvaluateCommand  = agent.EvaluateCommand
)

// Type alias for CommandAuditEntry
type CommandAuditEntry = agent.CommandAuditEntry

// MCP types, moved to internal/mcp/ (task 9).
type (
	MCPServerManager = mcp.MCPServerManager
	MCPServerStatus  = mcp.MCPServerStatus
)

// MCP function aliases.
var (
	NewMCPServerManager = mcp.NewMCPServerManager
)

// Task management types and functions
type (
	CreateTaskInput  = agent.CreateTaskInput
	CreateTaskOutput = agent.CreateTaskOutput
	UpdateTaskInput  = agent.UpdateTaskInput
	UpdateTaskOutput = agent.UpdateTaskOutput
	ListTasksInput   = agent.ListTasksInput
	ListTasksOutput  = agent.ListTasksOutput
	GetTaskInput     = agent.GetTaskInput
	GetTaskOutput    = agent.GetTaskOutput
	DeleteTaskInput  = agent.DeleteTaskInput
	DeleteTaskOutput = agent.DeleteTaskOutput
	ArchiveTaskInput = agent.ArchiveTaskInput
	ArchiveTaskOutput = agent.ArchiveTaskOutput
)

var (
	createTask  = agent.CreateTask
	updateTask  = agent.UpdateTask
	listTasks   = agent.ListTasks
	getTask     = agent.GetTask
	deleteTask  = agent.DeleteTask
	archiveTask = agent.ArchiveTask
)

// Task registry function
var loadTaskRegistry = agent.LoadTaskRegistry

// Provider types
type (
	GeminiProvider = agent.GeminiProvider
	OpenAIProvider = agent.OpenAIProvider
)

// Additional exported functions
var (
	contextBlockFor            = agent.ContextBlockFor
	createLoadMarkdownSkillTool = agent.CreateLoadMarkdownSkillTool
)

// Bridge for test-only access to DiscoverMarkdownSkills (now in internal/skill/)
func init() {
	skill.DiscoverMarkdownSkillsForTest = func(cwd string, extraDirs []string, log interfaces.LogFunc) []skill.MarkdownSkill {
		return skill.DiscoverMarkdownSkills(cwd, extraDirs, log)
	}
}
