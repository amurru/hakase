package agent

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"
	"context"
	"fmt"
	"iter"
	"log"

	"google.golang.org/adk/v2/model"
)

// FallbackProvider is an LLMProvider that fans CreateModel out over an
// ordered provider chain (primary first, then fallbacks). It is returned by
// ProviderFactory when cfg.FallbackProviders is non-empty; every model it
// creates is a *FallbackModel, so per-request provider outages fall over to
// the next provider in the chain instead of failing the run.
type FallbackProvider struct {
	cfg       *config.Config
	providers []LLMProvider // primary first, then fallbacks in order
}

// CreateModel returns a fallback-aware model for the requested model name and
// API key. The requested values override the stored config for this model so
// callers that create auxiliary models (e.g. the compaction summary model)
// get a fallback chain over the model they asked for, not the primary one.
func (p *FallbackProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	fc := *p.cfg
	if modelName != "" {
		fc.ModelName = modelName
	}
	if apiKey != "" {
		fc.APIKey = apiKey
	}
	return &FallbackModel{cfg: &fc, providers: p.providers}, nil
}

// ValidateConfig validates the primary provider strictly; a broken optional
// fallback logs a warning and is skipped, never failing startup (mirroring
// NewFallbackModel).
func (p *FallbackProvider) ValidateConfig(cfg *config.Config) error {
	if err := p.providers[0].ValidateConfig(cfg); err != nil {
		return err
	}
	for _, name := range cfg.FallbackProviders {
		fc := *cfg
		fc.Provider = name
		fp, err := providerForName(name, cfg.BaseURL)
		if err != nil {
			log.Printf("fallback: skipping broken fallback provider %q: %v", name, err)
			continue
		}
		if err := fp.ValidateConfig(&fc); err != nil {
			log.Printf("fallback: skipping invalid fallback provider %q: %v", name, err)
		}
	}
	return nil
}

// GetDefaultModel returns the primary provider's default model.
func (p *FallbackProvider) GetDefaultModel() string {
	return p.providers[0].GetDefaultModel()
}

// GetModelInfo reports the primary provider's model info; capability labels
// (context window, vision) describe the model the run starts on.
func (p *FallbackProvider) GetModelInfo(ctx context.Context, cfg *config.Config, modelName string) (*ModelInfo, error) {
	return p.providers[0].GetModelInfo(ctx, cfg, modelName)
}

// FallbackModel wraps a primary model with an ordered chain of fallback
// providers. GenerateContent yields from the primary; if the FIRST yielded
// value is a non-nil error (or the stream is empty), the next provider in the
// chain is tried. Once a provider yields a successful response, the rest of
// its stream is passed through untouched: an iterator cannot be rewound, so a
// mid-stream error is terminal and cannot be retried.
//
// Wiring: ProviderFactory returns a *FallbackProvider (whose CreateModel
// yields a *FallbackModel) whenever cfg.FallbackProviders is non-empty, so
// SetupRunner and every other ProviderFactory consumer get fallback behavior
// with no per-callsite wrapping.
type FallbackModel struct {
	cfg       *config.Config
	providers []LLMProvider // primary first, then optional fallbacks in order
}

// NewFallbackModel builds a FallbackModel from cfg. The primary provider
// comes from the single-provider resolution; each name in
// cfg.FallbackProviders is resolved the same way (only the Provider field is
// swapped). A broken optional fallback logs a warning and is skipped - it
// never fails startup. An error is returned only when no provider can be
// built at all.
func NewFallbackModel(cfg *config.Config) (*FallbackModel, error) {
	if cfg == nil {
		return nil, fmt.Errorf("fallback: nil config")
	}
	primary, err := providerForName(cfg.Provider, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("fallback: primary provider: %w", err)
	}
	providers := []LLMProvider{primary}
	for _, name := range cfg.FallbackProviders {
		p, err := providerForName(name, cfg.BaseURL)
		if err != nil {
			log.Printf("fallback: skipping broken fallback provider %q: %v", name, err)
			continue
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("fallback: no providers available")
	}
	return &FallbackModel{cfg: cfg, providers: providers}, nil
}

// Name returns a descriptive name identifying the primary provider.
func (fm *FallbackModel) Name() string {
	return fmt.Sprintf("fallback(%s)", fm.cfg.Provider)
}

// GenerateContent yields from the primary provider and falls back on the first
// yielded error (or an empty stream). Errors after the first successful
// response are passed through - mid-stream failures cannot be retried.
// Every fall-through is logged (stderr + structured debug log) so a
// primary-provider outage is visible in the logs, not silent.
func (fm *FallbackModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var lastErr error
		for i, p := range fm.providers {
			modelName := fm.cfg.ModelName
			if modelName == "" {
				modelName = p.GetDefaultModel()
			}
			m, err := p.CreateModel(ctx, modelName, fm.cfg.APIKey)
			if err != nil {
				lastErr = fmt.Errorf("create model: %w", err)
				logFallback(i, len(fm.providers), lastErr)
				continue
			}
			first, ok := pullFirst(m.GenerateContent(ctx, req, stream))
			if !ok {
				lastErr = fmt.Errorf("empty stream")
				logFallback(i, len(fm.providers), lastErr)
				continue
			}
			if first.err != nil {
				first.stop()
				lastErr = first.err
				logFallback(i, len(fm.providers), lastErr)
				continue
			}
			// First response succeeded: stream the rest, no fallback. stop is
			// idempotent, so releasing the pull iterator on every exit path is
			// safe even when the consumer aborts early.
			if !yield(first.resp, nil) {
				first.stop()
				return
			}
			for resp, err := range first.rest {
				if !yield(resp, err) {
					first.stop()
					return
				}
			}
			first.stop()
			return
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no providers configured")
		}
		yield(nil, fmt.Errorf("all providers failed: %w", lastErr))
	}
}

// logFallback records one fall-through to the next provider in the chain.
// attempt is the zero-based index of the provider that just failed. When more
// providers remain, the message names the retry; on the last provider it
// names the terminal failure. Logged to stderr (always on) and the structured
// debug log (dev mode) so a primary-provider outage is visible, not silent.
func logFallback(attempt, total int, err error) {
	if attempt+1 < total {
		log.Printf("fallback: provider %d/%d failed (%v), trying next provider", attempt+1, total, err)
		util.DebugEvent("provider_fallback", "attempt", attempt+1, "providers", total, "error", err.Error(), "retry", true)
		return
	}
	log.Printf("fallback: provider %d/%d failed (%v), no more providers", attempt+1, total, err)
	util.DebugEvent("provider_fallback", "attempt", attempt+1, "providers", total, "error", err.Error(), "retry", false)
}

// firstValue holds the eagerly-pulled first pair of a provider stream plus a
// seq for the remaining pairs and a stop function releasing the underlying
// pull iterator.
type firstValue struct {
	resp *model.LLMResponse
	err  error
	rest iter.Seq2[*model.LLMResponse, error]
	stop func()
}

// pullFirst eagerly pulls the first pair from seq via iter.Pull2 so the
// caller can decide whether to fall back before any stream is consumed. rest
// chains the remaining pairs of the SAME underlying stream (no restart), and
// stop must be called when rest is not consumed so the pull iterator is
// released. stop is idempotent.
func pullFirst(seq iter.Seq2[*model.LLMResponse, error]) (firstValue, bool) {
	next, stop := iter.Pull2(seq)
	resp, err, ok := next()
	if !ok {
		stop()
		return firstValue{}, false
	}
	rest := func(yield func(*model.LLMResponse, error) bool) {
		defer stop()
		for {
			r, e, ok := next()
			if !ok {
				return
			}
			if !yield(r, e) {
				return
			}
		}
	}
	return firstValue{resp: resp, err: err, rest: rest, stop: stop}, true
}
