package main

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
)

// stubProvider is a test double implementing both LLMProvider and model.LLM.
// err makes CreateModel fail; genErr makes GenerateContent yield (nil, err) as
// its first value; responses are yielded in order on success.
type stubProvider struct {
	name      string
	err       error
	genErr    error
	responses []*model.LLMResponse
	callCount int
}

func (s *stubProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s, nil
}

func (s *stubProvider) ValidateConfig(cfg *Config) error { return nil }

func (s *stubProvider) GetDefaultModel() string { return "stub-model" }

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	s.callCount++
	if s.genErr != nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, s.genErr)
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, r := range s.responses {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func resp() *model.LLMResponse { return &model.LLMResponse{} }

// collectFallback drains fm.GenerateContent, returning the responses and the
// last error seen.
func collectFallback(fm *FallbackModel, stream bool) ([]*model.LLMResponse, error) {
	var responses []*model.LLMResponse
	var lastErr error
	for resp, err := range fm.GenerateContent(context.Background(), &model.LLMRequest{}, stream) {
		if err != nil {
			lastErr = err
			continue
		}
		responses = append(responses, resp)
	}
	return responses, lastErr
}

// (a) fallback happens on first-error from primary.
func TestFallbackOnFirstError(t *testing.T) {
	primaryResp := resp()
	fallbackResp := resp()
	primary := &stubProvider{name: "primary", genErr: fmt.Errorf("primary down")}
	fallback := &stubProvider{name: "fallback", responses: []*model.LLMResponse{fallbackResp}}
	fm := &FallbackModel{cfg: &Config{Provider: "gemini"}, providers: []LLMProvider{primary, fallback}}

	responses, err := collectFallback(fm, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary.callCount != 1 {
		t.Errorf("primary GenerateContent calls: expected 1, got %d", primary.callCount)
	}
	if fallback.callCount != 1 {
		t.Errorf("fallback GenerateContent calls: expected 1, got %d", fallback.callCount)
	}
	if len(responses) != 1 || responses[0] != fallbackResp {
		t.Fatalf("expected the fallback response, got %v", responses)
	}
	if primaryResp == fallbackResp {
		t.Fatal("test setup broken: distinct responses required")
	}
}

// (b) no fallback when primary succeeds; full primary stream passes through.
func TestNoFallbackWhenPrimarySucceeds(t *testing.T) {
	p1, p2 := resp(), resp()
	fallbackResp := resp()
	primary := &stubProvider{name: "primary", responses: []*model.LLMResponse{p1, p2}}
	fallback := &stubProvider{name: "fallback", responses: []*model.LLMResponse{fallbackResp}}
	fm := &FallbackModel{cfg: &Config{Provider: "gemini"}, providers: []LLMProvider{primary, fallback}}

	responses, err := collectFallback(fm, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.callCount != 0 {
		t.Errorf("fallback GenerateContent calls: expected 0, got %d", fallback.callCount)
	}
	if len(responses) != 2 || responses[0] != p1 || responses[1] != p2 {
		t.Fatalf("expected the full primary stream, got %d responses", len(responses))
	}
}

// (c) all providers failing yields the all-providers-failed error wrapping the
// last error.
func TestAllProvidersFailed(t *testing.T) {
	primary := &stubProvider{name: "primary", genErr: fmt.Errorf("primary down")}
	fallback := &stubProvider{name: "fallback", genErr: fmt.Errorf("fallback down")}
	fm := &FallbackModel{cfg: &Config{Provider: "gemini"}, providers: []LLMProvider{primary, fallback}}

	responses, err := collectFallback(fm, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("error does not mention all providers failed: %v", err)
	}
	if !strings.Contains(err.Error(), "fallback down") {
		t.Errorf("error does not wrap the last provider error: %v", err)
	}
	if len(responses) != 0 {
		t.Errorf("expected no responses, got %d", len(responses))
	}
}

// A CreateModel failure falls through to the next provider.
func TestFallbackOnCreateModelError(t *testing.T) {
	fallbackResp := resp()
	primary := &stubProvider{name: "primary", err: fmt.Errorf("create failed")}
	fallback := &stubProvider{name: "fallback", responses: []*model.LLMResponse{fallbackResp}}
	fm := &FallbackModel{cfg: &Config{Provider: "gemini"}, providers: []LLMProvider{primary, fallback}}

	responses, err := collectFallback(fm, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.callCount != 1 {
		t.Errorf("fallback GenerateContent calls: expected 1, got %d", fallback.callCount)
	}
	if len(responses) != 1 || responses[0] != fallbackResp {
		t.Fatalf("expected the fallback response, got %v", responses)
	}
}

// An empty primary stream counts as a failure and triggers fallback.
func TestFallbackOnEmptyStream(t *testing.T) {
	fallbackResp := resp()
	primary := &stubProvider{name: "primary", responses: nil}
	fallback := &stubProvider{name: "fallback", responses: []*model.LLMResponse{fallbackResp}}
	fm := &FallbackModel{cfg: &Config{Provider: "gemini"}, providers: []LLMProvider{primary, fallback}}

	responses, err := collectFallback(fm, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.callCount != 1 {
		t.Errorf("fallback GenerateContent calls: expected 1, got %d", fallback.callCount)
	}
	if len(responses) != 1 || responses[0] != fallbackResp {
		t.Fatalf("expected the fallback response, got %v", responses)
	}
}

// NewFallbackModel resolves fallback names through ProviderFactory, skipping
// broken ones, and errors when nothing can be built.
func TestNewFallbackModel(t *testing.T) {
	fm, err := NewFallbackModel(&Config{
		Provider:          "gemini",
		FallbackProviders: []string{"bogus-provider", "openai"},
	})
	if err != nil {
		t.Fatalf("NewFallbackModel: unexpected error: %v", err)
	}
	if len(fm.providers) != 2 {
		t.Errorf("providers: expected 2 (gemini primary + openai fallback, bogus skipped), got %d", len(fm.providers))
	}
	if got := fm.Name(); got != "fallback(gemini)" {
		t.Errorf("Name: expected %q, got %q", "fallback(gemini)", got)
	}

	if _, err := NewFallbackModel(&Config{Provider: "bogus-primary"}); err == nil {
		t.Error("NewFallbackModel with unsupported primary: expected error, got nil")
	}
}
