package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

// setTestConfigPath points ResolveConfigPath at the given path for the
// duration of the test, restoring the original afterwards.
func setTestConfigPath(t *testing.T, path string) {
	t.Helper()
	orig := resolveConfigPath
	resolveConfigPath = func(local string) string { return path }
	t.Cleanup(func() { resolveConfigPath = orig })
}

// writeTestConfig writes a sample config.json and returns its path.
func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return path
}

func TestGetConfigSanitizesSecrets(t *testing.T) {
	path := writeTestConfig(t, `{
		"provider": "gemini",
		"model_name": "gemini-3.6-flash",
		"api_key": "super-secret-key",
		"base_url": "",
		"knowledge_dir": "./knowledge",
		"mcp": {"servers": {"lightpanda": {"type": "http", "url": "http://localhost:9223/mcp"}}}
	}`)
	setTestConfigPath(t, path)

	handler := (&ConfigAPI{}).GetConfig
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Path != path {
		t.Fatalf("expected path %q, got %q", path, resp.Path)
	}
	if !resp.Writable {
		t.Fatal("expected writable=true for existing config file")
	}
	if !resp.HasAPIKey {
		t.Fatal("expected has_api_key=true")
	}
	if resp.Config.APIKey != "" {
		t.Fatalf("api_key must never be returned, got %q", resp.Config.APIKey)
	}
	if resp.Config.Provider != "gemini" {
		t.Fatalf("expected provider=gemini, got %q", resp.Config.Provider)
	}
	if resp.Config.ModelName != "gemini-3.6-flash" {
		t.Fatalf("expected model_name, got %q", resp.Config.ModelName)
	}
	if resp.EffectiveModel != "gemini-3.6-flash" {
		t.Fatalf("expected effective_model=gemini-3.6-flash, got %q", resp.EffectiveModel)
	}
}

func TestGetConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	setTestConfigPath(t, path)

	handler := (&ConfigAPI{}).GetConfig
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for missing config, got %d: %s", w.Code, w.Body.String())
	}

	var resp ConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Writable {
		t.Fatal("expected writable=false for missing config file")
	}
	if resp.Config.Provider != "" {
		t.Fatalf("expected empty provider, got %q", resp.Config.Provider)
	}
}

func TestGetConfigEffectiveModelResolvesProviderDefault(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		expected string
	}{
		{"openai default", `{"provider": "openai"}`, "gpt-5.6-terra"},
		{"openai-compatible has no default", `{"provider": "openai-compatible", "base_url": "http://localhost:11434/v1"}`, ""},
		{"empty provider defaults to gemini", `{}`, "gemini-3.7-flash"},
		{"explicit model wins", `{"provider": "openai", "model_name": "gpt-4o"}`, "gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestConfig(t, tc.body)
			setTestConfigPath(t, path)

			handler := (&ConfigAPI{}).GetConfig
			req := httptest.NewRequest("GET", "/api/config", nil)
			w := httptest.NewRecorder()
			handler(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var resp ConfigResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if resp.EffectiveModel != tc.expected {
				t.Fatalf("expected effective_model=%q, got %q", tc.expected, resp.EffectiveModel)
			}
		})
	}
}

func TestUpdateConfigMergesAndPreservesUnknownKeys(t *testing.T) {
	path := writeTestConfig(t, `{
		"provider": "gemini",
		"model_name": "gemini-3.6-flash",
		"api_key": "secret",
		"knowledge_dir": "./knowledge",
		"mcp": {"servers": {"lightpanda": {"type": "http", "url": "http://localhost:9223/mcp"}}},
		"custom_field": "keep-me"
	}`)
	setTestConfigPath(t, path)

	body := `{"provider": "openai", "model_name": "gpt-4o-mini"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Re-read the file and verify merge semantics.
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Fatalf("expected provider=openai, got %q", cfg.Provider)
	}
	if cfg.ModelName != "gpt-4o-mini" {
		t.Fatalf("expected model_name=gpt-4o-mini, got %q", cfg.ModelName)
	}
	if cfg.APIKey != "secret" {
		t.Fatalf("api_key should be preserved, got %q", cfg.APIKey)
	}
	if cfg.KnowledgeDir != "./knowledge" {
		t.Fatalf("knowledge_dir should be preserved, got %q", cfg.KnowledgeDir)
	}
	if len(cfg.MCPServers.Servers) == 0 {
		t.Fatal("mcp block should be preserved")
	}

	// Unknown/custom keys must survive a save round-trip.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	var rawMap map[string]interface{}
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if rawMap["custom_field"] != "keep-me" {
		t.Fatalf("custom_field should be preserved, got %v", rawMap["custom_field"])
	}
}

func TestUpdateConfigNestedBlocks(t *testing.T) {
	path := writeTestConfig(t, `{
		"provider": "gemini",
		"sandbox": {"mode": "bubblewrap", "allow_network": false},
		"loop_guard": {"max_output_tokens": 8192}
	}`)
	setTestConfigPath(t, path)

	body := `{
		"sandbox": {"mode": "paths", "allow_network": true},
		"loop_guard": {"repetition_limit": 10}
	}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.Sandbox == nil {
		t.Fatal("sandbox block should exist")
	}
	if cfg.Sandbox.Mode != "paths" {
		t.Fatalf("expected sandbox.mode=paths, got %q", cfg.Sandbox.Mode)
	}
	if !cfg.Sandbox.AllowNetwork {
		t.Fatal("expected sandbox.allow_network=true")
	}
	if cfg.LoopGuard.RepetitionLimit != 10 {
		t.Fatalf("expected repetition_limit=10, got %d", cfg.LoopGuard.RepetitionLimit)
	}
	if cfg.LoopGuard.MaxOutputTokens != 8192 {
		t.Fatalf("expected max_output_tokens preserved at 8192, got %d", cfg.LoopGuard.MaxOutputTokens)
	}
}

func TestUpdateConfigRejectsNonEditableKeys(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini", "mcp_server_url": "http://localhost:9223/mcp"}`)
	setTestConfigPath(t, path)

	body := `{"mcp_server_url": "http://localhost:9999/mcp"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	// mcp is NOT in the editable allowlist - it must be rejected.
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mcp edit, got %d: %s", w.Code, w.Body.String())
	}

	// The file must be unchanged.
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.MCPServerURL != "http://localhost:9223/mcp" {
		t.Fatalf("mcp_server_url should be unchanged, got %q", cfg.MCPServerURL)
	}
}

func TestUpdateConfigSetsAPIKeyWriteOnly(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini"}`)
	setTestConfigPath(t, path)

	body := `{"api_key": "brand-new-secret"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The key must NOT be echoed back in the response.
	if strings.Contains(w.Body.String(), "brand-new-secret") {
		t.Fatalf("response must never echo the api_key value, got: %s", w.Body.String())
	}

	// The key must be persisted to disk.
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.APIKey != "brand-new-secret" {
		t.Fatalf("expected api_key=brand-new-secret, got %q", cfg.APIKey)
	}

	// GET must still never return the key.
	getHandler := (&ConfigAPI{}).GetConfig
	getReq := httptest.NewRequest("GET", "/api/config", nil)
	getW := httptest.NewRecorder()
	getHandler(getW, getReq)
	if strings.Contains(getW.Body.String(), "brand-new-secret") {
		t.Fatalf("GET must never return the api_key value, got: %s", getW.Body.String())
	}
	var resp ConfigResponse
	if err := json.NewDecoder(getW.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}
	if !resp.HasAPIKey {
		t.Fatal("expected has_api_key=true after setting a key")
	}
}

func TestUpdateConfigReplacesAPIKey(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini", "api_key": "old-secret"}`)
	setTestConfigPath(t, path)

	body := `{"api_key": "new-secret"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.APIKey != "new-secret" {
		t.Fatalf("expected api_key replaced with new-secret, got %q", cfg.APIKey)
	}
}

func TestUpdateConfigClearsAPIKey(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini", "api_key": "secret", "vision_api_key": "vsecret"}`)
	setTestConfigPath(t, path)

	body := `{"clear_api_key": true}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.APIKey != "" {
		t.Fatalf("expected api_key cleared, got %q", cfg.APIKey)
	}
	if cfg.VisionAPIKey != "vsecret" {
		t.Fatalf("vision_api_key should be untouched, got %q", cfg.VisionAPIKey)
	}
}

func TestUpdateConfigSetsVisionAPIKey(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini"}`)
	setTestConfigPath(t, path)

	body := `{"vision_api_key": "vision-secret"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.VisionAPIKey != "vision-secret" {
		t.Fatalf("expected vision_api_key=vision-secret, got %q", cfg.VisionAPIKey)
	}
}

func TestUpdateConfigClearsVisionAPIKey(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini", "vision_api_key": "vsecret"}`)
	setTestConfigPath(t, path)

	body := `{"clear_vision_api_key": true}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.VisionAPIKey != "" {
		t.Fatalf("expected vision_api_key cleared, got %q", cfg.VisionAPIKey)
	}
}

func TestUpdateConfigEmptyAPIKeyRejected(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini", "api_key": "secret"}`)
	setTestConfigPath(t, path)

	body := `{"api_key": ""}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty api_key, got %d: %s", w.Code, w.Body.String())
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.APIKey != "secret" {
		t.Fatalf("api_key should be unchanged, got %q", cfg.APIKey)
	}
}

func TestUpdateConfigInvalidProvider(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini"}`)
	setTestConfigPath(t, path)

	body := `{"provider": "not-a-provider"}`
	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid provider, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateConfigInvalidJSON(t *testing.T) {
	path := writeTestConfig(t, `{"provider": "gemini"}`)
	setTestConfigPath(t, path)

	handler := (&ConfigAPI{}).UpdateConfig
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestValidateConfigUpdate(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid provider", `{"provider": "openai"}`, false},
		{"invalid provider", `{"provider": "foo"}`, true},
		{"valid model_vision", `{"model_vision": "auto"}`, false},
		{"invalid model_vision", `{"model_vision": "maybe"}`, true},
		{"valid thinking_level", `{"thinking_level": "high"}`, false},
		{"invalid thinking_level", `{"thinking_level": "uber"}`, true},
		{"valid sandbox mode", `{"sandbox": {"mode": "off"}}`, false},
		{"invalid sandbox mode", `{"sandbox": {"mode": "chroot"}}`, true},
		{"valid approval mode", `{"approval": {"mode": "deny"}}`, false},
		{"invalid approval mode", `{"approval": {"mode": "sometimes"}}`, true},
		{"valid system_env", `{"system_env": {"enabled": true}}`, false},
		{"invalid system_env enabled", `{"system_env": {"enabled": "yes"}}`, true},
		{"valid units metric", `{"units": {"system": "metric"}}`, false},
		{"valid units imperial", `{"units": {"system": "imperial"}}`, false},
		{"units absent system", `{"units": {}}`, false},
		{"invalid units system", `{"units": {"system": "furlongs"}}`, true},
		{"units non-object", `{"units": "imperial"}`, true},
		{"units.system non-string", `{"units": {"system": 123}}`, true},
		{"non-map sandbox", `{"sandbox": "paths"}`, false},
		{"set api_key", `{"api_key": "secret"}`, false},
		{"empty api_key", `{"api_key": ""}`, true},
		{"clear api_key", `{"clear_api_key": true}`, false},
		{"non-bool clear_api_key", `{"clear_api_key": "yes"}`, true},
		{"set vision_api_key", `{"vision_api_key": "secret"}`, false},
		{"empty vision_api_key", `{"vision_api_key": ""}`, true},
		{"clear vision_api_key", `{"clear_vision_api_key": true}`, false},
		{"non-bool clear_vision_api_key", `{"clear_vision_api_key": 1}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req map[string]interface{}
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("bad test body: %v", err)
			}
			err := validateConfigUpdate(req)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
