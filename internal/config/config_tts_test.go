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
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","text_to_speech":` + tts + `}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

func TestTTSVoicesDefaultRequiredWhenEnabled(t *testing.T) {
	// Enabled TTS without a "default" voice is a load error (fail fast —
	// every voice reply would otherwise degrade to hints).
	_, err := writeTTSConfig(t, `{"enabled":true}`)
	if err == nil || !strings.Contains(err.Error(), "voices") || !strings.Contains(err.Error(), "default") {
		t.Fatalf("enabled without voices.default must fail loudly, got: %v", err)
	}

	cfg, err := writeTTSConfig(t, `{"enabled":true,"voices":{"default":"/v/en.onnx","de":"/v/de.onnx"}}`)
	if err != nil {
		t.Fatalf("valid voices rejected: %v", err)
	}
	if cfg.TextToSpeech.Voices["de"] != "/v/de.onnx" {
		t.Fatalf("voices map not applied: %+v", cfg.TextToSpeech.Voices)
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
	if cfg.TextToSpeech.MaxChars != DefaultTelegramTTSMaxChars {
		t.Fatalf("defaults not applied: %+v", cfg.TextToSpeech)
	}
}
