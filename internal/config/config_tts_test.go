// config_tts_test.go - text_to_speech config: the voices map with its
// reserved "default" key, and the enabled-but-unconfigured loud failure.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTTSConfig(t *testing.T, tts string) (*Config, error) {
	t.Helper()
	// The TTS block only validates when the Telegram channel itself is
	// enabled (an inert block can't be misconfigured), so the wrapper turns
	// the channel on with a token.
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","channels":{"telegram":{"enabled":true,"bot_token":"tok","text_to_speech":` + tts + `}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

func TestTTSVoicesDefaultRequiredWhenEnabled(t *testing.T) {
	// enabled without a "default" voice is NOT a load error: it degrades at
	// runtime (Availability hint per message), matching the STT posture
	// where missing binaries degrade instead of failing the server.
	cfg, err := writeTTSConfig(t, `{"enabled":true}`)
	if err != nil {
		t.Fatalf("enabled without voices must load, got: %v", err)
	}
	if cfg.Channels.Telegram.TextToSpeech.Voices["default"] != "" {
		t.Fatalf("unexpected default voice: %+v", cfg.Channels.Telegram.TextToSpeech.Voices)
	}

	cfg, err = writeTTSConfig(t, `{"enabled":true,"voices":{"default":"/v/en.onnx","de":"/v/de.onnx"}}`)
	if err != nil {
		t.Fatalf("valid voices rejected: %v", err)
	}
	if cfg.Channels.Telegram.TextToSpeech.Voices["de"] != "/v/de.onnx" {
		t.Fatalf("voices map not applied: %+v", cfg.Channels.Telegram.TextToSpeech.Voices)
	}
}

func TestTTSVoicesKeysValidated(t *testing.T) {
	_, err := writeTTSConfig(t, `{"enabled":true,"voices":{"default":"/v/en.onnx","not a lang":"/v/x.onnx"}}`)
	if err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("expected invalid-key error, got: %v", err)
	}
	_, err = writeTTSConfig(t, `{"enabled":true,"voices":{"default":"/v/en.onnx","de":""}}`)
	if err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("expected empty-path error, got: %v", err)
	}
}

func TestTTSDisabledWithoutVoicesIsValid(t *testing.T) {
	cfg, err := writeTTSConfig(t, `{"enabled":false}`)
	if err != nil {
		t.Fatalf("disabled TTS with no voices must load: %v", err)
	}
	if cfg.Channels.Telegram.TextToSpeech.MaxChars != DefaultTelegramTTSMaxChars {
		t.Fatalf("defaults not applied: %+v", cfg.Channels.Telegram.TextToSpeech)
	}
}
