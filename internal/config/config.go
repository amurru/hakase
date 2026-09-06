package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

type EnvOverrideConfig struct {
	DockerImage   string `json:"docker_image,omitempty"`
	ModalImage    string `json:"modal_image,omitempty"`
	EnvType       string `json:"env_type,omitempty"` // "local", "docker", "ssh"
	CPULimit      int    `json:"cpu_limit,omitempty"`
	MemoryLimitMB int    `json:"memory_limit_mb,omitempty"`
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
	Units UnitsConfig `json:"units,omitempty"`
	MCPServerURL string             `json:"mcp_server_url"`
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
	// EnvOverrides maps task IDs or agent names to environment
	// isolation configurations for delegated sub-agents.
	EnvOverrides map[string]EnvOverrideConfig `json:"env_overrides,omitempty"`
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
	// Channels configures communication channels (Telegram bot, extensible to
	// other chat transports) that let a remote client prompt the agent, watch
	// progress, answer approvals, and manage tasks/cron jobs. Channels run
	// inside the web/serve process and are off unless explicitly enabled.
	Channels ChannelsConfig `json:"channels,omitempty"`
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

// ApplyDefaults normalizes the Telegram channel config (currently a no-op
// hook kept for symmetry with the other config sections).
func (c *TelegramChannelConfig) ApplyDefaults() {}

// Validate errors when the Telegram channel is explicitly enabled without a
// bot token. Disabled/absent configs are always valid.
func (c *TelegramChannelConfig) Validate() error {
	if c == nil || c.Enabled == nil || !*c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.BotToken) == "" {
		return fmt.Errorf("channels.telegram: enabled but bot_token is empty (set bot_token or HAKASE_TELEGRAM_BOT_TOKEN, or disable with enabled:false)")
	}
	return nil
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
	ModeOff       = "off"       // disabled
	ModeOnDemand  = "on_demand" // ask_sidekick only (default when enabled)
	ModeWatch     = "watch"     // watchdog consults, notes injected into context
	ModeFull      = "full"      // watch + orchestrator told to act on notes
)

// validSidekickModes is the set of recognized Mode values.
var validSidekickModes = map[string]bool{
	ModeOff:       true,
	ModeOnDemand:  true,
	ModeWatch:     true,
	ModeFull:      true,
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
		os.Getenv("HAKASE_TELEGRAM_BOT_TOKEN") != ""
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

// LoadConfig reads the JSON config file and applies HAKASE_* environment
// overrides on top. Environment variables win over file values. When the file
// is missing, config can still come entirely from the environment; only when
// neither a file nor any env var is present is the file error returned.
func LoadConfig(filePath string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(filePath)
	switch {
	case err == nil:
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
		cfg.SearchExpansion = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("HAKASE_DEBUG"); v != "" {
		cfg.Debug = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("HAKASE_MAX_OUTPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.LoopGuard.MaxOutputTokens = int32(n)
		}
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
		b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
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
		b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
		cfg.Channels.Telegram.Enabled = &b
	}
	if v := os.Getenv("HAKASE_TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Channels.Telegram.BotToken = v
	}
	cfg.Channels.ApplyDefaults()
	if err := cfg.Channels.Validate(); err != nil {
		return nil, err
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
