package config

import (
	"testing"
)

func TestHybridSearchDefaults(t *testing.T) {
	path := writeTempConfig(t, `{"provider":"gemini"}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HybridSearch {
		t.Error("HybridSearch default must be false (zero-dependency story)")
	}
	if cfg.KnowledgeEmbedModel != "" || cfg.KnowledgeEmbedBaseURL != "" {
		t.Errorf("embed knobs default empty: model=%q base=%q", cfg.KnowledgeEmbedModel, cfg.KnowledgeEmbedBaseURL)
	}
}

func TestHybridSearchValidation(t *testing.T) {
	// Hybrid on with no model fails fast.
	path := writeTempConfig(t, `{"provider":"gemini","hybrid_search":true}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("hybrid_search without knowledge_embed_model must fail")
	}
	// Gemini primary with no embed endpoint (and no primary base_url)
	// fails fast: native Gemini embeddings are out of scope.
	path = writeTempConfig(t, `{"provider":"gemini","knowledge_embed_model":"nomic-embed-text"}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("gemini primary without embed base URL must fail")
	}
	// Same shape with an embed endpoint passes.
	path = writeTempConfig(t, `{"provider":"gemini","hybrid_search":true,"knowledge_embed_model":"nomic-embed-text","knowledge_embed_base_url":"http://localhost:11434/v1"}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("valid hybrid config must load: %v", err)
	}
	if !cfg.HybridSearch || cfg.KnowledgeEmbedModel != "nomic-embed-text" {
		t.Errorf("hybrid knobs not applied: %+v", cfg.HybridSearch)
	}
	// OpenAI-compatible primary reuses its base_url: no embed base needed.
	path = writeTempConfig(t, `{"provider":"openai-compatible","model_name":"m","base_url":"http://localhost:11434/v1","hybrid_search":true,"knowledge_embed_model":"nomic-embed-text"}`)
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("openai-compatible hybrid without embed base must load: %v", err)
	}
}

func TestHybridSearchEnvOverrides(t *testing.T) {
	path := writeTempConfig(t, `{"provider":"gemini"}`)
	t.Setenv("HAKASE_HYBRID_SEARCH", "true")
	t.Setenv("HAKASE_KNOWLEDGE_EMBED_MODEL", "env-model")
	t.Setenv("HAKASE_KNOWLEDGE_EMBED_BASE_URL", "http://env:11434/v1")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.HybridSearch || cfg.KnowledgeEmbedModel != "env-model" || cfg.KnowledgeEmbedBaseURL != "http://env:11434/v1" {
		t.Errorf("env overrides not applied: hybrid=%v model=%q base=%q", cfg.HybridSearch, cfg.KnowledgeEmbedModel, cfg.KnowledgeEmbedBaseURL)
	}
}
