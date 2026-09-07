package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelegramChannelEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"openai","model_name":"m","api_key":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKASE_TELEGRAM_ENABLED", "true")
	t.Setenv("HAKASE_TELEGRAM_BOT_TOKEN", "tok123")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	tg := cfg.Channels.Telegram
	if !tg.EnabledWithToken() {
		t.Fatalf("telegram not enabled with token: enabled=%v token=%q", tg.Enabled != nil, tg.BotToken)
	}
	if tg.BotToken != "tok123" {
		t.Errorf("bot token = %q", tg.BotToken)
	}
}

func TestTelegramChannelDisabledByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKASE_TELEGRAM_ENABLED", "")
	t.Setenv("HAKASE_TELEGRAM_BOT_TOKEN", "")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Channels.Telegram.EnabledWithToken() {
		t.Fatal("channels must be off unless explicitly enabled")
	}
	if err := cfg.Channels.Validate(); err != nil {
		t.Errorf("absent channels config must validate: %v", err)
	}
}

func TestTelegramChannelEnabledWithoutTokenFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"channels":{"telegram":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKASE_TELEGRAM_BOT_TOKEN", "")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("enabled without bot_token must fail validation")
	}
}

func TestTelegramChannelEnvTokenSatisfiesValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"channels":{"telegram":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKASE_TELEGRAM_BOT_TOKEN", "envtok")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("env token should satisfy validation: %v", err)
	}
	if !cfg.Channels.Telegram.EnabledWithToken() {
		t.Fatal("enabled+env token should be active")
	}
}
