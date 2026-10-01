package media

import (
	"context"
	"errors"
	"fmt"

	"amurru/hakase/internal/config"
)

// videoNoProviderMsg is the verbatim actionable error returned when no
// video-capable provider is configured. Shared by the registry and the
// generate_video tool layer.
const videoNoProviderMsg = "video generation requires a provider: configure an OpenAI-compatible router with video support (media.openai_video_key / openai_video_base_url, e.g. OpenRouter), or set fal_key (HAKASE_FAL_KEY) with media.video_provider fal"

// audioOffMsg is the verbatim actionable error when audio generation is
// switched off. Shared by the registry-adjacent tool layer.
const audioOffMsg = "audio generation is off: set media.audio_provider to piper with a text_to_speech default voice"

// Registry resolves providers by name or auto order, with semaphore concurrency control.
type Registry struct {
	cfg       config.MediaConfig
	tts       config.TTSConfig
	log       LogFunc
	store     *Store
	providers map[string]Provider
	order     []string
	sem       map[string]chan struct{}
}

// NewRegistry builds the provider factory map internally and validates config.
func NewRegistry(cfg config.MediaConfig, log LogFunc, store *Store) (*Registry, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("media store is required")
	}
	r := &Registry{
		cfg:       cfg,
		log:       log,
		store:     store,
		providers: make(map[string]Provider),
		order:     cfg.Order,
		sem:       make(map[string]chan struct{}),
	}
	// Build factory map internally (no global registration).
	factories := map[string]Factory{
		"pil":    newPilProvider,
		"openai": newOpenAIProvider,
		"fal":    newFalProvider,
	}
	for name, factory := range factories {
		p, err := factory(cfg, log, store)
		if err != nil {
			// Log and skip failed provider construction; don't fail whole registry.
			if log != nil {
				log(fmt.Sprintf("WARN [media] provider %s init failed: %v", name, err))
			}
			continue
		}
		r.providers[name] = p
		// Per-provider semaphore: 1 for pil, 4 for cloud.
		max := cfg.MaxConcurrent
		if max == 0 {
			if name == "pil" {
				max = 1
			} else {
				max = 4
			}
		}
		if max < 1 {
			max = 1
		}
		r.sem[name] = make(chan struct{}, max)
	}
	// Ensure pil always exists (fallback guarantee). If factory failed, create directly.
	if _, ok := r.providers["pil"]; !ok {
		p, err := newPilProvider(cfg, log, store)
		if err == nil {
			r.providers["pil"] = p
			max := cfg.MaxConcurrent
			if max == 0 {
				max = 1
			}
			r.sem["pil"] = make(chan struct{}, max)
		}
	}
	// Piper TTS is (re)built by SetTTSConfig, which carries the voices and
	// binary paths from top-level text_to_speech (outside MediaConfig).
	// A zero TTS config still registers the provider: resolution health
	// gates on the default voice, and synthesis fails actionably.
	r.setPiperProvider(log, store)
	return r, nil
}

// SetTTSConfig supplies the top-level text_to_speech config and (re)builds
// the piper provider entry against it. Called by setupMedia after
// NewRegistry; tests call it directly to simulate configured voices.
func (r *Registry) SetTTSConfig(tts config.TTSConfig) {
	r.tts = tts
	r.setPiperProvider(r.log, r.store)
}

func (r *Registry) setPiperProvider(log LogFunc, store *Store) {
	p, err := newPiperProvider(r.cfg, r.tts, log, store)
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("WARN [media] provider piper init failed: %v", err))
		}
		return
	}
	r.providers["piper"] = p
	max := r.cfg.MaxConcurrent
	if max == 0 {
		max = 2
	}
	if max < 1 {
		max = 1
	}
	r.sem["piper"] = make(chan struct{}, max)
}
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Resolve returns the provider for an auto or explicit request.
// kind is "image", "video", or "audio".
func (r *Registry) Resolve(kind string) (Provider, error) {
	return r.ResolveForProvider(kind, "auto")
}

// ResolveForProvider resolves with an explicit provider hint ("auto" or name).
func (r *Registry) ResolveForProvider(kind string, providerHint string) (Provider, error) {
	if providerHint != "" && providerHint != "auto" {
		p, ok := r.providers[providerHint]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q", providerHint)
		}
		if !supportsKind(p.Capabilities(), kind) {
			return nil, fmt.Errorf("provider %q does not support %s", providerHint, kind)
		}
		if !r.isHealthy(p.Name(), kind) {
			return nil, fmt.Errorf("provider %q is not configured (missing key)", providerHint)
		}
		return p, nil
	}
	// Auto: walk order.
	for _, name := range r.order {
		p, ok := r.providers[name]
		if !ok {
			continue
		}
		if !supportsKind(p.Capabilities(), kind) {
			continue
		}
		if !r.isHealthy(p.Name(), kind) {
			continue
		}
		return p, nil
	}
	// Special error messages for video/audio auto with no provider.
	if kind == "video" {
		return nil, errors.New(videoNoProviderMsg)
	}
	if kind == "audio" {
		return nil, fmt.Errorf("no audio provider configured: set media.audio_provider to piper with a text_to_speech default voice (local Piper TTS)")
	}
	// For image, pil guarantee should have prevented this.
	if kind == "image" {
		// Try pil directly as last resort.
		if p, ok := r.providers["pil"]; ok {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no provider available for %s", kind)
}

func supportsKind(c Capabilities, kind string) bool {
	switch kind {
	case "image":
		return c.Image
	case "video":
		return c.Video
	case "audio":
		return c.Audio
	default:
		return false
	}
}

// isHealthy reports whether a provider has the credentials needed for the
// requested kind. Health is per kind: a video-only OpenAI credential (e.g.
// only openai_video_key set) must make the provider resolvable for video even
// though it cannot serve images.
func (r *Registry) isHealthy(name, kind string) bool {
	cfg := r.cfg
	switch name {
	case "pil":
		return true
	case "openai":
		switch kind {
		case "video":
			// GenerateVideo accepts OpenAIVideoKey and falls back to
			// OpenAIImageKey, mirroring that chain here.
			return cfg.OpenAIVideoKey != "" || cfg.OpenAIImageKey != ""
		default:
			return cfg.OpenAIImageKey != ""
		}
	case "fal":
		return cfg.FalKey != ""
	case "piper":
		// Static only (no shell-outs at resolve time): a configured
		// default voice means synthesis is plausibly available; a
		// missing binary or voice file fails actionably at Generate.
		return r.tts.Voices["default"] != ""
	default:
		return false
	}
}

// Acquire acquires the semaphore for the provider.
func (r *Registry) Acquire(ctx context.Context, providerName string) error {
	ch, ok := r.sem[providerName]
	if !ok {
		return nil
	}
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release releases the semaphore.
func (r *Registry) Release(providerName string) {
	ch, ok := r.sem[providerName]
	if !ok {
		return
	}
	select {
	case <-ch:
	default:
	}
}

// Store returns the media store.
func (r *Registry) Store() *Store { return r.store }

// Config returns the media config (copy).
func (r *Registry) Config() config.MediaConfig { return r.cfg }

// Stubs for provider constructors - to be implemented in separate files.
// Declared here so registry compiles before providers.
func newPilProvider(cfg config.MediaConfig, log LogFunc, store *Store) (Provider, error) {
	return NewPilProvider(store), nil
}

func newOpenAIProvider(cfg config.MediaConfig, log LogFunc, store *Store) (Provider, error) {
	return NewOpenAIProvider(cfg, log, store), nil
}

func newFalProvider(cfg config.MediaConfig, log LogFunc, store *Store) (Provider, error) {
	return NewFalProvider(cfg, log, store), nil
}
