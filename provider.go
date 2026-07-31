package main

import (
	"context"
	"fmt"

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
