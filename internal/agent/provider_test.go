package agent

import (
	"amurru/hakase/internal/config"
	"context"
	"strings"
	"testing"
)

func TestGeminiProvider(t *testing.T) {
	p := &GeminiProvider{}

	if err := p.ValidateConfig(&config.Config{}); err == nil {
		t.Errorf("ValidateConfig with empty API key: expected error, got nil")
	}

	if err := p.ValidateConfig(&config.Config{APIKey: "test-key"}); err != nil {
		t.Errorf("ValidateConfig with API key: unexpected error: %v", err)
	}

	if got := p.GetDefaultModel(); got != "gemini-3.7-flash" {
		t.Errorf("GetDefaultModel: expected %q, got %q", "gemini-3.7-flash", got)
	}
}

func TestOpenAIProvider(t *testing.T) {
	p := &OpenAIProvider{BaseURL: "https://example.com/v1"}

	if err := p.ValidateConfig(&config.Config{}); err == nil {
		t.Errorf("ValidateConfig with empty API key: expected error, got nil")
	}

	if err := p.ValidateConfig(&config.Config{APIKey: "test-key"}); err != nil {
		t.Errorf("ValidateConfig with API key: unexpected error: %v", err)
	}

	if got := p.GetDefaultModel(); got != "gpt-5.6-terra" {
		t.Errorf("GetDefaultModel: expected %q, got %q", "gpt-5.6-terra", got)
	}

	if p.BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL field not set correctly: got %q", p.BaseURL)
	}
}

func TestProviderFactory(t *testing.T) {
	cases := []struct {
		provider string
		wantType LLMProvider
		wantErr  bool
	}{
		{provider: "gemini", wantType: &GeminiProvider{}},
		{provider: "", wantType: &GeminiProvider{}},
		{provider: "openai", wantType: &OpenAIProvider{}},
		{provider: "openai-compatible", wantType: &OpenAIProvider{}},
		{provider: "bogus", wantErr: true},
	}

	for _, tc := range cases {
		cfg := &config.Config{Provider: tc.provider, BaseURL: "https://example.com/v1"}
		got, err := ProviderFactory(cfg)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ProviderFactory(%q): expected error, got nil", tc.provider)
			} else if !strings.Contains(err.Error(), "unsupported provider") {
				t.Errorf("ProviderFactory(%q): error does not mention unsupported provider: %v", tc.provider, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ProviderFactory(%q): unexpected error: %v", tc.provider, err)
			continue
		}
		if _, ok := got.(*GeminiProvider); ok {
			if _, ok := tc.wantType.(*GeminiProvider); !ok {
				t.Errorf("ProviderFactory(%q): expected non-Gemini provider, got GeminiProvider", tc.provider)
			}
		} else if op, ok := got.(*OpenAIProvider); ok {
			if _, ok := tc.wantType.(*OpenAIProvider); !ok {
				t.Errorf("ProviderFactory(%q): expected non-OpenAI provider, got OpenAIProvider", tc.provider)
			}
			if op.BaseURL != "https://example.com/v1" {
				t.Errorf("ProviderFactory(%q): BaseURL not propagated, got %q", tc.provider, op.BaseURL)
			}
		} else {
			t.Errorf("ProviderFactory(%q): unexpected provider type %T", tc.provider, got)
		}
	}
}

func TestOpenAIProviderCreateModel(t *testing.T) {
	p := &OpenAIProvider{BaseURL: "https://example.com/v1"}

	m, err := p.CreateModel(context.Background(), "gpt-4o-mini", "test-key")
	if err != nil {
		t.Fatalf("CreateModel: unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("CreateModel: expected non-nil model, got nil")
	}
}
