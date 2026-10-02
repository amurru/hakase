package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDiscordConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscordChannelDisabledByDefault(t *testing.T) {
	path := writeDiscordConfig(t, `{}`)
	t.Setenv("HAKASE_DISCORD_ENABLED", "")
	t.Setenv("HAKASE_DISCORD_BOT_TOKEN", "")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Channels.Discord.EnabledWithToken() {
		t.Fatal("discord must be off unless explicitly enabled")
	}
	if err := cfg.Channels.Validate(); err != nil {
		t.Errorf("absent discord config must validate: %v", err)
	}
}

func TestDiscordChannelEnabledWithoutTokenFails(t *testing.T) {
	path := writeDiscordConfig(t, `{"channels":{"discord":{"enabled":true}}}`)
	t.Setenv("HAKASE_DISCORD_BOT_TOKEN", "")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("discord enabled without bot_token must fail validation")
	}
}

func TestDiscordChannelEnvOverrides(t *testing.T) {
	path := writeDiscordConfig(t, `{"provider":"openai","model_name":"m","api_key":"k"}`)
	t.Setenv("HAKASE_DISCORD_ENABLED", "true")
	t.Setenv("HAKASE_DISCORD_BOT_TOKEN", "dtok123")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	dc := cfg.Channels.Discord
	if !dc.EnabledWithToken() {
		t.Fatal("discord not enabled with token")
	}
	if dc.BotToken != "dtok123" {
		t.Errorf("bot token = %q", dc.BotToken)
	}
}

func TestDiscordChannelBadSnowflakeFails(t *testing.T) {
	path := writeDiscordConfig(t, `{"channels":{"discord":{"enabled":true,"bot_token":"t","allowed_user_ids":[-5]}}}`)
	t.Setenv("HAKASE_DISCORD_BOT_TOKEN", "")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("non-positive snowflake must fail validation")
	}
}

func TestDiscordChannelCoexistsWithTelegram(t *testing.T) {
	path := writeDiscordConfig(t, `{"channels":{"telegram":{"enabled":true,"bot_token":"t"},"discord":{"enabled":true,"bot_token":"d"}}}`)
	t.Setenv("HAKASE_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("HAKASE_DISCORD_BOT_TOKEN", "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("dual transport config must load: %v", err)
	}
	if !cfg.Channels.Telegram.EnabledWithToken() || !cfg.Channels.Discord.EnabledWithToken() {
		t.Fatal("both transports must be enabled with tokens")
	}
}
