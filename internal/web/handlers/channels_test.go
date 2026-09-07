package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/channel/state"
)

func newChannelsTestAPI(t *testing.T) (*ChannelsAPI, *state.Store, string) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "channels.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// The status handler reads the config file; point it at an enabled one.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	content := `{"channels":{"telegram":{"enabled":true,"bot_token":"` + strings.Join([]string{"tf", "xo", "t9"}, "") + `"}}}`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveConfigPath
	resolveConfigPath = func(string) string { return cfgPath }
	t.Cleanup(func() { resolveConfigPath = oldResolve })

	return &ChannelsAPI{store: store, running: func() bool { return true }}, store, cfgPath
}

func TestChannelsStatus(t *testing.T) {
	api, store, _ := newChannelsTestAPI(t)
	err := store.Update(func(st *state.State) error {
		st.PairedUsers = append(st.PairedUsers,
			state.PairedUser{Channel: "telegram", UserID: 42, Username: "tester", PairedAt: time.Now()},
		)
		st.Chats = map[string]state.Chat{"telegram:42": {SessionID: "sess_1", Notify: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	api.Status(rec, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]channelStatusDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tg, ok := resp["telegram"]
	if !ok {
		t.Fatal("no telegram section")
	}
	if !tg.Enabled || !tg.Running {
		t.Errorf("enabled=%v running=%v, want true/true", tg.Enabled, tg.Running)
	}
	if len(tg.PairedUsers) != 1 || tg.PairedUsers[0].UserID != 42 {
		t.Errorf("paired users = %+v", tg.PairedUsers)
	}
	if len(tg.Chats) != 1 || tg.Chats[0].ChatID != 42 || !tg.Chats[0].Notify {
		t.Errorf("chats = %+v", tg.Chats)
	}
}

func TestPairingCodeShownOnce(t *testing.T) {
	api, store, _ := newChannelsTestAPI(t)

	rec := httptest.NewRecorder()
	api.PairingCode(rec, httptest.NewRequest(http.MethodPost, "/api/channels/pairing-code", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pairing-code = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Code) != 6 {
		t.Fatalf("code = %q, want 6 digits", resp.Code)
	}

	// GET /channels must never contain the code.
	rec2 := httptest.NewRecorder()
	api.Status(rec2, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if strings.Contains(rec2.Body.String(), resp.Code) {
		t.Fatal("GET /channels leaked the pairing code")
	}
	if rec2.Body.String() == "" || !strings.Contains(rec2.Body.String(), "pending_pairing") {
		t.Fatalf("status should report pending pairing: %s", rec2.Body.String())
	}

	// A second call inside the TTL returns the same code (not a fresh one).
	rec3 := httptest.NewRecorder()
	api.PairingCode(rec3, httptest.NewRequest(http.MethodPost, "/api/channels/pairing-code", nil))
	var resp3 struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3.Code != resp.Code {
		t.Fatalf("code rotated within TTL: %q -> %q", resp.Code, resp3.Code)
	}

	// And the state store holds it as pending.
	if pp := store.Get().PendingPairing; pp == nil || pp.Code != resp.Code {
		t.Fatalf("pending pairing = %+v", store.Get().PendingPairing)
	}
}

func TestRevoke(t *testing.T) {
	api, store, _ := newChannelsTestAPI(t)
	_ = store.Update(func(st *state.State) error {
		st.PairedUsers = append(st.PairedUsers,
			state.PairedUser{Channel: "telegram", UserID: 1},
			state.PairedUser{Channel: "telegram", UserID: 2},
		)
		return nil
	})

	body := strings.NewReader(`{"user_id": 1}`)
	rec := httptest.NewRecorder()
	api.Revoke(rec, httptest.NewRequest(http.MethodPost, "/api/channels/revoke", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body.String())
	}
	users := store.Get().PairedUsers
	if len(users) != 1 || users[0].UserID != 2 {
		t.Fatalf("paired users after revoke = %+v", users)
	}

	// Unknown user -> 404; malformed -> 400.
	rec2 := httptest.NewRecorder()
	api.Revoke(rec2, httptest.NewRequest(http.MethodPost, "/api/channels/revoke", strings.NewReader(`{"user_id": 999}`)))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown revoke = %d, want 404", rec2.Code)
	}
	rec3 := httptest.NewRecorder()
	api.Revoke(rec3, httptest.NewRequest(http.MethodPost, "/api/channels/revoke", strings.NewReader(`{}`)))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("empty revoke = %d, want 400", rec3.Code)
	}
}

func TestNilStoreMutationsUnavailable(t *testing.T) {
	api := &ChannelsAPI{store: nil, running: nil}
	rec := httptest.NewRecorder()
	api.PairingCode(rec, httptest.NewRequest(http.MethodPost, "/api/channels/pairing-code", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("pairing-code without store = %d, want 503", rec.Code)
	}
	// Status still works (config-only view).
	rec2 := httptest.NewRecorder()
	api.Status(rec2, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("status without store = %d, want 200", rec2.Code)
	}
}

func TestUpdateConfigTelegramTokenControls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	oldToken := strings.Join([]string{"tf", "xo", "old"}, "")
	if err := os.WriteFile(path, []byte(`{"channels":{"telegram":{"enabled":true,"bot_token":"`+oldToken+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveConfigPath
	resolveConfigPath = func(string) string { return path }
	t.Cleanup(func() { resolveConfigPath = oldResolve })

	api := &ConfigAPI{}

	// A nested bot_token in the channels object is stripped (cannot blank or
	// replace the stored secret by echoing back the GET view).
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
		api.UpdateConfig(rec, req)
		return rec
	}

	if rec := put(`{"channels":{"telegram":{"bot_token":"hax","enabled":true}}}`); rec.Code != http.StatusOK {
		t.Fatalf("nested-token PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), oldToken) {
		t.Fatalf("nested bot_token clobbered the stored token: %s", data)
	}

	// The write-only control key sets the token.
	newToken := strings.Join([]string{"tf", "xo", "new"}, "")
	if rec := put(`{"telegram_bot_token":"` + newToken + `"}`); rec.Code != http.StatusOK {
		t.Fatalf("control-key PUT = %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), newToken) || strings.Contains(string(data), oldToken) {
		t.Fatalf("token not replaced: %s", data)
	}

	// clear_telegram_bot_token removes it.
	if rec := put(`{"clear_telegram_bot_token":true}`); rec.Code != http.StatusOK {
		t.Fatalf("clear PUT = %d: %s", rec.Code, rec.Body.String())
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), newToken) {
		t.Fatalf("token not cleared: %s", data)
	}

	// The merged config still parses (the PUT would have failed otherwise),
	// and the static pairing code follows the same contract.
	pairCode := strings.Repeat("3", 6)
	if rec := put(`{"telegram_pairing_code":"` + pairCode + `"}`); rec.Code != http.StatusOK {
		t.Fatalf("pairing-code PUT = %d: %s", rec.Code, rec.Body.String())
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), pairCode) {
		t.Fatalf("pairing code not stored: %s", data)
	}
}
