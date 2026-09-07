package channel

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/channel/state"
)

// PairingCodeTTL bounds how long a generated pairing code stays valid; the
// canonical definition (and the shared ensure helper) live in the state
// package so the CLI and web API can issue codes without importing this
// package.
const PairingCodeTTL = state.PairingCodeTTL

// denyReplyCooldown spaces the terse "unauthorized" reply so an unknown user
// cannot provoke unlimited bot traffic (or leak that the bot exists) by
// spamming messages. Mirrors Hermes' one-reminder-per-interval behavior.
const denyReplyCooldown = time.Hour

// Authenticator enforces deny-by-default channel access: a user is allowed
// when listed in the static config allowlist OR paired via a one-time code.
// Pairing codes live in the shared state file, so the CLI can issue them and
// the server honors them.
type Authenticator struct {
	store      *state.Store
	channel    string
	allowedIDs map[int64]bool
	staticCode string

	denyMu   sync.Mutex
	lastDeny map[int64]time.Time
}

// NewAuthenticator builds an authenticator for one transport.
func NewAuthenticator(store *state.Store, channel string, allowedIDs []int64, staticCode string) *Authenticator {
	ids := make(map[int64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		ids[id] = true
	}
	return &Authenticator{
		store:      store,
		channel:    channel,
		allowedIDs: ids,
		staticCode: strings.TrimSpace(staticCode),
		lastDeny:   map[int64]time.Time{},
	}
}

// AllowedIDs returns the static config allowlist (sorted for stable output).
func (a *Authenticator) AllowedIDs() []int64 {
	out := make([]int64, 0, len(a.allowedIDs))
	for id := range a.allowedIDs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsAllowed reports whether the user may use the channel.
func (a *Authenticator) IsAllowed(userID int64) bool {
	if a.allowedIDs[userID] {
		return true
	}
	for _, u := range a.store.Get().PairedUsers {
		if u.Channel == a.channel && u.UserID == userID {
			return true
		}
	}
	return false
}

// HasAnyUser reports whether any user (static or paired) can access the
// channel. When false, the transport should surface a pairing code.
func (a *Authenticator) HasAnyUser() bool {
	if len(a.allowedIDs) > 0 {
		return true
	}
	for _, u := range a.store.Get().PairedUsers {
		if u.Channel == a.channel {
			return true
		}
	}
	return false
}

// EnsurePairingCode returns the current usable pairing code: the static
// config code when set, otherwise the pending generated code, generating and
// persisting a fresh one when missing or expired.
func (a *Authenticator) EnsurePairingCode() (string, error) {
	if a.staticCode != "" {
		return a.staticCode, nil
	}
	return state.EnsurePairingCode(a.store, state.PairingCodeTTL)
}

// TryPair validates code and, on success, persists the paired user (clearing
// any pending code). Static config codes are also accepted.
func (a *Authenticator) TryPair(userID int64, username, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("empty pairing code")
	}
	if a.staticCode != "" && code == a.staticCode {
		return a.persistPairing(userID, username)
	}
	now := time.Now()
	err := a.store.Update(func(st *state.State) error {
		pp := st.PendingPairing
		if pp == nil || pp.Code != code || !now.Before(pp.ExpiresAt) {
			return fmt.Errorf("invalid or expired pairing code")
		}
		st.PendingPairing = nil
		st.PairedUsers = appendPairedUser(st.PairedUsers, a.channel, userID, username, now)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// DenyReplyAllowed reports whether the bot may send the unauthorized reply
// to this user right now (at most one per cooldown, per user).
func (a *Authenticator) DenyReplyAllowed(userID int64) bool {
	a.denyMu.Lock()
	defer a.denyMu.Unlock()
	if last, ok := a.lastDeny[userID]; ok && time.Since(last) < denyReplyCooldown {
		return false
	}
	a.lastDeny[userID] = time.Now()
	return true
}

func (a *Authenticator) persistPairing(userID int64, username string) error {
	return a.store.Update(func(st *state.State) error {
		st.PendingPairing = nil
		st.PairedUsers = appendPairedUser(st.PairedUsers, a.channel, userID, username, time.Now())
		return nil
	})
}

func appendPairedUser(users []state.PairedUser, channel string, userID int64, username string, at time.Time) []state.PairedUser {
	for i := range users {
		if users[i].Channel == channel && users[i].UserID == userID {
			users[i].Username = username
			users[i].PairedAt = at
			return users
		}
	}
	return append(users, state.PairedUser{
		Channel:  channel,
		UserID:   userID,
		Username: username,
		PairedAt: at,
	})
}
