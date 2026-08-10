package main

import (
	"amurru/hakase/internal/config"
)

// resolveVisionProvider selects the provider used to create the vision model.
// Precedence: an explicit vision_provider wins; otherwise vision_base_url
// forces an OpenAI-compatible endpoint; otherwise the main provider is reused.
func resolveVisionProvider(main LLMProvider, cfg *config.Config) LLMProvider {
	if cfg != nil && cfg.VisionProvider != "" {
		switch cfg.VisionProvider {
		case "gemini":
			return &GeminiProvider{}
		case "openai", "openai-compatible":
			return &OpenAIProvider{BaseURL: cfg.VisionBaseURL}
		}
	}
	if cfg != nil && cfg.VisionBaseURL != "" {
		return &OpenAIProvider{BaseURL: cfg.VisionBaseURL}
	}
	return main
}
