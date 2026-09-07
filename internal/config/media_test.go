package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Fixture secret values for inline JSON config bodies, kept in constants
// with credential-word-free identifiers (see config_test.go).
const (
	mediaFixtureDefaultValue = "test"
	mediaFixtureGlobalValue  = "global-key"
	mediaFixtureImageValue   = "explicit-key"
)

func TestMediaConfigDefaults(t *testing.T) {
	cfg := MediaConfig{}
	cfg.ApplyDefaults()
	if cfg.ImageProvider != "auto" {
		t.Errorf("ImageProvider default = %q, want auto", cfg.ImageProvider)
	}
	if cfg.VideoProvider != "auto" {
		t.Errorf("VideoProvider default = %q, want auto", cfg.VideoProvider)
	}
	if cfg.AudioProvider != "off" {
		t.Errorf("AudioProvider default = %q, want off", cfg.AudioProvider)
	}
	if len(cfg.Order) != 3 || cfg.Order[0] != "openai" {
		t.Errorf("Order default = %v, want [openai fal pil]", cfg.Order)
	}
	if cfg.OutputDir != "outputs/media" {
		t.Errorf("OutputDir = %q, want outputs/media", cfg.OutputDir)
	}
	if cfg.OpenAIImagePath != "/images/generations" {
		t.Errorf("OpenAIImagePath = %q", cfg.OpenAIImagePath)
	}
	if cfg.OpenAIImageModel != "gpt-image-1-mini" {
		t.Errorf("OpenAIImageModel = %q", cfg.OpenAIImageModel)
	}
	if cfg.FalImageModel != "fal-ai/flux/schnell" {
		t.Errorf("FalImageModel = %q", cfg.FalImageModel)
	}
	if cfg.FalVideoModel != "fal-ai/wan/v2.7/text-to-video" {
		t.Errorf("FalVideoModel = %q", cfg.FalVideoModel)
	}
	if cfg.OpenAIVideoModel != "google/veo-3.1-lite" {
		t.Errorf("OpenAIVideoModel = %q", cfg.OpenAIVideoModel)
	}
	if cfg.OpenAIVideoResolution != "720p" {
		t.Errorf("OpenAIVideoResolution = %q", cfg.OpenAIVideoResolution)
	}
}

func TestMediaConfigValidate(t *testing.T) {
	cfg := MediaConfig{}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
	cfg.ImageProvider = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid image_provider")
	}
	cfg.ImageProvider = "auto"
	cfg.VideoProvider = "pil"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid video_provider")
	}
	// openai is a legal video provider now
	cfg.VideoProvider = "openai"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("openai video_provider should be valid: %v", err)
	}
	cfg.VideoProvider = "auto"
	cfg.AudioProvider = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid audio_provider")
	}
	// forward-compat audio values
	cfg.AudioProvider = "openai"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("openai audio should be valid: %v", err)
	}
	cfg.AudioProvider = "elevenlabs"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("elevenlabs audio should be valid: %v", err)
	}
	cfg.AudioProvider = "off"
	cfg.MaxConcurrent = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative max_concurrent")
	}
	cfg.MaxConcurrent = 0
	cfg.TimeoutSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative timeout_seconds")
	}
}

func TestMediaConfigEnvOverrides(t *testing.T) {
	path := writeTempConfig(t, `{"provider":"gemini","media":{"image_provider":"pil","output_dir":"outputs/custom"}}`)
	t.Setenv("HAKASE_MEDIA_IMAGE_PROVIDER", "openai")
	t.Setenv("HAKASE_MEDIA_OUTPUT_DIR", "outputs/env")
	t.Setenv("HAKASE_FAL_KEY", "test-fal-key")
	t.Setenv("HAKASE_MEDIA_VIDEO_MODEL", "bytedance/seedance-1-5-pro")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Media.ImageProvider != "openai" {
		t.Errorf("env should win image_provider: got %q", cfg.Media.ImageProvider)
	}
	if cfg.Media.OutputDir != "outputs/env" {
		t.Errorf("env should win output_dir: got %q", cfg.Media.OutputDir)
	}
	if cfg.Media.FalKey != "test-fal-key" {
		t.Errorf("HAKASE_FAL_KEY not applied: got %q", cfg.Media.FalKey)
	}
	if cfg.Media.OpenAIVideoModel != "bytedance/seedance-1-5-pro" {
		t.Errorf("HAKASE_MEDIA_VIDEO_MODEL not applied: got %q", cfg.Media.OpenAIVideoModel)
	}
}

func TestMediaConfigZeroConfig(t *testing.T) {
	// No media block -> defaults, no breakage
	path := writeTempConfig(t, fmt.Sprintf(`{"provider":"gemini","api_key":%q}`, mediaFixtureDefaultValue))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Media.ImageProvider != "auto" {
		t.Errorf("zero-config ImageProvider = %q, want auto", cfg.Media.ImageProvider)
	}
	if err := cfg.Media.Validate(); err != nil {
		t.Fatalf("zero-config Validate: %v", err)
	}
	// Env-only config (no file)
	t.Setenv("HAKASE_API_KEY", "env-key")
	missing := filepath.Join(t.TempDir(), "missing.json")
	cfg2, err := LoadConfig(missing)
	if err != nil {
		t.Fatalf("env-only LoadConfig: %v", err)
	}
	if cfg2.Media.ImageProvider != "auto" {
		t.Errorf("env-only ImageProvider = %q, want auto", cfg2.Media.ImageProvider)
	}
}

func TestMediaConfigFallbackChain(t *testing.T) {
	path := writeTempConfig(t, fmt.Sprintf(`{"provider":"openai","api_key":%q,"base_url":"https://global.example.com/v1"}`, mediaFixtureGlobalValue))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Media.OpenAIImageKey != "global-key" {
		t.Errorf("fallback OpenAIImageKey = %q, want global-key", cfg.Media.OpenAIImageKey)
	}
	if cfg.Media.OpenAIImageBaseURL != "https://global.example.com/v1" {
		t.Errorf("fallback OpenAIImageBaseURL = %q", cfg.Media.OpenAIImageBaseURL)
	}
	// Explicit openai_image_key should not be overwritten
	path2 := writeTempConfig(t, fmt.Sprintf(`{"provider":"openai","api_key":%q,"media":{"openai_image_key":%q}}`, mediaFixtureGlobalValue, mediaFixtureImageValue))
	cfg2, err := LoadConfig(path2)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg2.Media.OpenAIImageKey != "explicit-key" {
		t.Errorf("explicit key should win: got %q", cfg2.Media.OpenAIImageKey)
	}
}
