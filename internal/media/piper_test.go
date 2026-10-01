package media

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

func piperTestConfig() config.MediaConfig {
	cfg := config.MediaConfig{AudioProvider: "piper"}
	cfg.ApplyDefaults()
	return cfg
}

func TestPiperRegisteredWithAudioCapability(t *testing.T) {
	s := tempStore(t)
	reg, err := NewRegistry(piperTestConfig(), nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, ok := reg.Get("piper")
	if !ok {
		t.Fatal("piper provider must register (even with zero TTS config)")
	}
	if cap := p.Capabilities(); !cap.Audio || cap.Image || cap.Video {
		t.Errorf("piper capabilities = %+v, want audio-only", cap)
	}
}

func TestPiperUnhealthyWithoutVoice(t *testing.T) {
	s := tempStore(t)
	reg, err := NewRegistry(piperTestConfig(), nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// No default voice configured: auto must fail with the actionable
	// no-provider message, not a bare "unsupported".
	if _, err := reg.Resolve("audio"); err == nil || !strings.Contains(err.Error(), "media.audio_provider") {
		t.Errorf("resolve = %v, want actionable no-provider error", err)
	}
	// Explicit hint also fails (health gate), naming the provider.
	if _, err := reg.ResolveForProvider("audio", "piper"); err == nil || !strings.Contains(err.Error(), "piper") {
		t.Errorf("explicit resolve = %v, want piper health error", err)
	}
}

func TestPiperResolvesWithVoice(t *testing.T) {
	s := tempStore(t)
	reg, err := NewRegistry(piperTestConfig(), nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.SetTTSConfig(config.TTSConfig{Voices: map[string]string{"default": "/nonexistent/voice.onnx"}})
	p, err := reg.Resolve("audio")
	if err != nil {
		t.Fatalf("resolve with voice = %v", err)
	}
	if p.Name() != "piper" {
		t.Errorf("resolved = %q, want piper", p.Name())
	}
}

func TestPiperGenerateAudioWithoutBinary(t *testing.T) {
	s := tempStore(t)
	reg, err := NewRegistry(piperTestConfig(), nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// No piper binary or voice model in CI: synthesis must fail with the
	// actionable Availability error (what binary/voice is missing), never
	// a panic or hang.
	reg.SetTTSConfig(config.TTSConfig{Voices: map[string]string{"default": "/nonexistent/voice.onnx"}})
	p, err := reg.Resolve("audio")
	if err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if _, err := p.GenerateAudio(t.Context(), AudioRequest{Text: "hello"}); err == nil {
		t.Fatal("synthesis without binary/voice must fail")
	} else if !strings.Contains(err.Error(), "speech:") {
		t.Errorf("error = %q, want actionable speech: error", err)
	}
}

func TestAudioRequestValidation(t *testing.T) {
	if err := (AudioRequest{}).Validate(); err == nil {
		t.Error("empty text must fail")
	}
	if err := (AudioRequest{Text: "hi"}).Validate(); err != nil {
		t.Errorf("short text must pass: %v", err)
	}
	if err := (AudioRequest{Text: strings.Repeat("a", 4001)}).Validate(); err == nil {
		t.Error("over-long text must fail")
	}
}

func TestPiperVoiceLabel(t *testing.T) {
	tts := config.TTSConfig{Voices: map[string]string{"default": "/models/en_US-amy-medium.onnx"}}
	if got := piperVoiceLabel(tts, ""); got != "en_US-amy-medium.onnx" {
		t.Errorf("default label = %q", got)
	}
	if got := piperVoiceLabel(tts, "de"); got != "de" {
		t.Errorf("unconfigured voice label = %q", got)
	}
	if got := piperVoiceLabel(config.TTSConfig{}, ""); got != "piper-default" {
		t.Errorf("empty label = %q", got)
	}
}
