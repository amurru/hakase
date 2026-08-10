package main

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"context"
	"fmt"
)

// ModelInfo is an alias for interfaces.ModelInfo to keep the root package
// compatible with internal packages that consume this type.
type ModelInfo = interfaces.ModelInfo

// FetchModelInfo resolves the configured provider and queries it for the
// selected model's capabilities. The resolved model name (defaulting to the
// provider default when unset) is used for both the request and the report.
func FetchModelInfo(ctx context.Context, cfg *config.Config) (*ModelInfo, error) {
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
