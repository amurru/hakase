package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetConfigBlanksChannelAndSidekickSecrets guards the has_*/blanked
// contract for the channel secrets: GET /api/config must never return the
// Telegram bot token, the static pairing code, or the sidekick API key -
// only booleans reporting that they are set. Fixture values are composed at
// runtime so no credential-looking literals live in source.
func TestGetConfigBlanksChannelAndSidekickSecrets(t *testing.T) {
	botToken := strings.Join([]string{"tf", "xo", "t1"}, "")   // fake token
	pairingCode := strings.Repeat("4", 6)                      // fake code
	sidekickKey := strings.Join([]string{"sf", "k", "x1"}, "") // fake key
	primaryKey := strings.Join([]string{"pf", "k", "x1"}, "")  // fake key

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
		"provider": "openai",
		"model_name": "test-model",
		"api_key": "` + primaryKey + `",
		"channels": {"telegram": {"enabled": true, "bot_token": "` + botToken + `", "pairing_code": "` + pairingCode + `"}},
		"sidekick": {"enabled": true, "model_name": "m", "api_key": "` + sidekickKey + `"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveConfigPath
	resolveConfigPath = func(string) string { return path }
	t.Cleanup(func() { resolveConfigPath = oldResolve })

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	(&ConfigAPI{}).GetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d: %s", rec.Code, rec.Body.String())
	}

	var resp ConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for name, flag := range map[string]bool{
		"has_api_key":               resp.HasAPIKey,
		"has_telegram_bot_token":    resp.HasTelegramBotToken,
		"has_telegram_pairing_code": resp.HasTelegramPairingCode,
		"has_sidekick_api_key":      resp.HasSidekickAPIKey,
	} {
		if !flag {
			t.Errorf("%s = false, want true", name)
		}
	}

	body := rec.Body.String()
	for _, secret := range []string{botToken, pairingCode, sidekickKey, primaryKey} {
		if strings.Contains(body, secret) {
			t.Errorf("GET /api/config leaked secret %q", secret)
		}
	}
}
