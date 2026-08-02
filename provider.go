package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// LLMProvider abstracts model creation and configuration validation across
// supported backends. The provider factory returns the concrete provider
// matching a config, isolating backend-specific APIs behind one interface.
type LLMProvider interface {
	CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error)
	ValidateConfig(cfg *Config) error
	GetDefaultModel() string
	GetModelInfo(ctx context.Context, cfg *Config, modelName string) (*ModelInfo, error)
}

// GeminiProvider creates models through the Google Gemini backend.
type GeminiProvider struct{}

// CreateModel constructs a Gemini model via the ADK v2 gemini package.
func (p *GeminiProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
}

// ValidateConfig returns an error when no API key is configured.
func (p *GeminiProvider) ValidateConfig(cfg *Config) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("gemini provider requires an api_key")
	}
	return nil
}

// GetDefaultModel returns the default Gemini model name.
func (p *GeminiProvider) GetDefaultModel() string {
	return "gemini-2.5-flash"
}

// GetModelInfo queries the Gemini API for the model's context window and
// thinking support via the models.get endpoint.
func (p *GeminiProvider) GetModelInfo(ctx context.Context, cfg *Config, modelName string) (*ModelInfo, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.APIKey})
	if err != nil {
		return nil, err
	}
	m, err := client.Models.Get(ctx, modelName, nil)
	if err != nil {
		return nil, err
	}
	return &ModelInfo{
		Name:            modelName,
		ContextWindow:   int64(m.InputTokenLimit),
		ThinkingEnabled: m.Thinking,
		Source:          "gemini models.get",
	}, nil
}

// OpenAIProvider creates models through OpenAI or any OpenAI-compatible
// endpoint. v2 openaimodel is marked EXPERIMENTAL; factory isolates it so
// swapping later is one-file change.
type OpenAIProvider struct {
	BaseURL string
}

// CreateModel constructs an OpenAI-compatible model via the ADK v2
// openaimodel package. BaseURL is only set when non-empty so the default
// OpenAI endpoint is used otherwise.
func (p *OpenAIProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	cfg := &openaimodel.ClientConfig{APIKey: apiKey}
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
	}
	return openaimodel.NewModel(ctx, modelName, cfg)
}

// ValidateConfig returns an error when no API key is configured.
func (p *OpenAIProvider) ValidateConfig(cfg *Config) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("openai provider requires an api_key")
	}
	return nil
}

// GetDefaultModel returns the default OpenAI model name.
func (p *OpenAIProvider) GetDefaultModel() string {
	return "gpt-4o-mini"
}

// openAIModelInfo mirrors the model objects returned by OpenAI-compatible
// model endpoints. context_length is an OpenRouter extension; input_token_limit
// appears on some self-hosted endpoints. reasoning is either a bool or an
// object (e.g. {"effort": true}), so it is captured raw and inspected.
type openAIModelInfo struct {
	ID                  string          `json:"id"`
	ContextLength       int64           `json:"context_length"`
	InputTokenLimit     int64           `json:"input_token_limit"`
	Reasoning           json.RawMessage `json:"reasoning"`
	SupportedParameters []string        `json:"supported_parameters"`
}

// reasoningDetail carries the fields of the OpenRouter reasoning object that
// are relevant for display: the default effort level and the supported ones.
type reasoningDetail struct {
	DefaultEffort    string   `json:"default_effort"`
	SupportedEfforts []string `json:"supported_efforts"`
}

// reasoningSupported reports whether the raw reasoning field (or the
// supported_parameters list) indicates a reasoning/thinking-capable model.
func (m *openAIModelInfo) reasoningSupported() bool {
	if len(m.Reasoning) > 0 && string(m.Reasoning) != "false" && string(m.Reasoning) != "null" {
		return true
	}
	for _, p := range m.SupportedParameters {
		if p == "reasoning" || p == "thinking" {
			return true
		}
	}
	return false
}

// thinkingLevel reports the provider's default reasoning effort, if exposed.
func (m *openAIModelInfo) thinkingLevel() string {
	if len(m.Reasoning) == 0 || string(m.Reasoning) == "false" || string(m.Reasoning) == "null" {
		return ""
	}
	var d reasoningDetail
	if err := json.Unmarshal(m.Reasoning, &d); err != nil || d.DefaultEffort == "" {
		return ""
	}
	return d.DefaultEffort
}

// GetModelInfo queries the OpenAI-compatible models endpoint for the model's
// context window and reasoning support. It tries the per-model endpoint first
// (GET /models/{name}) and falls back to scanning the full model list when the
// endpoint does not exist.
func (p *OpenAIProvider) GetModelInfo(ctx context.Context, cfg *Config, modelName string) (*ModelInfo, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	client := &http.Client{Timeout: 15 * time.Second}

	fetch := func(path string) (*openAIModelInfo, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
		}
		var info openAIModelInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return nil, err
		}
		return &info, nil
	}

	info, err := fetch("/models/" + modelName)
	if err == nil && info.ID != "" {
		return info.toModelInfo(modelName), nil
	}

	// Some endpoints only support listing; find the model in the list.
	list := struct {
		Data []openAIModelInfo `json:"data"`
	}{}
	if lerr := func() error {
		body, gerr := fetchRaw(client, ctx, base, "/models", cfg.APIKey)
		if gerr != nil {
			return gerr
		}
		return json.Unmarshal(body, &list)
	}(); lerr != nil {
		if err == nil {
			return nil, lerr
		}
		return nil, err
	}
	for i := range list.Data {
		if list.Data[i].ID == modelName {
			return list.Data[i].toModelInfo(modelName), nil
		}
	}
	return nil, fmt.Errorf("model %q not found in %s", modelName, base+"/models")
}

func (m *openAIModelInfo) toModelInfo(name string) *ModelInfo {
	limit := m.ContextLength
	if limit == 0 {
		limit = m.InputTokenLimit
	}
	return &ModelInfo{
		Name:            name,
		ContextWindow:   limit,
		ThinkingEnabled: m.reasoningSupported(),
		ThinkingLevel:   m.thinkingLevel(),
		Source:          "models endpoint",
	}
}

func fetchRaw(client *http.Client, ctx context.Context, base, path, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ProviderFactory returns the provider matching cfg.Provider, defaulting to
// Gemini when the field is empty.
func ProviderFactory(cfg *Config) (LLMProvider, error) {
	switch cfg.Provider {
	case "gemini", "":
		return &GeminiProvider{}, nil
	case "openai", "openai-compatible":
		return &OpenAIProvider{BaseURL: cfg.BaseURL}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}
