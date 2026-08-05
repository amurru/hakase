package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

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

type Config struct {
	Provider          string                 `json:"provider"`
	ModelName         string                 `json:"model_name"`
	APIKey            string                 `json:"api_key"`
	BaseURL           string                 `json:"base_url,omitempty"`
	Instruction       string                 `json:"instruction"`
	MCPServerURL      string                 `json:"mcp_server_url"`
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
	Sandbox *SandboxJSON `json:"sandbox,omitempty"`
	// LoopGuard enables anti-degeneration guardrails (max output cap, repetition
	// and no-tool-call watchdogs). Absent/zero values use loopguard.go defaults.
	LoopGuard LoopGuardConfig `json:"loop_guard,omitempty"`
	// Approval tunes the interactive approval gate for harmful-command
	// protection. Absent/zero values use defaults (interactive mode, 60s expiry).
	Approval ApprovalConfig `json:"approval,omitempty"`
}

// envConfigSet reports whether any HAKASE_* environment override is present.
// loadConfig uses it to build a config purely from the environment when the
// config file is missing.
func envConfigSet() bool {
	return os.Getenv("HAKASE_API_KEY") != "" ||
		os.Getenv("HAKASE_PROVIDER") != "" ||
		os.Getenv("HAKASE_MODEL") != "" ||
		os.Getenv("HAKASE_BASE_URL") != "" ||
		os.Getenv("HAKASE_SUMMARY_MODEL") != ""
}

// loadConfig reads the JSON config file and applies HAKASE_* environment
// overrides on top. Environment variables win over file values. When the file
// is missing, config can still come entirely from the environment; only when
// neither a file nor any env var is present is the file error returned.
func loadConfig(filePath string) (*Config, error) {
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
	if v := os.Getenv("HAKASE_DEBUG"); v != "" {
		cfg.Debug = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("HAKASE_MAX_OUTPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.LoopGuard.MaxOutputTokens = int32(n)
		}
	}

	return &cfg, nil
}
