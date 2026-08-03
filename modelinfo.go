package main

import (
	"context"
	"fmt"
)

// ModelInfo describes the selected model as reported by its provider: the
// context window (input token limit), whether thinking is supported, and the
// thinking level in use. ThinkingLevel is the raw provider string so values
// like "maximum" or "xhigh" are shown exactly as reported.
//
// ContextWindow is the total context budget; MaxInputTokens is the model's
// max input-token allowance (may differ from ContextWindow on some providers,
// and is 0 when the provider does not report a separate input limit). The
// effective budget used by context management is min(MaxInputTokens,
// 0.9*ContextWindow), falling back to ContextWindow when MaxInputTokens is 0.
type ModelInfo struct {
	Name            string
	ContextWindow   int64
	MaxInputTokens  int64
	ThinkingEnabled bool
	ThinkingLevel   string
	Source          string
}

// FetchModelInfo resolves the configured provider and queries it for the
// selected model's capabilities. The resolved model name (defaulting to the
// provider default when unset) is used for both the request and the report.
func FetchModelInfo(ctx context.Context, cfg *Config) (*ModelInfo, error) {
	provider, err := ProviderFactory(cfg)
	if err != nil {
		return nil, fmt.Errorf("model info: %w", err)
	}
	modelName := cfg.ModelName
	if modelName == "" {
		modelName = provider.GetDefaultModel()
	}
	info, err := provider.GetModelInfo(ctx, cfg, modelName)
	if err != nil {
		return nil, fmt.Errorf("model info for %s: %w", modelName, err)
	}
	if info.Name == "" {
		info.Name = modelName
	}
	if info.ThinkingLevel == "" {
		info.ThinkingLevel = cfg.ThinkingLevel
	}
	return info, nil
}
