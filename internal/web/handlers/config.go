// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"amurru/hakase/internal/config"
)

// ConfigRouter is the minimum interface needed by RegisterConfigRoutes.
type ConfigRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Put(pattern string, handlerFn http.HandlerFunc)
}

// resolveConfigPath resolves the config file path. It is a var so tests can
// redirect it to a temp file.
var resolveConfigPath = func(local string) string {
	return config.ResolveConfigPath(local)
}

// ConfigAPI exposes the config.json file for reading and editing over HTTP.
type ConfigAPI struct{}

// RegisterConfigRoutes registers the config API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterConfigRoutes(r ConfigRouter) {
	api := &ConfigAPI{}
	r.Get("/config", api.GetConfig)
	r.Put("/config", api.UpdateConfig)
}

// ConfigResponse is the sanitized config returned to the web UI. API keys are
// never returned - only a boolean flag reporting whether one is set.
type ConfigResponse struct {
	// Path is the config file the server reads/writes (resolved via
	// config.ResolveConfigPath, so it may be a project or user-level file).
	Path string `json:"path"`
	// Writable reports whether the resolved config file exists on disk. When
	// false (config came entirely from environment variables) the UI shows a
	// read-only notice.
	Writable bool `json:"writable"`
	// HasAPIKey reports whether an api_key is configured (the value itself is
	// never returned).
	HasAPIKey bool `json:"has_api_key"`
	// HasVisionAPIKey reports whether a vision_api_key is configured.
	HasVisionAPIKey bool `json:"has_vision_api_key"`
	HasFalKey         bool `json:"has_fal_key"`
	HasOpenAIImageKey bool `json:"has_openai_image_key"`
	HasOpenAIVideoKey bool `json:"has_openai_video_key"`
	// HasTelegramBotToken / HasTelegramPairingCode report channel secrets
	// (values never returned). HasSidekickAPIKey reports the sidekick key.
	HasTelegramBotToken     bool `json:"has_telegram_bot_token"`
	HasTelegramPairingCode  bool `json:"has_telegram_pairing_code"`
	HasSidekickAPIKey       bool `json:"has_sidekick_api_key"`
	// EffectiveModel is the model the agent will actually use, resolved from the
	// configured model_name or the provider default. Exposed so the web UI can
	// label the active model without recomputing provider defaults client-side.
	EffectiveModel string `json:"effective_model"`
	// Config holds the sanitized config values (api keys blanked).
	Config config.Config `json:"config"`
}

// GetConfig handles GET /api/config. It returns the current config file
// contents with API keys blanked, so the web UI can render an editor without
// ever receiving a secret.
func (api *ConfigAPI) GetConfig(w http.ResponseWriter, r *http.Request) {
	path := resolveConfigPath("config.json")

	cfg, err := config.LoadConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No file: return a fresh zero config (config built from env).
			cfg = &config.Config{}
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load config: " + err.Error()})
			return
		}
	}

	hasKey := cfg.APIKey != ""
	hasVisionKey := cfg.VisionAPIKey != ""
	hasFalKey := cfg.Media.FalKey != ""
	hasOpenAIImageKey := cfg.Media.OpenAIImageKey != ""
	hasOpenAIVideoKey := cfg.Media.OpenAIVideoKey != ""
	hasTelegramToken := cfg.Channels.Telegram.BotToken != ""
	hasTelegramPairingCode := cfg.Channels.Telegram.PairingCode != ""
	hasSidekickKey := cfg.Sidekick.APIKey != ""
	// Blank secrets before returning.
	cfg.APIKey = ""
	cfg.VisionAPIKey = ""
	cfg.Media.FalKey = ""
	cfg.Media.OpenAIImageKey = ""
	cfg.Media.OpenAIVideoKey = ""
	cfg.Channels.Telegram.BotToken = ""
	cfg.Channels.Telegram.PairingCode = ""
	cfg.Sidekick.APIKey = ""

	_, statErr := os.Stat(path)
	writable := statErr == nil

	writeJSON(w, http.StatusOK, ConfigResponse{
		Path:                   path,
		Writable:               writable,
		HasAPIKey:              hasKey,
		HasVisionAPIKey:        hasVisionKey,
		HasFalKey:              hasFalKey,
		HasOpenAIImageKey:      hasOpenAIImageKey,
		HasOpenAIVideoKey:      hasOpenAIVideoKey,
		HasTelegramBotToken:    hasTelegramToken,
		HasTelegramPairingCode: hasTelegramPairingCode,
		HasSidekickAPIKey:      hasSidekickKey,
		EffectiveModel:         cfg.EffectiveModelName(),
		Config:                 *cfg,
	})
}

// editableConfigKeys is the allowlist of top-level config.json keys the web UI
// may edit. Everything else in the file (mcp servers, provider_options,
// env_overrides, unknown/custom keys) is preserved untouched on save.
var editableConfigKeys = []string{
	"provider",
	"model_name",
	"base_url",
	"instruction",
	"knowledge_dir",
	"summary_model",
	"vision_model",
	"vision_base_url",
	"vision_provider",
	"model_vision",
	"thinking_level",
	"fallback_providers",
	"skill_dirs",
	"chat_buffer_size",
	"task_checkpoint",
	"search_expansion",
	"debug",
	"show_thinking",
	"system_env",
	"context_files",
	"units",
	"sandbox",
	"loop_guard",
	"approval",
	"clarify",
	"media",
}

// apiKeyControlKeys are write-only keys for managing secrets in config.json.
// Unlike editableConfigKeys their values are never returned by GET and never
// echoed back by PUT. A non-empty string replaces the stored key;
// clear_<key> removes it. This lets the web UI add or remove API keys without
// ever exposing them.
var apiKeyControlKeys = []string{
	"api_key",
	"clear_api_key",
	"vision_api_key",
	"clear_vision_api_key",
	"fal_key",
	"clear_fal_key",
	"openai_image_key",
	"clear_openai_image_key",
	"openai_video_key",
	"clear_openai_video_key",
}

// UpdateConfig handles PUT /api/config. The request body is a partial JSON
// object containing only the keys the user wants to change. Editable keys are
// merged into the existing file; non-editable keys (mcp, secrets) are
// rejected with 400. API keys are write-only: a non-empty api_key /
// vision_api_key replaces the stored value, and clear_api_key /
// clear_vision_api_key removes it. The file is written atomically (tmp + rename).
func (api *ConfigAPI) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := validateConfigUpdate(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	path := resolveConfigPath("config.json")

	// Load the existing file as a generic map so untouched keys are preserved.
	raw := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "existing config is not valid JSON: " + err.Error()})
			return
		}
	} else if !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read config: " + err.Error()})
		return
	}

	// Reject any key outside the editable + api-key allowlists so a typo (or
	// an attempt to change an unrelated secret) fails loudly instead of being
	// silently dropped.
	for key := range req {
		if !editableKey(key) && !apiKeyControlKey(key) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("config key %q is not editable via the web UI", key)})
			return
		}
	}

	// Secret fields are never accepted through nested media objects - only
	// the write-only top-level control keys may change them - so a partial
	// patch cannot blank or replace stored credentials via deepMergeValue.
	if m, ok := req["media"].(map[string]interface{}); ok {
		for _, secret := range []string{"fal_key", "openai_image_key", "openai_video_key"} {
			delete(m, secret)
		}
	}

	// Apply only the allowlisted keys, deep-merging nested object blocks so a
	// partial update to e.g. loop_guard preserves its sibling fields.
	for _, key := range editableConfigKeys {
		if v, ok := req[key]; ok {
			raw[key] = deepMergeValue(raw[key], v)
		}
	}

	// Write-only API key handling. clear_* takes precedence: when both a
	// clear flag and a new value arrive, the stored key is cleared. Values
	// are never echoed back in the response.
	if clear, ok := req["clear_api_key"].(bool); ok && clear {
		delete(raw, "api_key")
	} else if v, ok := req["api_key"].(string); ok && v != "" {
		raw["api_key"] = v
	}
	if clear, ok := req["clear_vision_api_key"].(bool); ok && clear {
		delete(raw, "vision_api_key")
	} else if v, ok := req["vision_api_key"].(string); ok && v != "" {
		raw["vision_api_key"] = v
	}
	// Media secrets live nested under media in config.json but are managed
	// through write-only top-level control keys.
	for _, k := range []string{"fal_key", "openai_image_key", "openai_video_key"} {
		if clear, ok := req["clear_"+k].(bool); ok && clear {
			clearMediaSecret(raw, k)
		} else if v, ok := req[k].(string); ok && v != "" {
			setMediaSecret(raw, k, v)
		}
	}

	// The merged result must still parse into a typed Config.
	mergedJSON, err := json.Marshal(raw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize config: " + err.Error()})
		return
	}
	var typed config.Config
	if err := json.Unmarshal(mergedJSON, &typed); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid config value: " + err.Error()})
		return
	}

	if err := writeConfigFileAtomic(path, mergedJSON); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save config: " + err.Error()})
		return
	}

	log.Printf("config: updated %s", path)
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": path})
}

// setMediaSecret stores v under raw["media"][key], creating the nested media
// object when it is absent or not an object.
func setMediaSecret(raw map[string]interface{}, key, v string) {
	m, ok := raw["media"].(map[string]interface{})
	if !ok {
		m = map[string]interface{}{}
	}
	m[key] = v
	raw["media"] = m
}

// clearMediaSecret removes key from raw["media"] and from the legacy
// top-level location.
func clearMediaSecret(raw map[string]interface{}, key string) {
	if m, ok := raw["media"].(map[string]interface{}); ok {
		delete(m, key)
	}
	delete(raw, key)
}

// validateConfigUpdate checks enum-constrained fields before they hit disk.
func validateConfigUpdate(req map[string]interface{}) error {
	if v, ok := req["provider"].(string); ok && v != "" {
		switch v {
		case "gemini", "openai", "openai-compatible":
		default:
			return fmt.Errorf("invalid provider %q: must be gemini, openai, or openai-compatible", v)
		}
	}
	if v, ok := req["model_vision"].(string); ok && v != "" {
		switch v {
		case "auto", "yes", "no":
		default:
			return fmt.Errorf("invalid model_vision %q: must be auto, yes, or no", v)
		}
	}
	if v, ok := req["thinking_level"].(string); ok && v != "" {
		switch v {
		case "off", "low", "medium", "high", "maximum", "xhigh":
		default:
			return fmt.Errorf("invalid thinking_level %q", v)
		}
	}
	if v, ok := req["system_env"].(map[string]interface{}); ok {
		if enabled, ok := v["enabled"]; ok {
			if _, ok := enabled.(bool); !ok {
				return fmt.Errorf("system_env.enabled must be a boolean")
			}
		}
	}
	if raw, present := req["units"]; present {
		v, ok := raw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("units must be an object")
		}
		if rawSystem, present := v["system"]; present {
			sys, ok := rawSystem.(string)
			if !ok {
				return fmt.Errorf("units.system must be a string")
			}
			if sys != "" {
				switch sys {
				case "metric", "imperial":
				default:
					return fmt.Errorf("units.system must be metric or imperial")
				}
			}
		}
	}
	if v, ok := req["sandbox"].(map[string]interface{}); ok {
		if mode, ok := v["mode"].(string); ok && mode != "" {
			switch mode {
			case "paths", "bubblewrap", "landlock", "off":
			default:
				return fmt.Errorf("invalid sandbox.mode %q: must be paths, bubblewrap, landlock, or off", mode)
			}
		}
	}
	if v, ok := req["approval"].(map[string]interface{}); ok {
		if mode, ok := v["mode"].(string); ok && mode != "" {
			switch mode {
			case "interactive", "deny", "allow":
			default:
				return fmt.Errorf("invalid approval.mode %q: must be interactive, deny, or allow", mode)
			}
		}
	}
	// API key controls: set values must be strings (non-empty replaces the
	// stored key), clear flags must be booleans.
	if v, ok := req["api_key"]; ok {
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Errorf("api_key must be a non-empty string to set a new key")
		}
	}
	if v, ok := req["vision_api_key"]; ok {
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Errorf("vision_api_key must be a non-empty string to set a new key")
		}
	}
	if v, ok := req["clear_api_key"]; ok {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("clear_api_key must be a boolean")
		}
	}
	if v, ok := req["clear_vision_api_key"]; ok {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("clear_vision_api_key must be a boolean")
		}
	}
	// Media secret controls follow the same contract as api_key: values must
	// be non-empty strings, clear flags must be booleans - otherwise the
	// handler would silently no-op and report success.
	for _, k := range []string{"fal_key", "openai_image_key", "openai_video_key"} {
		if v, ok := req[k]; ok {
			if s, isStr := v.(string); !isStr || s == "" {
				return fmt.Errorf("%s must be a non-empty string to set a new key", k)
			}
		}
		if v, ok := req["clear_"+k]; ok {
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("clear_%s must be a boolean", k)
			}
		}
	}
	return nil
}

// writeConfigFileAtomic writes data to path via a temp file + rename so a
// crash mid-write cannot leave a truncated config behind.
func writeConfigFileAtomic(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// editableKey reports whether key is in the editable allowlist.
func editableKey(key string) bool {
	for _, k := range editableConfigKeys {
		if k == key {
			return true
		}
	}
	return false
}

// apiKeyControlKey reports whether key is a write-only API key control
// (set/clear). These are accepted by PUT but never returned by GET.
func apiKeyControlKey(key string) bool {
	for _, k := range apiKeyControlKeys {
		if k == key {
			return true
		}
	}
	return false
}

// deepMergeValue merges a request value into an existing config value. Maps
// are merged key-by-key (a partial nested update preserves sibling fields);
// every other value type replaces the existing value outright.
func deepMergeValue(existing, incoming interface{}) interface{} {
	existingMap, okE := existing.(map[string]interface{})
	incomingMap, okI := incoming.(map[string]interface{})
	if !okE || !okI {
		return incoming
	}
	merged := make(map[string]interface{}, len(existingMap)+len(incomingMap))
	for k, v := range existingMap {
		merged[k] = v
	}
	for k, v := range incomingMap {
		merged[k] = deepMergeValue(merged[k], v)
	}
	return merged
}
