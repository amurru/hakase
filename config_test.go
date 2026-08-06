package main

import (
	"bytes"
	"encoding/json"
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

func TestLoadConfigSkillDirs(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "openai",
		"model_name": "gpt-4o-mini",
		"api_key": "file-key",
		"skill_dirs": ["./custom-skills", "/abs/path"]
	}`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}

	if len(cfg.SkillDirs) != 2 {
		t.Fatalf("SkillDirs: expected 2 entries, got %v", cfg.SkillDirs)
	}
	if cfg.SkillDirs[0] != "./custom-skills" {
		t.Errorf("SkillDirs[0]: expected %q, got %q", "./custom-skills", cfg.SkillDirs[0])
	}
	if cfg.SkillDirs[1] != "/abs/path" {
		t.Errorf("SkillDirs[1]: expected %q, got %q", "/abs/path", cfg.SkillDirs[1])
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

func TestLoadConfigSummaryModel(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "gemini",
		"model_name": "gemini-2.5-flash",
		"api_key": "file-key",
		"summary_model": "gemini-2.5-flash-lite"
	}`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}
	if cfg.SummaryModel != "gemini-2.5-flash-lite" {
		t.Errorf("SummaryModel = %q, want %q", cfg.SummaryModel, "gemini-2.5-flash-lite")
	}

	// Env override wins over the file.
	t.Setenv("HAKASE_SUMMARY_MODEL", "gpt-4o-mini")
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: unexpected error: %v", err)
	}
	if cfg.SummaryModel != "gpt-4o-mini" {
		t.Errorf("SummaryModel env override = %q, want %q", cfg.SummaryModel, "gpt-4o-mini")
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

func TestHakaseHome(t *testing.T) {
	t.Setenv("HAKASE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := hakaseHome(); got != filepath.Join(home, ".hakase") {
		t.Errorf("hakaseHome: expected %q, got %q", filepath.Join(home, ".hakase"), got)
	}

	// $HAKASE_HOME overrides the default ~/.hakase.
	override := t.TempDir()
	t.Setenv("HAKASE_HOME", override)
	if got := hakaseHome(); got != override {
		t.Errorf("hakaseHome with HAKASE_HOME: expected %q, got %q", override, got)
	}
}

func TestResolveConfigPath(t *testing.T) {
	t.Setenv("HAKASE_HOME", "")

	// Local config.json wins when present.
	localDir := t.TempDir()
	local := filepath.Join(localDir, "config.json")
	if err := os.WriteFile(local, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}
	if got := resolveConfigPath(local); got != local {
		t.Errorf("resolveConfigPath with local present: expected %q, got %q", local, got)
	}

	// User-level ~/.hakase/config.json is used when the local file is missing.
	home := t.TempDir()
	t.Setenv("HOME", home)
	userCfg := filepath.Join(home, ".hakase", "config.json")
	if err := os.MkdirAll(filepath.Dir(userCfg), 0o755); err != nil {
		t.Fatalf("mkdir ~/.hakase: %v", err)
	}
	if err := os.WriteFile(userCfg, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "config.json")
	if got := resolveConfigPath(missing); got != userCfg {
		t.Errorf("resolveConfigPath with user fallback: expected %q, got %q", userCfg, got)
	}

	// Neither exists: the local path is returned unchanged so loadConfig
	// keeps its existing missing-file error behavior.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	nowhere := filepath.Join(t.TempDir(), "nope.json")
	if got := resolveConfigPath(nowhere); got != nowhere {
		t.Errorf("resolveConfigPath with nothing present: expected %q, got %q", nowhere, got)
	}
}

func TestResolveConfigPathEnvOverride(t *testing.T) {
	// $HAKASE_HOME redirects the user-level fallback.
	override := t.TempDir()
	t.Setenv("HAKASE_HOME", override)
	userCfg := filepath.Join(override, "config.json")
	if err := os.WriteFile(userCfg, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "config.json")
	if got := resolveConfigPath(missing); got != userCfg {
		t.Errorf("resolveConfigPath with HAKASE_HOME: expected %q, got %q", userCfg, got)
	}
}

// TestConfigExampleFileValid guards the committed config.json.example: it must
// parse into the Config struct without unknown keys (encoding/json silently
// ignores typos, so a strict decode catches config drift) and its MCP servers
// must pass the same validation the runtime applies. Users copy this file
// verbatim, so it must never reference a field that no longer exists.
func TestConfigExampleFileValid(t *testing.T) {
	data, err := os.ReadFile("config.json.example")
	if err != nil {
		t.Fatalf("reading config.json.example: %v", err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("config.json.example has keys that do not map to Config fields: %v", err)
	}

	// The example must also survive the runtime MCP registry validation. Use
	// an isolated HAKASE_HOME so a real ~/.hakase/mcp.json never merges in.
	mcpTestIsolate(t)
	reg, err := LoadMCPRegistry(&cfg)
	if err != nil {
		t.Fatalf("LoadMCPRegistry(config.json.example): %v", err)
	}
	if len(reg.Servers) != 3 {
		t.Fatalf("expected 3 MCP servers (lightpanda, github, remote), got %d", len(reg.Servers))
	}
	lp, ok := reg.Servers["lightpanda"]
	if !ok || lp.Type != "http" || lp.URL != "http://localhost:9223/mcp" {
		t.Fatalf("lightpanda server wrong: %+v", lp)
	}
	gh, ok := reg.Servers["github"]
	if !ok || gh.Type != "stdio" || len(gh.Command) == 0 || gh.Command[0] != "npx" {
		t.Fatalf("github server wrong: %+v", gh)
	}
	rm, ok := reg.Servers["remote"]
	if !ok || rm.Type != "http" || rm.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("remote server wrong: %+v", rm)
	}
}
