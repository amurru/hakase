// piper.go - the local Piper TTS audio provider (spec MG-012).
//
// This wires the existing speech.PiperTTS (shipped for Telegram
// voice-note replies) into the media registry as a first-class audio
// provider. No new synthesis code, no new dependencies: text in,
// OGG/Opus bytes out, stored like any other generated artifact.
package media

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/speech"
)

// piperProvider synthesizes speech locally via PiperTTS.
type piperProvider struct {
	store *Store
	tts   config.TTSConfig
}

// newPiperProvider builds the audio provider; tts carries voices and
// binary paths from top-level text_to_speech (see SetTTSConfig).
func newPiperProvider(cfg config.MediaConfig, tts config.TTSConfig, log LogFunc, store *Store) (Provider, error) {
	if store == nil {
		return nil, fmt.Errorf("piper provider needs a store")
	}
	return &piperProvider{store: store, tts: tts}, nil
}

func (p *piperProvider) Name() string { return "piper" }

func (p *piperProvider) Capabilities() Capabilities {
	return Capabilities{Audio: true}
}

func (p *piperProvider) GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider piper does not support image")
}

func (p *piperProvider) GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider piper does not support video")
}

// GenerateAudio synthesizes req.Text with the configured Piper voices.
// req.Voice selects the voice the same way Telegram does (per-language
// voice, script detection, default fallback); empty means default.
func (p *piperProvider) GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	synth := speech.NewPiperTTS(speech.TTSConfig{
		BinaryPath: p.tts.BinaryPath,
		Voices:     p.tts.Voices,
		FFMpegPath: p.tts.FFMpegPath,
	})
	audio, err := synth.Synthesize(ctx, req.Text, strings.ToLower(strings.TrimSpace(req.Voice)))
	if err != nil {
		return nil, err
	}
	path, err := p.store.Allocate(".ogg")
	if err != nil {
		return nil, err
	}
	if err := p.store.Write(path, bytes.NewReader(audio), 20<<20); err != nil {
		return nil, err
	}
	relPath := p.store.WorkspaceRelPath(path)
	return &MediaResult{
		Path:     relPath,
		Provider: "piper",
		Model:    piperVoiceLabel(p.tts, req.Voice),
		MimeType: "audio/ogg",
		Markdown: fmt.Sprintf(`<audio controls src="%s"></audio>`, relPath),
	}, nil
}

// piperVoiceLabel names the voice for the result/model surface: the
// configured onnx basename when it resolves, else the raw selector, else
// "default". Never a path that leaks host layout beyond the basename.
func piperVoiceLabel(tts config.TTSConfig, voice string) string {
	key := strings.ToLower(strings.TrimSpace(voice))
	if key == "" {
		key = "default"
	}
	if path := tts.Voices[key]; path != "" {
		if base := filepath.Base(path); base != "" {
			return base
		}
	}
	if key != "default" {
		return key
	}
	return "piper-default"
}
