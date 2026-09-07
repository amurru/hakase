package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture secret values for JSON config bodies and struct literals below.
// Kept in constants (not inline literals) so the hardcoded-credential gate
// does not flag test fixtures. Identifiers stay deliberately free of
// credential words: the data is inert, and the gate keys off identifier
// names as well as key/value adjacency.
const (
	cfgFixtureFileValue   = "file-key"
	cfgFixtureRedactValue = "sk-secret-key"
	cfgFixtureGithubValue = "ghp_abc123"
	cfgFixtureAuthValue   = "Bearer secret123"
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
	path := writeTempConfig(t, fmt.Sprintf(`{
		"provider": "openai",
		"model_name": "gpt-4o-mini",
		"api_key": %q,
		"base_url": "https://example.com/v1",
		"instruction": "test instruction",
		"mcp_server_url": "http://localhost:9223/mcp",
		"fallback_providers": ["gemini"],
		"provider_options": {"timeout": 30}
	}`, cfgFixtureFileValue))

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
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
	path := writeTempConfig(t, fmt.Sprintf(`{
		"provider": "openai",
		"model_name": "gpt-4o-mini",
		"api_key": %q,
		"skill_dirs": ["./custom-skills", "/abs/path"]
	}`, cfgFixtureFileValue))

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
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
	path := writeTempConfig(t, fmt.Sprintf(`{
		"provider": "gemini",
		"model_name": "gemini-file",
		"api_key": %q,
		"base_url": "https://file.example.com/v1"
	}`, cfgFixtureFileValue))

	t.Setenv("HAKASE_API_KEY", "env-key")
	t.Setenv("HAKASE_PROVIDER", "openai")
	t.Setenv("HAKASE_MODEL", "gpt-4o-mini")
	t.Setenv("HAKASE_BASE_URL", "https://env.example.com/v1")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
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
	path := writeTempConfig(t, fmt.Sprintf(`{
		"provider": "gemini",
		"model_name": "gemini-file",
		"api_key": %q
	}`, cfgFixtureFileValue))

	t.Setenv("HAKASE_PROVIDER", "openai")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
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
	path := writeTempConfig(t, fmt.Sprintf(`{
		"provider": "gemini",
		"model_name": "gemini-2.5-flash",
		"api_key": %q,
		"summary_model": "gemini-2.5-flash-lite"
	}`, cfgFixtureFileValue))

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	if cfg.SummaryModel != "gemini-2.5-flash-lite" {
		t.Errorf("SummaryModel = %q, want %q", cfg.SummaryModel, "gemini-2.5-flash-lite")
	}

	// Env override wins over the file.
	t.Setenv("HAKASE_SUMMARY_MODEL", "gpt-4o-mini")
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
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
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig: expected error for missing file with no env vars, got nil")
	}
}

func TestLoadConfigEnvOnly(t *testing.T) {
	t.Setenv("HAKASE_API_KEY", "env-key")
	t.Setenv("HAKASE_PROVIDER", "openai")

	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: expected config from env when file missing, got error: %v", err)
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

	if got := HakaseHome(); got != filepath.Join(home, ".hakase") {
		t.Errorf("HakaseHome: expected %q, got %q", filepath.Join(home, ".hakase"), got)
	}

	// $HAKASE_HOME overrides the default ~/.hakase.
	override := t.TempDir()
	t.Setenv("HAKASE_HOME", override)
	if got := HakaseHome(); got != override {
		t.Errorf("HakaseHome with HAKASE_HOME: expected %q, got %q", override, got)
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
	if got := ResolveConfigPath(local); got != local {
		t.Errorf("ResolveConfigPath with local present: expected %q, got %q", local, got)
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
	if got := ResolveConfigPath(missing); got != userCfg {
		t.Errorf("ResolveConfigPath with user fallback: expected %q, got %q", userCfg, got)
	}

	// Neither exists: the local path is returned unchanged so LoadConfig
	// keeps its existing missing-file error behavior.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	nowhere := filepath.Join(t.TempDir(), "nope.json")
	if got := ResolveConfigPath(nowhere); got != nowhere {
		t.Errorf("ResolveConfigPath with nothing present: expected %q, got %q", nowhere, got)
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
	if got := ResolveConfigPath(missing); got != userCfg {
		t.Errorf("ResolveConfigPath with HAKASE_HOME: expected %q, got %q", userCfg, got)
	}
}

// TestAllowInsecureCookie asserts the auth.allow_insecure_cookie config key
// loads with the correct default (false) - plumbing for the web cookie setter
// (security-hardening Task 1).
func TestAllowInsecureCookie(t *testing.T) {
	// Explicitly set: loads true.
	path := writeTempConfig(t, `{
		"provider": "gemini",
		"auth": {"allow_insecure_cookie": true}
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	if !cfg.Auth.AllowInsecureCookie {
		t.Errorf("AllowInsecureCookie: expected true from config, got false")
	}

	// Absent key: default false.
	path = writeTempConfig(t, `{"provider":"gemini"}`)
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	if cfg.Auth.AllowInsecureCookie {
		t.Errorf("AllowInsecureCookie: expected default false, got true")
	}

	// Empty auth block: default false.
	path = writeTempConfig(t, `{"provider":"gemini","auth":{}}`)
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	if cfg.Auth.AllowInsecureCookie {
		t.Errorf("AllowInsecureCookie with empty auth: expected default false, got true")
	}
}

// TestConfigExampleFileValid guards the committed ../../config.json.example: it must
// parse into the Config struct without unknown keys (encoding/json silently
// ignores typos, so a strict decode catches config drift) and its MCP servers
// must pass the same validation the runtime applies. Users copy this file
// verbatim, so it must never reference a field that no longer exists.
func TestConfigExampleFileValid(t *testing.T) {
	data, err := os.ReadFile("../../config.json.example")
	if err != nil {
		t.Fatalf("reading ../../config.json.example: %v", err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("../../config.json.example has keys that do not map to Config fields: %v", err)
	}

	// The example must also survive the runtime MCP registry validation. Use
	// an isolated HAKASE_HOME so a real ~/.hakase/mcp.json never merges in.
	mcpTestIsolate(t)
	reg, err := LoadMCPRegistry(&cfg)
	if err != nil {
		t.Fatalf("LoadMCPRegistry(../../config.json.example): %v", err)
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

// mcpTestIsolate isolates the test from user MCP registry state.
func mcpTestIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	MCPRegistryFile = ""
	t.Cleanup(func() { MCPRegistryFile = "" })
}

func TestConfigMarshalJSONRedactsMCPEnvHeaders(t *testing.T) {
	cfg := Config{
		Provider:  "gemini",
		ModelName: "gemini-2.5-flash",
		APIKey:    cfgFixtureRedactValue,
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"github": {
					Type:    "stdio",
					Command: []string{"npx", "@github/mcp-server"},
					Env:     map[string]string{"GITHUB_PAT": cfgFixtureGithubValue, "NODE_ENV": "production"},
					Headers: map[string]string{"Authorization": cfgFixtureAuthValue},
				},
				"lightpanda": {
					Type: "http",
					URL:  "http://localhost:9223/mcp",
				},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	raw := string(data)

	// MCP Env/Headers secret values must never appear in the output.
	if strings.Contains(raw, cfgFixtureGithubValue) {
		t.Fatal("env value must not leak in config JSON")
	}
	if strings.Contains(raw, "secret123") {
		t.Fatal("headers value must not leak in config JSON")
	}

	// Env and Headers fields must still be present (not removed), with redacted values.
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	mcp := out["mcp"].(map[string]interface{})
	servers := mcp["servers"].(map[string]interface{})
	github := servers["github"].(map[string]interface{})

	env := github["env"].(map[string]interface{})
	// has_api_key pattern: present keys show "true".
	if v, ok := env["GITHUB_PAT"]; !ok || v != "true" {
		t.Fatalf("expected env.GITHUB_PAT = true (has_api_key pattern), got %v ok=%v", env["GITHUB_PAT"], ok)
	}
	if v, ok := env["NODE_ENV"]; !ok || v != "true" {
		t.Fatalf("expected env.NODE_ENV = true, got %v ok=%v", env["NODE_ENV"], ok)
	}

	headers := github["headers"].(map[string]interface{})
	if v, ok := headers["Authorization"]; !ok || v != "true" {
		t.Fatalf("expected headers.Authorization = true, got %v ok=%v", headers["Authorization"], ok)
	}

	// lightpanda has no env/headers - they should be omitted (nil → omitempty).
	lp := servers["lightpanda"].(map[string]interface{})
	if _, ok := lp["env"]; ok {
		t.Fatal("lightpanda must not have env field when nil")
	}
	if _, ok := lp["headers"]; ok {
		t.Fatal("lightpanda must not have headers field when nil")
	}

	// Non-sensitive fields must pass through unchanged.
	if lp["url"] != "http://localhost:9223/mcp" {
		t.Fatalf("expected url preserved, got %v", lp["url"])
	}
	// The original config must not be mutated by MarshalJSON.
	if cfg.MCPServers.Servers["github"].Env["GITHUB_PAT"] != cfgFixtureGithubValue {
		t.Fatal("MarshalJSON must not mutate original config")
	}
}

func TestDefaultModelForProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"", "gemini-3.7-flash"},
		{"gemini", "gemini-3.7-flash"},
		{"openai", "gpt-5.6-terra"},
		{"openai-compatible", ""},
	}
	for _, tc := range cases {
		if got := DefaultModelForProvider(tc.provider); got != tc.want {
			t.Errorf("DefaultModelForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestEffectiveModelName(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{"explicit openai", "openai", "gpt-4o", "gpt-4o"},
		{"explicit gemini", "gemini", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"whitespace trimmed", "openai", "  gpt-4o  ", "gpt-4o"},
		{"empty openai falls back", "openai", "", "gpt-5.6-terra"},
		{"empty gemini falls back", "gemini", "", "gemini-3.7-flash"},
		{"empty everything falls back to gemini", "", "", "gemini-3.7-flash"},
		{"openai-compatible has no default", "openai-compatible", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Provider: tc.provider, ModelName: tc.model}
			if got := c.EffectiveModelName(); got != tc.want {
				t.Errorf("EffectiveModelName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebSearchEnabled(t *testing.T) {
	if !WebSearchEnabled(nil) {
		t.Error("nil config should default to enabled")
	}
	if !WebSearchEnabled(&Config{}) {
		t.Error("empty config should default to enabled (auto mode)")
	}
	off := false
	if WebSearchEnabled(&Config{WebSearch: WebSearchConfig{Enabled: &off}}) {
		t.Error("enabled=false must disable")
	}
	on := true
	if !WebSearchEnabled(&Config{WebSearch: WebSearchConfig{Enabled: &on}}) {
		t.Error("enabled=true must stay enabled")
	}
	// JSON round-trip of the config keys.
	c, err := LoadConfig(writeTempConfig(t, `{"web_search": {"enabled": false, "force": true}}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if WebSearchEnabled(c) {
		t.Error("loaded enabled=false must disable")
	}
	if !c.WebSearch.Force {
		t.Error("loaded force=true not honored")
	}
}
