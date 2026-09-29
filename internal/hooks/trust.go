package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/util"
)

// TrustStoreFile is the trust-store filename inside the hakase home.
const TrustStoreFile = "hooks-trust.json"

// TrustChecker answers whether a hook fingerprint is trusted. The runner
// depends on this interface (not the file store) so tests can substitute a
// map and a nil checker means "trust nothing".
type TrustChecker interface {
	Trusted(fingerprint string) bool
}

// TrustEntry is one trusted hook: the fingerprint IS the identity (content
// hash over resolved argv + local script bytes — never the handler name,
// which gemini-cli#27900 showed is forgeable). Everything else is audit
// context shown back to the user.
type TrustEntry struct {
	Fingerprint string   `json:"fingerprint"`
	Name        string   `json:"name,omitempty"`
	Event       string   `json:"event,omitempty"`
	Matcher     string   `json:"matcher,omitempty"`
	Command     []string `json:"command,omitempty"`
	FirstSeen   string   `json:"first_seen,omitempty"`
	TrustedAt   string   `json:"trusted_at,omitempty"`
}

// DefaultTrustStorePath returns ~/.hakase/hooks-trust.json (HAKASE_HOME
// honored), or "" when no home resolves. Implemented here (not via
// internal/config, which imports this package) to avoid an import cycle.
func DefaultTrustStorePath() string {
	var home string
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		home = h
	} else if h, err := os.UserHomeDir(); err == nil {
		home = filepath.Join(h, ".hakase")
	} else {
		return ""
	}
	return filepath.Join(home, TrustStoreFile)
}

// TrustStore is the file-backed content-hash trust store for project hooks
// (spec HK-102). Reads are mtime-cached so per-tool-call trust checks stay
// cheap while a mid-session `hakase hooks trust` takes effect on the next
// call without a restart. Writes are atomic renames under a .lock flock.
type TrustStore struct {
	path string

	mu      sync.Mutex
	mtime   time.Time
	size    int64
	entries map[string]TrustEntry
	loaded  bool
}

// OpenTrustStore opens (creating nothing yet) the store at path. An empty
// path yields a store that trusts nothing and refuses writes with an
// actionable error.
func OpenTrustStore(path string) *TrustStore {
	return &TrustStore{path: path, entries: map[string]TrustEntry{}}
}

// OpenDefaultTrustStore opens the store at DefaultTrustStorePath().
func OpenDefaultTrustStore() *TrustStore {
	return OpenTrustStore(DefaultTrustStorePath())
}

// Path returns the backing file path (possibly "").
func (s *TrustStore) Path() string { return s.path }

// Trusted reports whether fp is currently trusted, reloading the file when
// it changed under us. Never errors: an unreadable store trusts nothing
// (fail-closed is the only safe direction for a trust read).
func (s *TrustStore) Trusted(fp string) bool {
	if s == nil || fp == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChangedLocked()
	_, ok := s.entries[fp]
	return ok
}

// List returns every trusted entry, sorted by fingerprint for stable output.
func (s *TrustStore) List() []TrustEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChangedLocked()
	out := make([]TrustEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

// FindByPrefix returns trusted entries whose fingerprint starts with prefix
// (empty prefix matches all). Used by `hakase hooks untrust`.
func (s *TrustStore) FindByPrefix(prefix string) []TrustEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChangedLocked()
	var out []TrustEntry
	for fp, e := range s.entries {
		if strings.HasPrefix(fp, prefix) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

// Trust records fp as trusted (idempotent) with audit context. There is no
// auto-trust path: only an explicit `hakase hooks trust` call reaches here.
func (s *TrustStore) Trust(e TrustEntry) error {
	if s == nil {
		return fmt.Errorf("no trust store (no hakase home)")
	}
	if s.path == "" {
		return fmt.Errorf("cannot trust %s: no hakase home resolves (set HAKASE_HOME)", e.Fingerprint)
	}
	if e.Fingerprint == "" {
		return fmt.Errorf("cannot trust an empty fingerprint")
	}
	if e.TrustedAt == "" {
		e.TrustedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockLocked()
	if err != nil {
		return err
	}
	defer unlock()
	// Re-read under the lock so a concurrent writer's entries survive.
	entries, _, _, rerr := s.readLocked()
	if rerr != nil {
		return rerr
	}
	entries[e.Fingerprint] = e
	return s.writeLocked(entries)
}

// Untrust removes fp, reporting whether anything was removed.
func (s *TrustStore) Untrust(fp string) (bool, error) {
	if s == nil || s.path == "" {
		return false, fmt.Errorf("no trust store (no hakase home)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockLocked()
	if err != nil {
		return false, err
	}
	defer unlock()
	entries, _, _, rerr := s.readLocked()
	if rerr != nil {
		return false, rerr
	}
	if _, ok := entries[fp]; !ok {
		return false, nil
	}
	delete(entries, fp)
	return true, s.writeLocked(entries)
}

// reloadIfChangedLocked re-reads the file when its mtime/size moved.
// Caller must hold s.mu.
func (s *TrustStore) reloadIfChangedLocked() {
	if s.path == "" {
		return
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		// Deleted (or never created): trust nothing. Keep loaded=true so
		// a re-created file is picked up via the mtime path... a missing
		// file has no mtime, so reset the cache markers to force a re-read
		// attempt next time.
		if os.IsNotExist(err) {
			if len(s.entries) != 0 || s.loaded {
				s.entries = map[string]TrustEntry{}
				s.loaded = false
				s.mtime = time.Time{}
				s.size = -1
			}
			return
		}
		return // transient stat failure: keep the last good view
	}
	if s.loaded && fi.ModTime().Equal(s.mtime) && fi.Size() == s.size {
		return
	}
	entries, mtime, size, err := s.readLocked()
	if err != nil {
		return // transient read failure: keep the last good view
	}
	s.entries, s.mtime, s.size, s.loaded = entries, mtime, size, true
}

// trustDisk is the on-disk shape (versioned for forward migration room).
type trustDisk struct {
	Version int                   `json:"version"`
	Hooks   map[string]TrustEntry `json:"hooks"`
}

// readLocked parses the file, returning entries plus cache markers.
// A missing file yields an empty set (not an error).
func (s *TrustStore) readLocked() (map[string]TrustEntry, time.Time, int64, error) {
	out := map[string]TrustEntry{}
	fi, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, time.Time{}, -1, nil
		}
		return nil, time.Time{}, 0, fmt.Errorf("stat trust store: %v", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("read trust store: %v", err)
	}
	var disk trustDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("invalid trust store %s: %v (delete it to start over, or restore from backup)", s.path, err)
	}
	for fp, e := range disk.Hooks {
		e.Fingerprint = fp // the map key is authoritative
		out[fp] = e
	}
	return out, fi.ModTime(), fi.Size(), nil
}

// writeLocked atomically replaces the file (0600) and refreshes the cache.
// Caller must hold s.mu and the flock.
func (s *TrustStore) writeLocked(entries map[string]TrustEntry) error {
	data, err := json.MarshalIndent(trustDisk{Version: 1, Hooks: entries}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create trust store dir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, "hooks-trust-*.tmp")
	if err != nil {
		return fmt.Errorf("write trust store: %v", err)
	}
	tmpName := tmp.Name()
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write trust store: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write trust store: %v", err)
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write trust store: %v", err)
	}
	s.entries = entries
	if fi, err := os.Stat(s.path); err == nil {
		s.mtime, s.size, s.loaded = fi.ModTime(), fi.Size(), true
	}
	return nil
}

// lockLocked takes the adjacent .lock flock (audit-log discipline).
// Caller must hold s.mu; the returned func releases.
func (s *TrustStore) lockLocked() (func(), error) {
	nop := func() {}
	if s.path == "" {
		return nop, fmt.Errorf("no trust store path")
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nop, fmt.Errorf("lock trust store: %v", err)
	}
	if err := util.FlockExclusive(f); err != nil {
		_ = f.Close()
		return nop, fmt.Errorf("lock trust store: %v", err)
	}
	return func() {
		_ = util.FlockUnlock(f)
		_ = f.Close()
	}, nil
}
