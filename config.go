package main

import (
	"encoding/json"
	"os"
)

type Config struct {
	Provider          string                 `json:"provider"`
	ModelName         string                 `json:"model_name"`
	APIKey            string                 `json:"api_key"`
	BaseURL           string                 `json:"base_url,omitempty"`
	Instruction       string                 `json:"instruction"`
	MCPServerURL      string                 `json:"mcp_server_url"`
	FallbackProviders []string               `json:"fallback_providers,omitempty"`
	ProviderOptions   map[string]interface{} `json:"provider_options,omitempty"`
	ChatBufferSize    int                    `json:"chat_buffer_size,omitempty"`
	ShowThinking      bool                   `json:"show_thinking,omitempty"`
}

// envConfigSet reports whether any HAKASE_* environment override is present.
// loadConfig uses it to build a config purely from the environment when the
// config file is missing.
func envConfigSet() bool {
	return os.Getenv("HAKASE_API_KEY") != "" ||
		os.Getenv("HAKASE_PROVIDER") != "" ||
		os.Getenv("HAKASE_MODEL") != "" ||
		os.Getenv("HAKASE_BASE_URL") != ""
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

	return &cfg, nil
}
