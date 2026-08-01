package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes content to a fresh temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return path
}

func TestLoadConfigFile(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "openai",
		"model_name": "gpt-4o-mini",
		"api_key": "file-key",
		"base_url": "https://example.com/v1",
		"instruction": "test instruction",
		"mcp_server_url": "http://localhost:9223/mcp",
		"fallback_providers": ["gemini"],
		"provider_options": {"timeout": 30}
	}`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider: expected %q, got %q", "openai", cfg.Provider)
	}
	if cfg.ModelName != "gpt-4o-mini" {
		t.Errorf("ModelName: expected %q, got %q", "gpt-4o-mini", cfg.ModelName)
	}
	if cfg.APIKey != "file-key" {
		t.Errorf("APIKey: expected %q, got %q", "file-key", cfg.APIKey)
	}
	if cfg.BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL: expected %q, got %q", "https://example.com/v1", cfg.BaseURL)
	}
	if cfg.Instruction != "test instruction" {
		t.Errorf("Instruction: expected %q, got %q", "test instruction", cfg.Instruction)
	}
	if cfg.MCPServerURL != "http://localhost:9223/mcp" {
		t.Errorf("MCPServerURL: expected %q, got %q", "http://localhost:9223/mcp", cfg.MCPServerURL)
	}
	if len(cfg.FallbackProviders) != 1 || cfg.FallbackProviders[0] != "gemini" {
		t.Errorf("FallbackProviders: expected [gemini], got %v", cfg.FallbackProviders)
	}
	if len(cfg.ProviderOptions) != 1 {
		t.Fatalf("ProviderOptions: expected 1 entry, got %v", cfg.ProviderOptions)
	}
	if v, ok := cfg.ProviderOptions["timeout"]; !ok || v.(float64) != 30 {
		t.Errorf("ProviderOptions[timeout]: expected 30, got %v", cfg.ProviderOptions["timeout"])
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "gemini",
		"model_name": "gemini-file",
		"api_key": "file-key",
		"base_url": "https://file.example.com/v1"
	}`)

	t.Setenv("HAKASE_API_KEY", "env-key")
	t.Setenv("HAKASE_PROVIDER", "openai")
	t.Setenv("HAKASE_MODEL", "gpt-4o-mini")
	t.Setenv("HAKASE_BASE_URL", "https://env.example.com/v1")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}

	if cfg.APIKey != "env-key" {
		t.Errorf("APIKey: env should win, expected %q, got %q", "env-key", cfg.APIKey)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider: env should win, expected %q, got %q", "openai", cfg.Provider)
	}
	if cfg.ModelName != "gpt-4o-mini" {
		t.Errorf("ModelName: env should win, expected %q, got %q", "gpt-4o-mini", cfg.ModelName)
	}
	if cfg.BaseURL != "https://env.example.com/v1" {
		t.Errorf("BaseURL: env should win, expected %q, got %q", "https://env.example.com/v1", cfg.BaseURL)
	}
}

func TestLoadConfigEnvOverridePartial(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "gemini",
		"model_name": "gemini-file",
		"api_key": "file-key"
	}`)

	t.Setenv("HAKASE_PROVIDER", "openai")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider: env should win, expected %q, got %q", "openai", cfg.Provider)
	}
	if cfg.APIKey != "file-key" {
		t.Errorf("APIKey: expected file value %q, got %q", "file-key", cfg.APIKey)
	}
	if cfg.ModelName != "gemini-file" {
		t.Errorf("ModelName: expected file value %q, got %q", "gemini-file", cfg.ModelName)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Setenv("HAKASE_API_KEY", "")
	t.Setenv("HAKASE_PROVIDER", "")
	t.Setenv("HAKASE_MODEL", "")
	t.Setenv("HAKASE_BASE_URL", "")

	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("loadConfig: expected error for missing file with no env vars, got nil")
	}
}

func TestLoadConfigEnvOnly(t *testing.T) {
	t.Setenv("HAKASE_API_KEY", "env-key")
	t.Setenv("HAKASE_PROVIDER", "openai")

	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: expected config from env when file missing, got error: %v", err)
	}
	if cfg.APIKey != "env-key" {
		t.Errorf("APIKey: expected %q, got %q", "env-key", cfg.APIKey)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider: expected %q, got %q", "openai", cfg.Provider)
	}
}
