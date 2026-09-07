// channels.go exposes communication-channel status and management over the
// web API, so channels can be paired, inspected, and revoked from the
// Settings UI instead of the server console. It reads/writes only the
// channels state file (internal/channel/state); the transport runtimes keep
// running independently - a revoke takes effect on the user's next message.
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/config"
)

// ChannelsRouter is the minimum interface needed by RegisterChannelsRoutes.
type ChannelsRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// ChannelsAPI serves channel status and management endpoints. store and
// running may be nil/absent when the subsystem is unavailable: status then
// reports config-only information and mutations return 503.
type ChannelsAPI struct {
	store *state.Store
	// running reports whether the channel service is live in this process;
	// nil means channels never started (disabled or failed at boot).
	running func() bool
}

// RegisterChannelsRoutes registers channel routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterChannelsRoutes(r ChannelsRouter, store *state.Store, running func() bool) {
	api := &ChannelsAPI{store: store, running: running}
	r.Get("/channels", api.Status)
	r.Post("/channels/pairing-code", api.PairingCode)
	r.Post("/channels/revoke", api.Revoke)
}

// pairedUserDTO mirrors state.PairedUser for the API.
type pairedUserDTO struct {
	Channel  string `json:"channel"`
	UserID   int64  `json:"user_id"`
	Username string `json:"username,omitempty"`
	PairedAt string `json:"paired_at"`
}

// pendingPairingDTO reports an outstanding pairing code without ever
// returning the code itself; the code is shown exactly once by
// POST /channels/pairing-code.
type pendingPairingDTO struct {
	ExpiresAt string `json:"expires_at"`
}

// chatDTO mirrors one state.Chat binding.
type chatDTO struct {
	ChatID    int64  `json:"chat_id"`
	SessionID string `json:"session_id,omitempty"`
	Notify    bool   `json:"notify"`
}

// channelStatusDTO is the management view of one transport.
type channelStatusDTO struct {
	// Enabled reflects the config file (enabled + token present). Config
	// changes need a server restart to take effect.
	Enabled     bool               `json:"enabled"`
	Running     bool               `json:"running"`
	PairedUsers []pairedUserDTO    `json:"paired_users"`
	Pending     *pendingPairingDTO `json:"pending_pairing,omitempty"`
	Chats       []chatDTO          `json:"chats"`
}

// Status handles GET /api/channels - the Settings UI's channel overview.
func (api *ChannelsAPI) Status(w http.ResponseWriter, r *http.Request) {
	enabled := api.telegramEnabled()
	running := api.running != nil && api.running()

	dto := channelStatusDTO{
		Enabled:     enabled,
		Running:     running,
		PairedUsers: []pairedUserDTO{},
		Chats:       []chatDTO{},
	}
	if api.store != nil {
		st := api.store.Get()
		for _, u := range st.PairedUsers {
			if u.Channel != "telegram" {
				continue
			}
			dto.PairedUsers = append(dto.PairedUsers, pairedUserDTO{
				Channel:  u.Channel,
				UserID:   u.UserID,
				Username: u.Username,
				PairedAt: u.PairedAt.UTC().Format(time.RFC3339),
			})
		}
		if pp := st.PendingPairing; pp != nil {
			dto.Pending = &pendingPairingDTO{ExpiresAt: pp.ExpiresAt.UTC().Format(time.RFC3339)}
		}
		for key, chat := range st.Chats {
			id, ok := strings.CutPrefix(key, "telegram:")
			if !ok {
				continue
			}
			chatID, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				continue
			}
			dto.Chats = append(dto.Chats, chatDTO{ChatID: chatID, SessionID: chat.SessionID, Notify: chat.Notify})
		}
	}
	writeJSON(w, http.StatusOK, map[string]channelStatusDTO{"telegram": dto})
}

// PairingCode handles POST /api/channels/pairing-code. It generates (or
// returns the still-valid) pairing code; the response is the only place the
// code is ever served - GET /channels deliberately reports only the expiry.
func (api *ChannelsAPI) PairingCode(w http.ResponseWriter, r *http.Request) {
	if api.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel state unavailable"})
		return
	}
	code, expires, err := state.EnsurePairingCodeWithExpiry(api.store, state.PairingCodeTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":        code,
		"expires_at":  expires.UTC().Format(time.RFC3339),
		"ttl_seconds": int(state.PairingCodeTTL.Seconds()),
	})
}

// Revoke handles POST /api/channels/revoke. Body: {user_id int64,
// channel string (optional, default "telegram")}. Removes the paired user;
// takes effect on their next message.
func (api *ChannelsAPI) Revoke(w http.ResponseWriter, r *http.Request) {
	if api.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel state unavailable"})
		return
	}
	var req struct {
		UserID  int64  `json:"user_id"`
		Channel string `json:"channel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if req.Channel == "" {
		req.Channel = "telegram"
	}
	removed := 0
	err := api.store.Update(func(st *state.State) error {
		kept := st.PairedUsers[:0]
		for _, u := range st.PairedUsers {
			if u.Channel == req.Channel && u.UserID == req.UserID {
				removed++
				continue
			}
			kept = append(kept, u)
		}
		st.PairedUsers = kept
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if removed == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no paired user matched"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "removed": removed})
}

// telegramEnabled resolves the config file's channels.telegram state the same
// way GetConfig does (missing file = config-from-env = still checked there).
func (api *ChannelsAPI) telegramEnabled() bool {
	path := resolveConfigPath("config.json")
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return false
	}
	return cfg.Channels.Telegram.EnabledWithToken()
}
