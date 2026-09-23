package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"amurru/hakase/internal/sandbox"
)

// DefaultSystemEnvMaxChars caps the rendered environment block.
const DefaultSystemEnvMaxChars = 800

// LoopGuardConfig tunes the anti-degeneration guardrails that abort a run
// that starts producing looped or text-only output. Zero values fall back to
// the defaults in loopguard.go (defaultLoopGuard). These bounds prevent a
// degenerate provider from burning the whole context/output window.
type LoopGuardConfig struct {
	// MaxOutputTokens caps the provider maxOutputTokens for every agent
	// (root + delegated). 0 uses defaultMaxOutputTokens. Prevents a run from
	// generating for minutes into a full output window.
	MaxOutputTokens int32 `json:"max_output_tokens,omitempty"`
	// RepetitionLimit aborts a run after this many consecutive identical
	// non-thought text chunks. 0 uses defaultRepetitionLimit.
	RepetitionLimit int `json:"repetition_limit,omitempty"`
	// MaxTextWithoutTool aborts a run that streams this many runes of
	// non-thought text with zero tool calls (a text-only bloat / refusal
	// loop). 0 uses defaultMaxTextWithoutTool.
	MaxTextWithoutTool int `json:"max_text_without_tool,omitempty"`
}

// ApprovalConfig tunes the interactive approval gate.
type ApprovalConfig struct {
	// Mode: "interactive" (default) | "deny" (auto-deny everything) | "allow" (auto-approve everything).
	Mode          string `json:"mode,omitempty"`
	ExpirySeconds int    `json:"expiry_seconds,omitempty"` // default 60
}

// ClarifyConfig tunes the interactive clarify gate.
type ClarifyConfig struct {
	// ExpirySeconds is how long the tool waits for a user answer before
	// returning a timed-out response. 0 uses the default (120s).
	ExpirySeconds int `json:"expiry_seconds,omitempty"`
}

// ContextFilesConfig tunes the project context files (AGENTS.md) feature.
type ContextFilesConfig struct {
	// MaxChars caps the per-file content contributed to the system
	// instruction. 0 uses defaultContextFileMaxChars (20000). Content beyond
	// the cap is truncated with a 70% head / 20% tail split and a marker.
	MaxChars int `json:"max_chars,omitempty"`
	// ApplyTo restricts which agents receive the rendered context block.
	// Empty means all agents. Valid names: orchestrator, web_researcher,
	// code_interpreter, general_purpose.
	ApplyTo []string `json:"apply_to,omitempty"`
}

// SystemEnvConfig tunes the runtime-environment block (OS/distro/arch,
// package manager, toolchains, disk/memory) injected into agent system
// instructions at session start.
type SystemEnvConfig struct {
	// Enabled toggles the environment block. Absent (nil) = enabled, which is
	// the default. A pointer keeps "absent" distinguishable from "false" so a
	// missing system_env block cannot accidentally disable the feature.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxChars caps the rendered block. 0 uses DefaultSystemEnvMaxChars (800).
	MaxChars int `json:"max_chars,omitempty"`
	// ApplyTo restricts which agents receive the rendered environment block.
	// Empty means all agents. Valid names: orchestrator, web_researcher,
	// code_interpreter, general_purpose.
	ApplyTo []string `json:"apply_to,omitempty"`
}

// UnitsConfig tunes the user's preferred measurement system. It is rendered
// as a system-reminder block so the agent reports physical quantities (length,
// mass, volume, temperature, speed, area) in the user's preferred units.
// Unset defaults to the metric (SI/ISO) system.
type UnitsConfig struct {
	// System selects the preferred measurement system: "metric" (SI/ISO;
	// default) or "imperial". An empty/missing value uses metric.
	System string `json:"system,omitempty"`
}

// AuthConfig tunes the web authentication layer (cookie security, login
// hardening). Zero values are the secure defaults.
type AuthConfig struct {
	// AllowInsecureCookie permits the session cookie to be set without the
	// Secure attribute (e.g. plain-HTTP localhost deployments). Default
	// false: cookies are Secure-only. Consumed by the web cookie setter
	// (security-hardening Task 17 - W8).
	AllowInsecureCookie bool `json:"allow_insecure_cookie"`
}

type Config struct {
	Provider  string `json:"provider"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url,omitempty"`
	// Instruction is an optional, additional customization rendered into the
	// agent instructions as a "USER CONFIG INSTRUCTION" section alongside the
	// discovered project context files (AGENTS.md). It is NOT a replacement
	// for the built-in system prompts; it only adds.
	Instruction string `json:"instruction"`
	// InstructionFiles lists extra context files (local paths or http(s)
	// URLs) merged into the project context, after project and user-level
	// AGENTS.md files. Local paths may be absolute, "~/"-prefixed, or
	// relative to the project root. Remote URLs are fetched at startup with
	// a short timeout; fetch failures are skipped, never fatal.
	InstructionFiles []string `json:"instruction_files,omitempty"`
	// ContextFiles tunes project-context loading: MaxChars caps each file
	// (0 uses 20000), ApplyTo restricts which agents receive the rendered
	// block (empty = all agents).
	ContextFiles ContextFilesConfig `json:"context_files,omitempty"`
	// SystemEnv tunes the runtime-environment block injected into agent
	// instructions. Absent = enabled with default caps. Set enabled:false to
	// disable, max_chars to cap the rendered block.
	SystemEnv SystemEnvConfig `json:"system_env,omitempty"`
	// Units tunes the user's preferred measurement system injected as a
	// system-reminder block so the agent reports quantities in the user's
	// preferred units. Absent = metric (SI/ISO).
	Units        UnitsConfig `json:"units,omitempty"`
	MCPServerURL string      `json:"mcp_server_url"`
	// MCPServers configures MCP servers (see mcp_config.go). Legacy
	// mcp_server_url is auto-migrated into the "lightpanda" server.
	MCPServers        MCPConfig              `json:"mcp,omitempty"`
	FallbackProviders []string               `json:"fallback_providers,omitempty"`
	SkillDirs         []string               `json:"skill_dirs,omitempty"`
	KnowledgeDir      string                 `json:"knowledge_dir,omitempty"`
	ProviderOptions   map[string]interface{} `json:"provider_options,omitempty"`
	ChatBufferSize    int                    `json:"chat_buffer_size,omitempty"`
	ShowThinking      bool                   `json:"show_thinking,omitempty"`
	TaskCheckpoint    bool                   `json:"task_checkpoint,omitempty"`
	// ThinkingLevel is passed through to the provider as the thinking depth
	// ("off", "low", "medium", "high", "maximum", "xhigh"); empty = provider default.
	ThinkingLevel string `json:"thinking_level,omitempty"`
	// SummaryModel optionally names a cheaper/weaker model used for context
	// compaction summarization (the plan's "cheap/weak model if available").
	// When empty, the primary model is used for summaries.
	SummaryModel string `json:"summary_model,omitempty"`
	// VisionModel optionally names a multimodal model used to describe images
	// when the primary model has no vision support (the legacy path). When
	// empty and the primary model is not vision-capable, the vision tool
	// warns and continues. Set e.g. "google/gemini-3-flash-preview:free".
	VisionModel string `json:"vision_model,omitempty"`
	// VisionBaseURL optionally overrides the endpoint used for the vision
	// model. When empty, the primary base_url is used.
	VisionBaseURL string `json:"vision_base_url,omitempty"`
	// VisionAPIKey optionally overrides the API key used for the vision
	// model. When empty, the primary api_key is used.
	VisionAPIKey string `json:"vision_api_key,omitempty"`
	// VisionProvider optionally selects the provider used for the vision
	// model: "gemini", "openai", or "openai-compatible". When empty, the
	// primary provider is used (vision_base_url alone still forces an
	// OpenAI-compatible endpoint). Needed when the vision model lives on a
	// different backend than the primary - e.g. a gemini vision model while
	// the primary provider is openai-compatible.
	VisionProvider string `json:"vision_provider,omitempty"`
	// ModelVision overrides multimodal detection for the primary model:
	// "auto" (default), "yes", or "no".
	ModelVision string `json:"model_vision,omitempty"`
	// DelegateTimeoutSeconds bounds how long a delegated sub-agent may run
	// before it is aborted as timed out. 0 uses the default (300s). Prevents
	// stuck sub-agents from hanging the orchestrator indefinitely.
	DelegateTimeoutSeconds int `json:"delegate_timeout_seconds,omitempty"`
	// Debug enables dev-mode structured JSON logging of all system events to
	// ./logs/ for development and troubleshooting. Off by default.
	Debug bool `json:"debug,omitempty"`
	// Sandbox optionally configures workspace path confinement and subprocess
	// sandboxing. nil/absent = sandbox disabled (backward compatible). See
	// sandbox.go for the full shape and defaults.
	Sandbox *sandbox.SandboxJSON `json:"sandbox,omitempty"`
	// LoopGuard enables anti-degeneration guardrails (max output cap, repetition
	// and no-tool-call watchdogs). Absent/zero values use loopguard.go defaults.
	LoopGuard LoopGuardConfig `json:"loop_guard,omitempty"`
	// Approval tunes the interactive approval gate for harmful-command
	// protection. Absent/zero values use defaults (interactive mode, 60s expiry).
	Approval ApprovalConfig `json:"approval,omitempty"`
	// Clarify tunes the interactive clarify gate for mid-task questions.
	// Absent/zero values use defaults (120s expiry).
	Clarify ClarifyConfig `json:"clarify,omitempty"`
	// Auth tunes the web authentication layer (cookie security, login
	// hardening). Absent/zero values are the secure defaults (cookie Secure
	// flag on). The web bootstrap (cmd/hakase/web.go) may override with the
	// --insecure-cookie CLI flag.
	Auth AuthConfig `json:"auth,omitempty"`
	// SearchExpansion enables HyDE-lite LLM query expansion for
	// search_knowledge (plan Phase 3d-4). Default false: when off, search
	// behavior is byte-identical to plain substring search. When on, one
	// model call per search expands the query into 2-3 phrasings which are
	// OR-matched and fused with Reciprocal Rank Fusion; on failure or
	// timeout it falls back silently to plain substring search.
	SearchExpansion bool `json:"search_expansion,omitempty"`
	// Media configures pluggable media generation (image/video/audio).
	Media MediaConfig `json:"media,omitempty"`
	// Sidekick tunes the optional second-LLM "sidekick" agent (side-process
	// and/or watchdog). Absent/zero values fall back to sidekick defaults
	// (see SidekickConfig). Disabling requires only enabled:false or an empty
	// model_name.
	Sidekick SidekickConfig `json:"sidekick,omitempty"`
	// WebSearch controls the built-in keyless web search fallback
	// (internal/websearch) that activates when no research-capable MCP
	// server is connected. enabled=false switches the feature (and its
	// outbound calls) off; force=true keeps the fallback tools visible even
	// when research MCP tools are connected.
	WebSearch WebSearchConfig `json:"web_search,omitempty"`
	// Channels configures communication channels (Telegram bot, extensible to
	// other chat transports) that let a remote client prompt the agent, watch
	// progress, answer approvals, and manage tasks/cron jobs. Channels run
	// inside the web/serve process and are off unless explicitly enabled.
	Channels ChannelsConfig `json:"channels,omitempty"`
	// Sleep tunes the SkillOpt-Sleep offline self-improvement loop
	// (docs/skillopt-sleep/plan.md Phase 2, SL-023). Every field is
	// optional with a safe default; the loop only runs when explicitly
	// invoked (`hakase sleep run`) or scheduled via `hakase sleep schedule`.
	// See SleepConfig for the per-field meaning.
	Sleep SleepConfig `json:"sleep,omitempty"`
	// Memory tunes agent-written auto-memory (docs/auto-memory/spec.md):
	// typed notes the agent saves via the remember/forget_memory tools,
	// persisted at ~/.hakase/memory/notes.json and injected at session
	// start. On by default; enabled:false removes the tools and the
	// session-start injection. See MemoryConfig for the per-field meaning.
	Memory MemoryConfig `json:"memory,omitempty"`
	// Tracing configures OpenTelemetry GenAI tracing over OTLP/HTTP
	// (docs/otel-tracing/spec.md, issue #18): one waterfall trace per agent
	// run — LLM calls with token usage, tool calls with durations, delegated
	// sub-agents nested. Off unless explicitly enabled; disabled tracing
	// installs nothing (no exporter, no network traffic).
	Tracing TracingConfig `json:"tracing,omitempty"`
	// Session tunes per-session persistence behavior (docs/session-rewind/
	// spec.md, issue #21). See SessionConfig for the per-field meaning.
	Session SessionConfig `json:"session,omitempty"`
}

// Tracing default constants.
const (
	// DefaultTracingEndpoint is the OTLP/HTTP convention (collector default
	// port 4318). Scheme decides TLS: https upgrades, http stays plaintext.
	DefaultTracingEndpoint = "http://localhost:4318"
	// DefaultTracingSampleRatio samples every root span when tracing is on.
	DefaultTracingSampleRatio = 1.0
)

// TracingConfig configures OpenTelemetry tracing export (issue #18).
type TracingConfig struct {
	// Enabled turns tracing on. The zero value is the feature default
	// (off) — no tri-state pointer needed, unlike sections that default on.
	Enabled bool `json:"enabled,omitempty"`
	// Endpoint is the OTLP/HTTP base URL receiving the spans. Default
	// http://localhost:4318.
	Endpoint string `json:"endpoint,omitempty"`
	// Headers are sent verbatim on every OTLP export request (vendor auth
	// like Langfuse basic-auth pairs).
	Headers map[string]string `json:"headers,omitempty"`
	// SampleRatio is the root-sampling probability in [0,1]; children
	// follow their parent. Default 1.
	SampleRatio float64 `json:"sample_ratio,omitempty"`
}

// ApplyDefaults fills zero values with defaults. Call after loading config.
func (c *TracingConfig) ApplyDefaults() {
	if c.Endpoint == "" {
		c.Endpoint = DefaultTracingEndpoint
	}
	if c.SampleRatio == 0 {
		c.SampleRatio = DefaultTracingSampleRatio
	}
}

// Validate checks TracingConfig for sane values.
func (c *TracingConfig) Validate() error {
	if math.IsNaN(c.SampleRatio) || c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("invalid tracing.sample_ratio %v: must be within [0,1]", c.SampleRatio)
	}
	for k := range c.Headers {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("invalid tracing.headers: header keys must be non-empty")
		}
	}
	return nil
}

// Session default constants.
const (
	// DefaultSessionSnapshotsMax bounds the pre-turn snapshot ring per
	// session (issue #21). Oldest snapshots are pruned beyond it.
	DefaultSessionSnapshotsMax = 50
)

// SessionConfig tunes per-session persistence behavior.
type SessionConfig struct {
	// Snapshots configures restore-to-message checkpoints
	// (docs/session-rewind/spec.md). On by default; see SnapshotsConfig.
	Snapshots SnapshotsConfig `json:"snapshots,omitempty"`
}

// SnapshotsConfig configures pre-turn session snapshots (issue #21).
type SnapshotsConfig struct {
	// Enabled tri-state: nil (default) = on; an explicit false stops taking
	// pre-turn snapshots entirely (existing snapshots stay restorable). The
	// pointer keeps "absent" distinguishable from "false" (memory pattern).
	Enabled *bool `json:"enabled,omitempty"`
	// Max bounds the per-session snapshot ring; the oldest snapshots are
	// pruned beyond it. Default 50.
	Max int `json:"max,omitempty"`
}

// ApplyDefaults fills zero values with defaults. Call after loading config.
// Explicitly negative values are NOT defaulted here - Validate rejects them.
func (c *SnapshotsConfig) ApplyDefaults() {
	if c.Max == 0 {
		c.Max = DefaultSessionSnapshotsMax
	}
}

// Validate checks SnapshotsConfig for sane values.
func (c *SnapshotsConfig) Validate() error {
	if c.Max < 0 {
		return fmt.Errorf("invalid session.snapshots.max %d: must be >= 0", c.Max)
	}
	return nil
}

// SessionSnapshotsEnabled reports whether pre-turn snapshots are on: only an
// explicit enabled=false disables them.
func SessionSnapshotsEnabled(c *Config) bool {
	if c == nil || c.Session.Snapshots.Enabled == nil {
		return true
	}
	return *c.Session.Snapshots.Enabled
}

// SessionSnapshotsMax returns the effective per-session snapshot ring size.
func SessionSnapshotsMax(c *Config) int {
	if c == nil || c.Session.Snapshots.Max <= 0 {
		return DefaultSessionSnapshotsMax
	}
	return c.Session.Snapshots.Max
}

// Memory default constants. The block caps keep prompts lean; the note cap
// bounds the store and is enforced with an actionable error (never silent
// eviction).
const (
	DefaultMemoryMaxPromptChars = 4000
	DefaultMemoryMaxNotes       = 200
)

// MemoryConfig tunes agent-written auto-memory.
type MemoryConfig struct {
	// Enabled tri-state: nil (default) = on; an explicit false removes the
	// remember/forget_memory tools and the session-start injection. The
	// pointer keeps "absent" distinguishable from "false" (system_env
	// pattern).
	Enabled *bool `json:"enabled,omitempty"`
	// MaxPromptChars caps the injected session-start memory block (the
	// truncation notice may exceed it by ~130 chars). Default 4000.
	MaxPromptChars int `json:"max_prompt_chars,omitempty"`
	// MaxNotes bounds the store; remember refuses to add beyond it and
	// names forget_memory as the remedy. Default 200.
	MaxNotes int `json:"max_notes,omitempty"`
}

// ApplyDefaults fills zero values with defaults. Call after loading config.
// Explicitly negative values are NOT defaulted here - Validate rejects them
// so a mistyped config fails loudly instead of silently reverting.
func (c *MemoryConfig) ApplyDefaults() {
	if c.MaxPromptChars == 0 {
		c.MaxPromptChars = DefaultMemoryMaxPromptChars
	}
	if c.MaxNotes == 0 {
		c.MaxNotes = DefaultMemoryMaxNotes
	}
}

// Validate checks MemoryConfig for sane values.
func (c *MemoryConfig) Validate() error {
	if c.MaxPromptChars < 0 {
		return fmt.Errorf("invalid memory.max_prompt_chars %d: must be >= 0", c.MaxPromptChars)
	}
	if c.MaxNotes < 0 {
		return fmt.Errorf("invalid memory.max_notes %d: must be >= 0", c.MaxNotes)
	}
	return nil
}

// MemoryEnabled reports whether agent-written auto-memory is on: only an
// explicit enabled=false disables it.
func MemoryEnabled(c *Config) bool {
	if c == nil || c.Memory.Enabled == nil {
		return true
	}
	return *c.Memory.Enabled
}

// MemoryMaxPromptChars returns the effective injected-block cap.
func MemoryMaxPromptChars(c *Config) int {
	if c == nil || c.Memory.MaxPromptChars <= 0 {
		return DefaultMemoryMaxPromptChars
	}
	return c.Memory.MaxPromptChars
}

// MemoryMaxNotes returns the effective store-size cap.
func MemoryMaxNotes(c *Config) int {
	if c == nil || c.Memory.MaxNotes <= 0 {
		return DefaultMemoryMaxNotes
	}
	return c.Memory.MaxNotes
}

// SleepConfig tunes one SkillOpt-Sleep night. Defaults (documented per
// field) keep the loop conservative: redaction always on, no evidence log,
// single-group nights, no auto-adoption. redact_secrets is the one field
// that can never be set to false from this file: disabling redaction
// requires the explicit --allow-unredacted CLI flag at invocation time
// (plan SL-004: file-only-never).
type SleepConfig struct {
	// ModelBackend/ModelModel override the optimizer/target model; empty
	// uses the configured provider stack. JudgeBackend/JudgeModel select a
	// SEPARATE judge model (H2); empty resolves to the cheap secondary
	// (summary_model) so miner/judge/optimizer are not silently the same
	// model.
	ModelBackend string `json:"model_backend,omitempty"`
	ModelModel   string `json:"model_model,omitempty"`
	JudgeBackend string `json:"judge_backend,omitempty"`
	JudgeModel   string `json:"judge_model,omitempty"`
	// EditBudget caps applied edits per skill per night (learning rate).
	// Default 4.
	EditBudget int `json:"edit_budget,omitempty"`
	// GateMetric projects (hard, soft) to one comparison: hard | soft |
	// mixed (default mixed). MixedWeight is the soft weight (default 0.5).
	GateMetric  string  `json:"gate_metric,omitempty"`
	MixedWeight float64 `json:"gate_mixed_weight,omitempty"`
	// GateNoRegression blocks acceptance when ANY val task regresses.
	// Default false (mean-based gate).
	GateNoRegression bool `json:"gate_no_regression,omitempty"`
	// Caps (plan SL-021/SL-022 defaults): MaxTasksPerNight 40,
	// MaxSessionsPerNight 120, PerTaskTimeoutSeconds 120,
	// PerNightTimeoutSeconds 3600, MaxToolCallsPerTask 50,
	// MaxTokensPerNight 2000000 (estimated).
	MaxTasksPerNight       int `json:"max_tasks_per_night,omitempty"`
	MaxSessionsPerNight    int `json:"max_sessions_per_night,omitempty"`
	PerTaskTimeoutSeconds  int `json:"per_task_timeout_seconds,omitempty"`
	PerNightTimeoutSeconds int `json:"per_night_timeout_seconds,omitempty"`
	MaxToolCallsPerTask    int `json:"max_tool_calls_per_task,omitempty"`
	MaxTokensPerNight      int `json:"max_tokens_per_night,omitempty"`
	// LookbackHours is the first-run harvest window (default 72; later
	// nights use the state checkpoint).
	LookbackHours int `json:"lookback_hours,omitempty"`
	// LLMMine enables the LLM miner over redacted digests (default false:
	// heuristic miner).
	LLMMine bool `json:"llm_mine,omitempty"`
	// Split fractions: TrainFraction/ValFraction/TestFraction must sum to
	// 1 (defaults 0.8/0.2/0 - train takes everything the legacy empty test
	// slice does not).
	TrainFraction float64 `json:"train_fraction,omitempty"`
	ValFraction   float64 `json:"val_fraction,omitempty"`
	TestFraction  float64 `json:"test_fraction,omitempty"`
	// SplitSeed drives the deterministic shuffle (default 42).
	SplitSeed int64 `json:"split_seed,omitempty"`
	// EvolveSkill is the master switch for skill consolidation (default
	// true when a night runs). EvolveMemory reserves the memory-trial
	// surface (Phase 3; ignored today).
	EvolveSkill  bool `json:"evolve_skill,omitempty"`
	EvolveMemory bool `json:"evolve_memory,omitempty"`
	// RecallK and DreamFactor plumb Phase 3 recall/dream consolidation
	// (defaults 0 and 0: off). RecallK recalls top-k knowledge notes into
	// the reflector context; DreamRollouts + DreamFactor > 0 synthesizes
	// contrastive dream tasks quarantined to the train slice. FanOut
	// enables per-skill consolidation in one night (default false: single
	// largest group).
	RecallK       int     `json:"recall_k,omitempty"`
	DreamFactor   float64 `json:"dream_factor,omitempty"`
	DreamRollouts bool    `json:"dream_rollouts,omitempty"`
	FanOut        bool    `json:"fan_out,omitempty"`
	// Learning-rate schedule (Phase 3, SL-030): LRScheduler constant|
	// linear|cosine decays EditBudget toward LRFloor across LRHorizon
	// nights (epoch = nights recorded in the sleep state). Defaults:
	// constant / floor 0 / horizon 0 (no decay).
	LRScheduler string `json:"lr_scheduler,omitempty"`
	LRFloor     int    `json:"lr_floor,omitempty"`
	LRHorizon   int    `json:"lr_horizon,omitempty"`
	// SkillAwareReflection enables SKILL_DEFECT vs EXECUTION_LAPSE routing
	// (Phase 3, SL-032): lapse reminders land in the protected appendix,
	// bypassing the gate by design. Default off.
	SkillAwareReflection bool `json:"skill_aware_reflection,omitempty"`
	// AutoAdopt installs accepted proposals for managed skills only (M5);
	// hand-written skills always stage for explicit `sleep adopt`.
	// Default false.
	AutoAdopt bool `json:"auto_adopt,omitempty"`
	// EvidenceLog enables outputs/sleep evidence.jsonl (default false:
	// kill switch stays off until redaction matrix tests pass in CI).
	EvidenceLog bool `json:"evidence_log,omitempty"`
	// RedactSecrets is accepted in the file for forward compatibility ONLY
	// as the true default; loaders must refuse redact_secrets:false
	// (SL-004: file-only-never). Not consumed directly by the cycle.
	RedactSecrets *bool `json:"redact_secrets,omitempty"`
	// SkillRoots adds extra skill discovery roots for group resolution
	// (disambiguates cross-root collisions with --skill-root at CLI level).
	SkillRoots []string `json:"skill_roots,omitempty"`
	// IncludeAuditArgs/IncludeAuditOutputs join audit-log detail into
	// digests (default false: tool names only). The audit log records no
	// outputs today; IncludeAuditOutputs reserves the key.
	IncludeAuditArgs    bool `json:"include_audit_args,omitempty"`
	IncludeAuditOutputs bool `json:"include_audit_outputs,omitempty"`
}

// ChannelsConfig tunes the communication-channel subsystem. Absent values are
// the secure defaults: everything off, deny-by-default pairing.
type ChannelsConfig struct {
	// EnableCronScheduler starts the background cron scheduler in web/serve
	// mode (normally TUI-only, so scheduled jobs only fire while the terminal
	// is open). Set true when running headless with channels so scheduled jobs
	// actually fire - e.g. to deliver cron results to Telegram.
	EnableCronScheduler bool `json:"enable_cron_scheduler,omitempty"`
	// Telegram configures the Telegram bot channel. See TelegramChannelConfig.
	Telegram TelegramChannelConfig `json:"telegram,omitempty"`
}

// TelegramChannelConfig configures the Telegram bot transport. Follows the
// SidekickConfig idiom: Enabled is a *bool so "absent" stays distinguishable
// from "false", and the feature is off unless explicitly enabled with a token.
type TelegramChannelConfig struct {
	// Enabled toggles the Telegram channel. nil/absent = disabled.
	Enabled *bool `json:"enabled,omitempty"`
	// BotToken is the bot token from @BotFather. May also come from the
	// HAKASE_TELEGRAM_BOT_TOKEN environment variable (env wins).
	BotToken string `json:"bot_token,omitempty"`
	// AllowedUserIDs statically allowlists Telegram numeric user IDs
	// (deny-by-default; use e.g. @userinfobot to find yours). When empty,
	// users pair at runtime via `/start <code>` with the pairing code printed
	// on the server console (or `hakase channels pair-code`).
	AllowedUserIDs []int64 `json:"allowed_user_ids,omitempty"`
	// PairingCode optionally fixes a static pairing code for scripted setups
	// instead of the generated rotating code. Stored plaintext, like api_key.
	PairingCode string `json:"pairing_code,omitempty"`
	// Pins pins the user's prompt message for the duration of each Telegram
	// run and unpins it at completion (Hermes-style turn marker). Default off.
	Pins bool `json:"pins,omitempty"`
	// SpeechToText configures local voice-note transcription via whisper.cpp
	// (docs/telegram-voice/spec.md, issue #19). Off unless explicitly enabled.
	SpeechToText TelegramSTTConfig `json:"speech_to_text,omitempty"`
	// TextToSpeech configures optional local voice-note replies via Piper
	// (the seam ships; transport wiring is the stretch phase). Off by default.
	TextToSpeech TelegramTTSConfig `json:"text_to_speech,omitempty"`
}

// Default values for the Telegram speech blocks.
const (
	// DefaultTelegramSTTModel is the quantized multilingual whisper.cpp
	// model (~58 MiB, auto-detects language).
	DefaultTelegramSTTModel = "base-q5_1"
	// DefaultTelegramSTTMaxSeconds caps accepted voice-note length.
	DefaultTelegramSTTMaxSeconds = 120
	// DefaultTelegramSTTTimeout bounds one transcription.
	DefaultTelegramSTTTimeout = 180
	// DefaultTelegramTTSMaxChars caps voice-note reply text length.
	DefaultTelegramTTSMaxChars = 1200
)

// TelegramSTTConfig configures local whisper.cpp voice-note transcription.
type TelegramSTTConfig struct {
	// Enabled turns voice-note transcription on. nil/absent = disabled
	// (voice notes then get an actionable setup hint instead of a run).
	Enabled *bool `json:"enabled,omitempty"`
	// Model is the whisper.cpp ggml model name (without ggml- prefix/.bin
	// suffix), e.g. base-q5_1, small-q5_1, tiny. Default base-q5_1; the
	// model file auto-downloads from HuggingFace on first transcription.
	Model string `json:"model,omitempty"`
	// Language is "auto" (detect) or an ISO code like en/de/zh. Default auto.
	Language string `json:"language,omitempty"`
	// BinaryPath is the whisper-cli binary. Default "whisper-cli" on PATH.
	BinaryPath string `json:"binary_path,omitempty"`
	// FFMpegPath is the ffmpeg binary (OGG/Opus → 16 kHz WAV). Default
	// "ffmpeg" on PATH.
	FFMpegPath string `json:"ffmpeg_path,omitempty"`
	// ModelsDir holds ggml model files. Default ~/.hakase/models/whisper.
	ModelsDir string `json:"models_dir,omitempty"`
	// ModelURLBase overrides the model download base URL (mirrors/offline).
	ModelURLBase string `json:"model_url_base,omitempty"`
	// MaxSeconds refuses voice notes longer than this. Default 120.
	MaxSeconds int `json:"max_seconds,omitempty"`
	// TimeoutSeconds bounds one transcription. Default 180.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ApplyDefaults fills zero values with defaults. Call after load.
func (c *TelegramSTTConfig) ApplyDefaults() {
	if c.Model == "" {
		c.Model = DefaultTelegramSTTModel
	}
	if c.Language == "" {
		c.Language = "auto"
	}
	if c.MaxSeconds == 0 {
		c.MaxSeconds = DefaultTelegramSTTMaxSeconds
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = DefaultTelegramSTTTimeout
	}
}

// Validate checks the STT block for sane values.
func (c *TelegramSTTConfig) Validate() error {
	if c.MaxSeconds < 0 {
		return fmt.Errorf("channels.telegram.speech_to_text.max_seconds %d: must be >= 0", c.MaxSeconds)
	}
	if c.TimeoutSeconds < 0 {
		return fmt.Errorf("channels.telegram.speech_to_text.timeout_seconds %d: must be >= 0", c.TimeoutSeconds)
	}
	return nil
}

// TelegramTTSConfig configures optional local Piper voice-note replies.
type TelegramTTSConfig struct {
	// Enabled turns voice-reply synthesis on (the transport wiring is the
	// stretch phase — the config is accepted now for forward stability).
	Enabled *bool `json:"enabled,omitempty"`
	// BinaryPath is the piper CLI. Default "piper" on PATH, with "piper-tts"
	// (e.g. Arch piper-tts-bin) resolved as a fallback name.
	BinaryPath string `json:"binary_path,omitempty"`
	// Voices maps language codes to .onnx voice files, plus the RESERVED
	// "default" key: the voice for typed prompts (no language known) and
	// the fallback for languages without an entry or with a missing file.
	// Example: {"default": ".../en_US-amy-medium.onnx", "de":
	// ".../de_DE-thorsten-medium.onnx"} — a German voice note is then
	// answered with German speech. Required when enabled.
	Voices map[string]string `json:"voices,omitempty"`
	// FFMpegPath is the ffmpeg binary (WAV → OGG/Opus). Default "ffmpeg".
	FFMpegPath string `json:"ffmpeg_path,omitempty"`
	// MaxChars caps voice-note reply text length. Default 1200.
	MaxChars int `json:"max_chars,omitempty"`
}

// validLangKey guards the voices-map keys: language codes plus the
// reserved "default".
var validLangKey = regexp.MustCompile(`^(default|[a-z]{2,3}(-[a-z0-9]{1,16})?)$`)

// ApplyDefaults fills zero values with defaults. Call after load.
func (c *TelegramTTSConfig) ApplyDefaults() {
	if c.MaxChars == 0 {
		c.MaxChars = DefaultTelegramTTSMaxChars
	}
}

// Validate checks the TTS block for sane values: map keys must be language
// codes or the reserved "default", and paths must be non-empty. A missing
// "default" entry with enabled:true is NOT a load error — it degrades at
// runtime like any other missing tooling (voice replies answer with an
// actionable hint), matching the speech_to_text posture.
func (c *TelegramTTSConfig) Validate() error {
	if c.MaxChars < 0 {
		return fmt.Errorf("channels.telegram.text_to_speech.max_chars %d: must be >= 0", c.MaxChars)
	}
	for lang, path := range c.Voices {
		if !validLangKey.MatchString(lang) {
			return fmt.Errorf("channels.telegram.text_to_speech.voices: invalid key %q (want a language code like en/de/ar, or \"default\")", lang)
		}
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("channels.telegram.text_to_speech.voices: key %q has an empty path", lang)
		}
	}
	return nil
}

// ApplyDefaults fills zero values with channel defaults. Call after load.
func (c *ChannelsConfig) ApplyDefaults() {
	c.Telegram.ApplyDefaults()
}

// Validate checks ChannelsConfig. Like SidekickConfig, it only errors when a
// channel is explicitly enabled but unusable, so a misconfigured block fails
// fast instead of silently staying off.
func (c *ChannelsConfig) Validate() error {
	return c.Telegram.Validate()
}

// ApplyDefaults normalizes the Telegram channel config (speech sub-block
// defaults; issue #19).
func (c *TelegramChannelConfig) ApplyDefaults() {
	c.SpeechToText.ApplyDefaults()
	c.TextToSpeech.ApplyDefaults()
}

// Validate errors when the Telegram channel is explicitly enabled without a
// bot token, or when a speech sub-block carries negative bounds. Disabled/
// absent configs are always valid.
func (c *TelegramChannelConfig) Validate() error {
	if c == nil || c.Enabled == nil || !*c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.BotToken) == "" {
		return fmt.Errorf("channels.telegram: enabled but bot_token is empty (set bot_token or HAKASE_TELEGRAM_BOT_TOKEN, or disable with enabled:false)")
	}
	if err := c.SpeechToText.Validate(); err != nil {
		return err
	}
	return c.TextToSpeech.Validate()
}

// EnabledWithToken reports whether the Telegram channel should actually start:
// explicitly enabled AND carrying a non-empty token. This is the single
// source of truth consumed by the web bootstrap and tests.
func (c *TelegramChannelConfig) EnabledWithToken() bool {
	return c != nil && c.Enabled != nil && *c.Enabled && strings.TrimSpace(c.BotToken) != ""
}

// MediaConfig configures pluggable media generation providers.
type MediaConfig struct {
	ImageProvider      string   `json:"image_provider,omitempty"`
	VideoProvider      string   `json:"video_provider,omitempty"`
	AudioProvider      string   `json:"audio_provider,omitempty"`
	Order              []string `json:"order,omitempty"`
	MaxConcurrent      int      `json:"max_concurrent,omitempty"`
	TimeoutSeconds     int      `json:"timeout_seconds,omitempty"`
	OutputDir          string   `json:"output_dir,omitempty"`
	FalKey             string   `json:"fal_key,omitempty"`
	FalBaseURL         string   `json:"fal_base_url,omitempty"`
	FalImageModel      string   `json:"fal_image_model,omitempty"`
	FalVideoModel      string   `json:"fal_video_model,omitempty"`
	OpenAIImageKey     string   `json:"openai_image_key,omitempty"`
	OpenAIImageBaseURL string   `json:"openai_image_base_url,omitempty"`
	OpenAIImagePath    string   `json:"openai_image_path,omitempty"`
	OpenAIImageModel   string   `json:"openai_image_model,omitempty"`
	// OpenAI-compatible video generation (async jobs API, e.g. OpenRouter
	// /api/v1/videos). Key/base fall back to the image fields, then to the
	// global api_key/base_url.
	OpenAIVideoKey        string `json:"openai_video_key,omitempty"`
	OpenAIVideoBaseURL    string `json:"openai_video_base_url,omitempty"`
	OpenAIVideoModel      string `json:"openai_video_model,omitempty"`
	OpenAIVideoResolution string `json:"openai_video_resolution,omitempty"`
}

// SidekickConfig tunes the optional "sidekick" agent: a second, independently
// configured LLM that runs alongside the primary orchestrator. It has two
// capabilities: a side-process (user-initiated ask_sidekick tool + /sidekick
// command) and a consult/watchdog (observes the current run and injects quiet
// advisory notes). See docs/sidekick-agent/. Absent/zero values fall back to
// the defaults in the accessors below, so disabling requires only enabled:false
// or an empty model_name.
type SidekickConfig struct {
	// Enabled toggles the whole sidekick feature. nil/absent = disabled, which
	// keeps the default (off) and avoids starting any sidekick model. A
	// pointer keeps "absent" distinguishable from "false".
	Enabled *bool `json:"enabled,omitempty"`
	// Mode selects the sidekick behavior: "off" (default when disabled or unset),
	// "on_demand" (side-process only, no watchdog), "watch" (watchdog only, no
	// side-process), "full" (both). Empty when enabled falls back to "on_demand"
	// per the Phase 0 decision (Q1).
	Mode string `json:"mode,omitempty"`
	// Provider is the sidekick LLM provider: "gemini", "openai", or
	// "openai-compatible". Empty = reuse the primary provider (mirrors vision).
	Provider string `json:"provider,omitempty"`
	// ModelName names the sidekick model. Empty = provider default (and the
	// feature is forced off, because the sidekick cannot run without a model).
	ModelName string `json:"model_name,omitempty"`
	// BaseURL optionally overrides the endpoint for the sidekick model.
	BaseURL string `json:"base_url,omitempty"`
	// APIKey optionally overrides the API key for the sidekick model.
	APIKey string `json:"api_key,omitempty"`
	// EvaluateDebounceSeconds spaces watchdog evaluations during a run.
	// 0 uses defaultSidekickDebounceSeconds (20).
	EvaluateDebounceSeconds int `json:"evaluate_debounce_seconds,omitempty"`
	// MaxEvaluationsPerRun caps watchdog evaluations per run (anti-runaway).
	// 0 uses defaultSidekickMaxEvals (5).
	MaxEvaluationsPerRun int `json:"max_evaluations_per_run,omitempty"`
	// MaxNotesPerTurn caps advisory notes emitted per watchdog turn.
	// 0 uses defaultSidekickMaxNotes (2).
	MaxNotesPerTurn int `json:"max_notes_per_turn,omitempty"`
	// MaxNoteChars caps each advisory note's rendered length. 0 uses
	// defaultSidekickNoteChars (1200).
	MaxNoteChars int `json:"max_note_chars,omitempty"`
	// TranscriptWindowChars bounds the run-transcript text sent to the
	// watchdog. 0 uses defaultSidekickWindow (6000).
	TranscriptWindowChars int `json:"transcript_window_chars,omitempty"`
}

// Sidekick default constants.
const (
	defaultSidekickDebounceSeconds = 20
	defaultSidekickMaxEvals        = 5
	defaultSidekickMaxNotes        = 2
	defaultSidekickNoteChars       = 1200
	defaultSidekickWindow          = 6000
)

// Sidekick mode constants.
const (
	ModeOff      = "off"       // disabled
	ModeOnDemand = "on_demand" // ask_sidekick only (default when enabled)
	ModeWatch    = "watch"     // watchdog consults, notes injected into context
	ModeFull     = "full"      // watch + orchestrator told to act on notes
)

// validSidekickModes is the set of recognized Mode values.
var validSidekickModes = map[string]bool{
	ModeOff:      true,
	ModeOnDemand: true,
	ModeWatch:    true,
	ModeFull:     true,
}

// ApplyDefaults fills zero values with sidekick defaults. Call after load.
func (c *SidekickConfig) ApplyDefaults() {
	if c.EvaluateDebounceSeconds <= 0 {
		c.EvaluateDebounceSeconds = defaultSidekickDebounceSeconds
	}
	if c.MaxEvaluationsPerRun <= 0 {
		c.MaxEvaluationsPerRun = defaultSidekickMaxEvals
	}
	if c.MaxNotesPerTurn <= 0 {
		c.MaxNotesPerTurn = defaultSidekickMaxNotes
	}
	if c.MaxNoteChars <= 0 {
		c.MaxNoteChars = defaultSidekickNoteChars
	}
	if c.TranscriptWindowChars <= 0 {
		c.TranscriptWindowChars = defaultSidekickWindow
	}
}

// EffectiveMode computes the active sidekick mode from the config, honoring
// the Phase 0 decision (Q1): disabled OR empty model_name forces "off"; an
// empty mode on an enabled-with-model config falls back to "on_demand"; an
// unrecognized mode falls back to "off".
func (c *SidekickConfig) EffectiveMode() string {
	if c == nil || c.Enabled == nil || !*c.Enabled {
		return "off"
	}
	if strings.TrimSpace(c.ModelName) == "" {
		return "off"
	}
	mode := strings.TrimSpace(c.Mode)
	if mode == "" {
		return "on_demand"
	}
	if !validSidekickModes[mode] {
		return "off"
	}
	return mode
}

// EnabledWithModel reports whether the sidekick is both enabled and has a model
// (i.e. EffectiveMode() != "off").
func (c *SidekickConfig) EnabledWithModel() bool {
	return c.EffectiveMode() != "off"
}

// Validate checks SidekickConfig for valid values. It only errors when the
// feature is explicitly enabled (per Q1: an enabled config must name a model
// and a valid mode), so a misconfigured enabled block fails fast rather than
// silently disabling.
func (c *SidekickConfig) Validate() error {
	if c == nil || c.Enabled == nil || !*c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.ModelName) == "" {
		return fmt.Errorf("sidekick: enabled but model_name is empty (set model_name or disable with enabled:false)")
	}
	mode := strings.TrimSpace(c.Mode)
	if mode != "" && !validSidekickModes[mode] {
		return fmt.Errorf("sidekick: invalid mode %q: must be one of off, on_demand, watch, full", mode)
	}
	switch strings.TrimSpace(c.Provider) {
	case "", "gemini", "openai", "openai-compatible":
	default:
		return fmt.Errorf("sidekick: invalid provider %q: must be gemini, openai, or openai-compatible", c.Provider)
	}
	return nil
}

// SidekickEnabled reports whether the sidekick feature is enabled at all.
func SidekickEnabled(cfg *Config) bool {
	return cfg != nil && cfg.Sidekick.EnabledWithModel()
}

// WebSearchConfig tunes the built-in keyless web search fallback
// (internal/websearch) exposed when no research-capable MCP server is
// connected.
type WebSearchConfig struct {
	// Enabled tri-state: nil (default) = auto, where detection in
	// internal/agent decides visibility from the connected MCP tools;
	// false = never expose the fallback or make outbound search calls.
	Enabled *bool `json:"enabled,omitempty"`
	// Force keeps the fallback tools visible even when research-capable MCP
	// tools are connected (testing, or preferring the lightweight path).
	Force bool `json:"force,omitempty"`
}

// WebSearchEnabled reports whether the fallback feature may run at all: only
// an explicit enabled=false disables it.
func WebSearchEnabled(c *Config) bool {
	if c == nil || c.WebSearch.Enabled == nil {
		return true
	}
	return *c.WebSearch.Enabled
}

// ApplyDefaults fills zero values with defaults. Call after loading config.
func (c *MediaConfig) ApplyDefaults() {
	if c.ImageProvider == "" {
		c.ImageProvider = "auto"
	}
	if c.VideoProvider == "" {
		c.VideoProvider = "auto"
	}
	if c.AudioProvider == "" {
		c.AudioProvider = "off"
	}
	if len(c.Order) == 0 {
		c.Order = []string{"openai", "fal", "pil"}
	}
	if c.OutputDir == "" {
		c.OutputDir = "outputs/media"
	}
	if c.OpenAIImagePath == "" {
		c.OpenAIImagePath = "/images/generations"
	}
	if c.OpenAIImageModel == "" {
		c.OpenAIImageModel = "gpt-image-1-mini"
	}
	if c.FalImageModel == "" {
		c.FalImageModel = "fal-ai/flux/schnell"
	}
	if c.FalVideoModel == "" {
		c.FalVideoModel = "fal-ai/wan/v2.7/text-to-video"
	}
	// Cheapest confirmed OpenRouter video model (2026-08): veo-3.1-lite at
	// $0.03/s @720p with generate_audio=false; durations 4/6/8s.
	if c.OpenAIVideoModel == "" {
		c.OpenAIVideoModel = "google/veo-3.1-lite"
	}
	if c.OpenAIVideoResolution == "" {
		c.OpenAIVideoResolution = "720p"
	}
}

// Validate checks MediaConfig for valid values.
func (c *MediaConfig) Validate() error {
	validImage := map[string]bool{"auto": true, "pil": true, "openai": true, "fal": true, "off": true}
	if !validImage[c.ImageProvider] {
		return fmt.Errorf("invalid media.image_provider %q: must be one of auto, pil, openai, fal, off", c.ImageProvider)
	}
	validVideo := map[string]bool{"auto": true, "openai": true, "fal": true, "off": true}
	if !validVideo[c.VideoProvider] {
		return fmt.Errorf("invalid media.video_provider %q: must be one of auto, openai, fal, off", c.VideoProvider)
	}
	validAudio := map[string]bool{"off": true, "openai": true, "elevenlabs": true}
	if !validAudio[c.AudioProvider] {
		return fmt.Errorf("invalid media.audio_provider %q: must be one of off, openai, elevenlabs", c.AudioProvider)
	}
	if c.MaxConcurrent < 0 {
		return fmt.Errorf("invalid media.max_concurrent %d: must be >= 0", c.MaxConcurrent)
	}
	if c.TimeoutSeconds < 0 {
		return fmt.Errorf("invalid media.timeout_seconds %d: must be >= 0", c.TimeoutSeconds)
	}
	return nil
}

// envConfigSet reports whether any HAKASE_* environment override is present.
// LoadConfig uses it to build a config purely from the environment when the
// config file is missing.
func envConfigSet() bool {
	return os.Getenv("HAKASE_API_KEY") != "" ||
		os.Getenv("HAKASE_PROVIDER") != "" ||
		os.Getenv("HAKASE_MODEL") != "" ||
		os.Getenv("HAKASE_BASE_URL") != "" ||
		os.Getenv("HAKASE_SUMMARY_MODEL") != "" ||
		os.Getenv("HAKASE_VISION_MODEL") != "" ||
		os.Getenv("HAKASE_VISION_BASE_URL") != "" ||
		os.Getenv("HAKASE_VISION_API_KEY") != "" ||
		os.Getenv("HAKASE_VISION_PROVIDER") != "" ||
		os.Getenv("HAKASE_MODEL_VISION") != "" ||
		os.Getenv("HAKASE_MEDIA_IMAGE_PROVIDER") != "" ||
		os.Getenv("HAKASE_MEDIA_VIDEO_PROVIDER") != "" ||
		os.Getenv("HAKASE_MEDIA_OUTPUT_DIR") != "" ||
		os.Getenv("HAKASE_FAL_KEY") != "" ||
		os.Getenv("HAKASE_SIDEKICK_ENABLED") != "" ||
		os.Getenv("HAKASE_SIDEKICK_MODE") != "" ||
		os.Getenv("HAKASE_SIDEKICK_PROVIDER") != "" ||
		os.Getenv("HAKASE_SIDEKICK_MODEL") != "" ||
		os.Getenv("HAKASE_SIDEKICK_BASE_URL") != "" ||
		os.Getenv("HAKASE_SIDEKICK_API_KEY") != "" ||
		os.Getenv("HAKASE_TELEGRAM_ENABLED") != "" ||
		os.Getenv("HAKASE_TELEGRAM_BOT_TOKEN") != "" ||
		os.Getenv("HAKASE_MEMORY_ENABLED") != "" ||
		os.Getenv("HAKASE_MEMORY_MAX_PROMPT_CHARS") != "" ||
		os.Getenv("HAKASE_MEMORY_MAX_NOTES") != "" ||
		os.Getenv("HAKASE_TRACING_ENABLED") != "" ||
		os.Getenv("HAKASE_TRACING_ENDPOINT") != "" ||
		os.Getenv("HAKASE_TRACING_SAMPLE_RATIO") != "" ||
		os.Getenv("HAKASE_TRACING_HEADERS") != "" ||
		os.Getenv("HAKASE_SESSION_SNAPSHOTS_ENABLED") != "" ||
		os.Getenv("HAKASE_SESSION_SNAPSHOTS_MAX") != "" ||
		os.Getenv("HAKASE_TELEGRAM_STT_ENABLED") != "" ||
		os.Getenv("HAKASE_TELEGRAM_TTS_ENABLED") != ""
}

// HakaseHome returns the user-level hakase home directory: $HAKASE_HOME when
// set (mirroring how other harnesses honor a config-dir override), otherwise
// ~/.hakase (Claude-style user home). Returns "" when no home directory can
// be determined. This is the canonical location for user-level hakase state:
// config.json, skills/, and (optionally) a user-global knowledge base.
func HakaseHome() string {
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hakase")
}

// ResolveConfigPath returns the config file to load: the local config.json in
// the working directory when present (project wins), otherwise the user-level
// <HakaseHome>/config.json when present, otherwise the local path unchanged
// so LoadConfig keeps its existing "missing file" error behavior.
func ResolveConfigPath(local string) string {
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if home := HakaseHome(); home != "" {
		userCfg := filepath.Join(home, "config.json")
		if _, err := os.Stat(userCfg); err == nil {
			return userCfg
		}
	}
	return local
}

// removedConfigKeys names top-level config.json keys that were deleted from
// the schema because they promised behavior that was never implemented.
// LoadConfig refuses them loudly (instead of silently ignoring) so a stale
// config fails fast with a pointer at the removal.
var removedConfigKeys = map[string]string{
	"env_overrides": "the docker/ssh environment isolation it described was parsed but never implemented (no code ever created those environments)",
}

// rejectRemovedKeys errors when raw config JSON still carries a removed key.
func rejectRemovedKeys(filePath string, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // malformed JSON: let the typed unmarshal below report it
	}
	for key, why := range removedConfigKeys {
		if _, ok := raw[key]; ok {
			return fmt.Errorf("config: %s was removed (%s); remove the %s block from %s", key, why, key, filePath)
		}
	}
	return nil
}

// parseEnvBool parses a boolean HAKASE_* environment override. Accepted
// forms are 1/0, true/false, and yes/no, case-insensitive; anything else is
// a configuration error. The old per-site coercion (anything unrecognized
// meant false) silently ignored typos like "ture" and disabled the very
// feature the variable was meant to enable.
func parseEnvBool(name, v string) (bool, error) {
	switch {
	case v == "1", strings.EqualFold(v, "true"), strings.EqualFold(v, "yes"):
		return true, nil
	case v == "0", strings.EqualFold(v, "false"), strings.EqualFold(v, "no"):
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean value %q (accepted: 1/0, true/false, yes/no)", name, v)
	}
}

// parseEnvPositiveInt parses a numeric HAKASE_* environment override. The
// value must be a positive integer; anything else is a configuration error
// instead of a silent fall-back to the config-file or default value.
func parseEnvPositiveInt(name, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer value %q", name, v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s: must be a positive integer, got %d", name, n)
	}
	return n, nil
}

// parseEnvRatio parses a float HAKASE_* override that must land in [0,1]
// (sampling ratios). NaN and out-of-range values are configuration errors,
// same strict policy as the other numeric parsers.
func parseEnvRatio(name, v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid float value %q", name, v)
	}
	if math.IsNaN(f) || f < 0 || f > 1 {
		return 0, fmt.Errorf("%s: must be within [0,1], got %v", name, f)
	}
	return f, nil
}

// parseEnvHeaders parses a comma-separated K=V header list for
// HAKASE_TRACING_HEADERS. Entries without "=" are load errors; an empty
// value is allowed (some vendors use bare keys).
func parseEnvHeaders(name, v string) (map[string]string, error) {
	headers := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		k, val, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s: invalid header entry %q (expected K=V)", name, pair)
		}
		headers[strings.TrimSpace(k)] = val
	}
	return headers, nil
}

// LoadConfig reads the JSON config file and applies HAKASE_* environment
// overrides on top. Environment variables win over file values. Boolean and
// numeric overrides share one strict parsing policy (parseEnvBool and
// parseEnvPositiveInt): an invalid value is a load error that names the
// variable, never a silent fallback. When the file is missing, config can
// still come entirely from the environment; only when neither a file nor any
// env var is present is the file error returned.
func LoadConfig(filePath string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(filePath)
	switch {
	case err == nil:
		if err := rejectRemovedKeys(filePath, data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
	case envConfigSet():
	default:
		return nil, err
	}

	if v := os.Getenv("HAKASE_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("HAKASE_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("HAKASE_MODEL"); v != "" {
		cfg.ModelName = v
	}
	if v := os.Getenv("HAKASE_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("HAKASE_SUMMARY_MODEL"); v != "" {
		cfg.SummaryModel = v
	}
	if v := os.Getenv("HAKASE_VISION_MODEL"); v != "" {
		cfg.VisionModel = v
	}
	if v := os.Getenv("HAKASE_VISION_BASE_URL"); v != "" {
		cfg.VisionBaseURL = v
	}
	if v := os.Getenv("HAKASE_VISION_API_KEY"); v != "" {
		cfg.VisionAPIKey = v
	}
	if v := os.Getenv("HAKASE_VISION_PROVIDER"); v != "" {
		cfg.VisionProvider = v
	}
	if v := os.Getenv("HAKASE_MODEL_VISION"); v != "" {
		cfg.ModelVision = v
	}
	if v := os.Getenv("HAKASE_SEARCH_EXPANSION"); v != "" {
		b, err := parseEnvBool("HAKASE_SEARCH_EXPANSION", v)
		if err != nil {
			return nil, err
		}
		cfg.SearchExpansion = b
	}
	if v := os.Getenv("HAKASE_DEBUG"); v != "" {
		b, err := parseEnvBool("HAKASE_DEBUG", v)
		if err != nil {
			return nil, err
		}
		cfg.Debug = b
	}
	if v := os.Getenv("HAKASE_MAX_OUTPUT_TOKENS"); v != "" {
		n, err := parseEnvPositiveInt("HAKASE_MAX_OUTPUT_TOKENS", v)
		if err != nil {
			return nil, err
		}
		if n > math.MaxInt32 {
			return nil, fmt.Errorf("HAKASE_MAX_OUTPUT_TOKENS: %d exceeds the field maximum (%d)", n, int64(math.MaxInt32))
		}
		cfg.LoopGuard.MaxOutputTokens = int32(n)
	}
	if v := os.Getenv("HAKASE_MEDIA_IMAGE_PROVIDER"); v != "" {
		cfg.Media.ImageProvider = v
	}
	if v := os.Getenv("HAKASE_MEDIA_VIDEO_PROVIDER"); v != "" {
		cfg.Media.VideoProvider = v
	}
	if v := os.Getenv("HAKASE_MEDIA_OUTPUT_DIR"); v != "" {
		cfg.Media.OutputDir = v
	}
	if v := os.Getenv("HAKASE_FAL_KEY"); v != "" {
		cfg.Media.FalKey = v
	}
	if v := os.Getenv("HAKASE_MEDIA_VIDEO_MODEL"); v != "" {
		cfg.Media.OpenAIVideoModel = v
	}

	// Sidekick env overrides (mirrors the vision pattern).
	if v := os.Getenv("HAKASE_SIDEKICK_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_SIDEKICK_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Sidekick.Enabled = &b
	}
	if v := os.Getenv("HAKASE_SIDEKICK_MODE"); v != "" {
		cfg.Sidekick.Mode = v
	}
	if v := os.Getenv("HAKASE_SIDEKICK_PROVIDER"); v != "" {
		cfg.Sidekick.Provider = v
	}
	if v := os.Getenv("HAKASE_SIDEKICK_MODEL"); v != "" {
		cfg.Sidekick.ModelName = v
	}
	if v := os.Getenv("HAKASE_SIDEKICK_BASE_URL"); v != "" {
		cfg.Sidekick.BaseURL = v
	}
	if v := os.Getenv("HAKASE_SIDEKICK_API_KEY"); v != "" {
		cfg.Sidekick.APIKey = v
	}
	cfg.Sidekick.ApplyDefaults()
	if err := cfg.Sidekick.Validate(); err != nil {
		return nil, err
	}

	// Telegram channel env overrides (mirrors the sidekick pattern).
	if v := os.Getenv("HAKASE_TELEGRAM_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_TELEGRAM_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Channels.Telegram.Enabled = &b
	}
	if v := os.Getenv("HAKASE_TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Channels.Telegram.BotToken = v
	}
	if v := os.Getenv("HAKASE_TELEGRAM_STT_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_TELEGRAM_STT_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Channels.Telegram.SpeechToText.Enabled = &b
	}
	if v := os.Getenv("HAKASE_TELEGRAM_TTS_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_TELEGRAM_TTS_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Channels.Telegram.TextToSpeech.Enabled = &b
	}
	cfg.Channels.ApplyDefaults()
	if err := cfg.Channels.Validate(); err != nil {
		return nil, err
	}

	// Memory env overrides (mirrors the sidekick pattern).
	if v := os.Getenv("HAKASE_MEMORY_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_MEMORY_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Memory.Enabled = &b
	}
	if v := os.Getenv("HAKASE_MEMORY_MAX_PROMPT_CHARS"); v != "" {
		n, err := parseEnvPositiveInt("HAKASE_MEMORY_MAX_PROMPT_CHARS", v)
		if err != nil {
			return nil, err
		}
		cfg.Memory.MaxPromptChars = n
	}
	if v := os.Getenv("HAKASE_MEMORY_MAX_NOTES"); v != "" {
		n, err := parseEnvPositiveInt("HAKASE_MEMORY_MAX_NOTES", v)
		if err != nil {
			return nil, err
		}
		cfg.Memory.MaxNotes = n
	}
	cfg.Memory.ApplyDefaults()
	if err := cfg.Memory.Validate(); err != nil {
		return nil, err
	}

	// Tracing env overrides (mirrors the sidekick pattern).
	if v := os.Getenv("HAKASE_TRACING_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_TRACING_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Tracing.Enabled = b
	}
	if v := os.Getenv("HAKASE_TRACING_ENDPOINT"); v != "" {
		cfg.Tracing.Endpoint = v
	}
	if v := os.Getenv("HAKASE_TRACING_SAMPLE_RATIO"); v != "" {
		f, err := parseEnvRatio("HAKASE_TRACING_SAMPLE_RATIO", v)
		if err != nil {
			return nil, err
		}
		cfg.Tracing.SampleRatio = f
	}
	if v := os.Getenv("HAKASE_TRACING_HEADERS"); v != "" {
		h, err := parseEnvHeaders("HAKASE_TRACING_HEADERS", v)
		if err != nil {
			return nil, err
		}
		cfg.Tracing.Headers = h
	}
	cfg.Tracing.ApplyDefaults()
	if err := cfg.Tracing.Validate(); err != nil {
		return nil, err
	}

	// Session snapshot env overrides (mirrors the memory pattern).
	if v := os.Getenv("HAKASE_SESSION_SNAPSHOTS_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_SESSION_SNAPSHOTS_ENABLED", v)
		if err != nil {
			return nil, err
		}
		cfg.Session.Snapshots.Enabled = &b
	}
	if v := os.Getenv("HAKASE_SESSION_SNAPSHOTS_MAX"); v != "" {
		n, err := parseEnvPositiveInt("HAKASE_SESSION_SNAPSHOTS_MAX", v)
		if err != nil {
			return nil, err
		}
		cfg.Session.Snapshots.Max = n
	}
	cfg.Session.Snapshots.ApplyDefaults()
	if err := cfg.Session.Snapshots.Validate(); err != nil {
		return nil, err
	}

	// Issue #14: landlock mode is reserved but unimplemented - refuse at
	// config load instead of silently degrading to path-auditing-only exec.
	// LoadSandboxConfig normalizes the roots so Validate sees the effective
	// mode; a nil sandbox block means confinement disabled (valid).
	if cfg.Sandbox != nil {
		if err := sandbox.ValidateSandboxConfig(sandbox.LoadSandboxConfig(cfg.Sandbox)); err != nil {
			return nil, err
		}
	}

	cfg.Media.ApplyDefaults()
	// Fallback chain for OpenAI image provider (mirrors vision pattern):
	// openai_image_key empty -> cfg.APIKey, openai_image_base_url empty -> cfg.BaseURL
	if cfg.Media.OpenAIImageKey == "" && cfg.APIKey != "" {
		cfg.Media.OpenAIImageKey = cfg.APIKey
	}
	if cfg.Media.OpenAIImageBaseURL == "" && cfg.BaseURL != "" {
		cfg.Media.OpenAIImageBaseURL = cfg.BaseURL
	}

	return &cfg, nil
}

// SystemEnvEnabled reports whether the runtime-environment block should be
// injected. Absent config or absent `enabled` field means enabled (default);
// an explicit enabled:false opts out.
func SystemEnvEnabled(cfg *Config) bool {
	if cfg == nil || cfg.SystemEnv.Enabled == nil {
		return true
	}
	return *cfg.SystemEnv.Enabled
}

// DefaultModelForProvider returns the default model name for a provider. An
// empty or "gemini" provider uses Gemini's default; "openai" uses OpenAI's
// default. "openai-compatible" endpoints have no universal default - the model
// name is endpoint-specific (Ollama, vLLM, etc.), so it returns an empty string
// and the caller must configure one explicitly. This is the single source of
// truth for provider defaults, shared with the agent package and the web API so
// the UI does not have to recompute them.
func DefaultModelForProvider(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.6-terra"
	case "openai-compatible":
		return ""
	default:
		return "gemini-3.7-flash"
	}
}

// EffectiveModelName returns the model the agent will actually use: the
// configured ModelName when set, otherwise the provider's default. Trims
// surrounding whitespace so a stray space in config does not leak into labels.
func (c *Config) EffectiveModelName() string {
	if name := strings.TrimSpace(c.ModelName); name != "" {
		return name
	}
	return DefaultModelForProvider(c.Provider)
}

// EffectiveUnitsSystem normalizes the configured units system, defaulting to
// "metric" (SI/ISO) when unset or invalid. Only "imperial" selects imperial.
func EffectiveUnitsSystem(cfg *Config) string {
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Units.System), "imperial") {
		return "imperial"
	}
	return "metric"
}

// MarshalJSON implements json.Marshaler. It redacts sensitive map values in
// MCPServerConfig (Env, Headers) using the has_api_key pattern: each value is
// replaced with "true" when the key exists, so the caller sees key presence
// without the actual secret. The original Config is not mutated.
func (c Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	alias := Alias(c)
	if alias.MCPServers.Servers != nil {
		redacted := make(map[string]*MCPServerConfig, len(alias.MCPServers.Servers))
		for name, srv := range alias.MCPServers.Servers {
			copy := *srv
			copy.Env = redactMap(copy.Env)
			copy.Headers = redactMap(copy.Headers)
			redacted[name] = &copy
		}
		alias.MCPServers = MCPConfig{Servers: redacted}
	}
	return json.Marshal(alias)
}

// redactMap returns a copy of m with every value replaced by "true" (the
// has_api_key pattern: shows key presence without revealing the actual value).
// nil maps return nil.
func redactMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = "true"
	}
	return out
}

// SystemEnvMaxChars returns the configured block cap, falling back to the
// default when unset.
func SystemEnvMaxChars(cfg *Config) int {
	if cfg != nil && cfg.SystemEnv.MaxChars > 0 {
		return cfg.SystemEnv.MaxChars
	}
	return DefaultSystemEnvMaxChars
}
