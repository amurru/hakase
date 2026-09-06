// Package state persists communication-channel state: paired users, per-chat
// bindings (session, notify flag), and the pending pairing code. The file
// lives at ~/.hakase/channels.json (0600) and is sandbox-denied like the
// other hakase secret files. The package is intentionally a leaf (only
// internal/config and internal/util) so the CLI's `hakase channels` command
// can inspect and mutate the state without importing the channel runtime.
package state

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"
)

// FileName is the state file name under the hakase home.
const FileName = "channels.json"

// PairedUser is one successfully paired channel user.
type PairedUser struct {
	Channel  string    `json:"channel"` // "telegram"
	UserID   int64     `json:"user_id"`
	Username string    `json:"username,omitempty"`
	PairedAt time.Time `json:"paired_at"`
}

// Chat is the per-chat binding: which hakase session the chat talks to and
// whether lifecycle pushes (cron/task completions) are delivered to it.
type Chat struct {
	SessionID string `json:"session_id,omitempty"`
	Notify    bool   `json:"notify,omitempty"`
}

// PendingPairing is a generated pairing code awaiting use.
type PendingPairing struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// State is the on-disk document.
type State struct {
	PairedUsers    []PairedUser    `json:"paired_users,omitempty"`
	Chats          map[string]Chat `json:"chats,omitempty"`
	PendingPairing *PendingPairing `json:"pending_pairing,omitempty"`
}

// ChatKey builds the canonical per-chat state key: "<channel>:<chatID>".
func ChatKey(channel string, chatID int64) string {
	return channel + ":" + strconv.FormatInt(chatID, 10)
}

// GenerateCode returns a random 6-digit pairing code (as a string, so a
// leading zero is preserved).
func GenerateCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		// Crypto/rand failing means the system entropy source is broken;
		// refuse to issue a predictable code.
		panic("state: cannot generate pairing code: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// DefaultPath returns ~/.hakase/channels.json (or $HAKASE_HOME/channels.json).
func DefaultPath() string {
	home := config.HakaseHome()
	if home == "" {
		return FileName
	}
	return filepath.Join(home, FileName)
}

// Store is a cross-process-safe accessor for the state file. Reads are served
// from an in-memory cache refreshed on every Update; writes take an exclusive
// flock and reload from disk first, so CLI processes (`hakase channels
// pair-code`) and the web server can mutate the file without lost updates.
type Store struct {
	mu    sync.Mutex
	path  string
	cache *State
}

// Open loads the state file (or starts empty when missing) and returns a
// store rooted at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	st, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	s.cache = st
	return s, nil
}

// OpenDefault opens the store at DefaultPath().
func OpenDefault() (*Store, error) {
	return Open(DefaultPath())
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Get returns a deep copy of the cached state. It reflects the last Update
// performed by this process or loaded at Open.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.cache)
}

// Update applies fn to the on-disk state under an exclusive flock and
// refreshes the cache. fn may reject the mutation by returning an error, in
// which case nothing is written.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := loadFile(s.path)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	if err := saveFile(s.path, st); err != nil {
		return err
	}
	s.cache = st
	return nil
}

func loadFile(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("channels: cannot read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		// A corrupt state file must not wedge the subsystem; start empty and
		// let the next Update rewrite it (the pairing/user list is cheap to
		// re-establish, and silently dropping it is safer than refusing to
		// boot the web server).
		return &State{}, nil
	}
	return &st, nil
}

func saveFile(path string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	lockFile := path + ".lock"

	// Exclusive flock for cross-process safety, mirroring cronjobs.json.
	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := util.FlockExclusive(lf); err != nil {
		return err
	}
	defer util.FlockUnlock(lf)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func cloneState(st *State) State {
	if st == nil {
		return State{}
	}
	out := State{
		Chats: make(map[string]Chat, len(st.Chats)),
	}
	out.PairedUsers = append(out.PairedUsers, st.PairedUsers...)
	for k, v := range st.Chats {
		out.Chats[k] = v
	}
	if st.PendingPairing != nil {
		pp := *st.PendingPairing
		out.PendingPairing = &pp
	}
	return out
}
