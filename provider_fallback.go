package main

import (
	"context"
	"fmt"
	"iter"
	"log"

	"google.golang.org/adk/v2/model"
)

// FallbackModel wraps a primary model with an ordered chain of fallback
// providers. GenerateContent yields from the primary; if the FIRST yielded
// value is a non-nil error (or the stream is empty), the next provider in the
// chain is tried. Once a provider yields a successful response, the rest of
// its stream is passed through untouched: an iterator cannot be rewound, so a
// mid-stream error is terminal and cannot be retried.
//
// Wiring point (not active yet - agent.go belongs to T1): setupRunner in
// agent.go creates the model via provider.CreateModel. To enable fallback,
// wrap that result there:
//
//	if len(cfg.FallbackProviders) > 0 {
//	    if fm, err := NewFallbackModel(cfg); err == nil {
//	        model = fm
//	    }
//	}
type FallbackModel struct {
	cfg       *Config
	providers []LLMProvider // primary first, then optional fallbacks in order
}

// NewFallbackModel builds a FallbackModel from cfg. The primary provider
// comes from ProviderFactory(cfg); each name in cfg.FallbackProviders is
// resolved through ProviderFactory with the same cfg (only the Provider field
// is swapped). A broken optional fallback logs a warning and is skipped - it
// never fails startup. An error is returned only when no provider can be
// built at all.
func NewFallbackModel(cfg *Config) (*FallbackModel, error) {
	if cfg == nil {
		return nil, fmt.Errorf("fallback: nil config")
	}
	primary, err := ProviderFactory(cfg)
	if err != nil {
		return nil, fmt.Errorf("fallback: primary provider: %w", err)
	}
	providers := []LLMProvider{primary}
	for _, name := range cfg.FallbackProviders {
		fc := *cfg // shallow copy; only Provider is swapped
		fc.Provider = name
		p, err := ProviderFactory(&fc)
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
func (fm *FallbackModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var lastErr error
		for _, p := range fm.providers {
			modelName := fm.cfg.ModelName
			if modelName == "" {
				modelName = p.GetDefaultModel()
			}
			m, err := p.CreateModel(ctx, modelName, fm.cfg.APIKey)
			if err != nil {
				lastErr = fmt.Errorf("create model: %w", err)
				continue
			}
			first, ok := pullFirst(m.GenerateContent(ctx, req, stream))
			if !ok {
				lastErr = fmt.Errorf("empty stream")
				continue
			}
			if first.err != nil {
				first.stop()
				lastErr = first.err
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
