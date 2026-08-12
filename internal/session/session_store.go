package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionStore handles persisting sessions as JSON files in a local directory.
type SessionStore struct {
	mu          sync.RWMutex
	sessionsDir string
}

// NewSessionStore creates a SessionStore backed by the given directory.
// The directory is created if it does not exist. Existing session files
// written before the 0600/0700 hardening are chmod'd on startup (best-effort).
func NewSessionStore(sessionsDir string) (*SessionStore, error) {
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}
	store := &SessionStore{
		sessionsDir: sessionsDir,
	}
	store.migrateSessionPermissions()
	return store, nil
}

// migrateSessionPermissions fixes permissions on session files created before
// the 0600/0700 hardening. Session files written with the old 0644 mode are
// chmod'd to 0600, and the sessions directory is tightened to 0700 if it was
// created with looser permissions (e.g. 0755). Best-effort: failures are
// logged and never abort startup.
func (s *SessionStore) migrateSessionPermissions() {
	if err := os.Chmod(s.sessionsDir, 0700); err != nil {
		log.Printf("session store: failed to chmod sessions dir %s to 0700: %v", s.sessionsDir, err)
	}

	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		log.Printf("session store: failed to list sessions dir %s for permission migration: %v", s.sessionsDir, err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileExt) {
			continue
		}
		path := filepath.Join(s.sessionsDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			log.Printf("session store: failed to stat %s for permission migration: %v", path, err)
			continue
		}
		if info.Mode().Perm() != 0600 {
			if err := os.Chmod(path, 0600); err != nil {
				log.Printf("session store: failed to chmod %s to 0600: %v", path, err)
			}
		}
	}
}

// Save writes a session to disk as a JSON file.
func (s *SessionStore) Save(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := filepath.Join(s.sessionsDir, session.ID+FileExt)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	return nil
}

// Load reads a session from disk by its ID.
func (s *SessionStore) Load(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := filepath.Join(s.sessionsDir, id+FileExt)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return nil, fmt.Errorf("failed to read session %s: %w", id, err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session %s: %w", id, err)
	}
	return &session, nil
}

// List returns all non-archived sessions sorted by updated_at descending.
func (s *SessionStore) List() ([]SessionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions directory: %w", err)
	}

	var summaries []SessionSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileExt) {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), FileExt)
		session, err := s.loadUnlocked(id)
		if err != nil {
			// Skip corrupted or unreadable files
			continue
		}
		if session.Archived {
			continue
		}
		summaries = append(summaries, session.Summary())
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})

	return summaries, nil
}

// ListArchived returns all archived sessions sorted by updated_at descending.
func (s *SessionStore) ListArchived() ([]SessionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions directory: %w", err)
	}

	var summaries []SessionSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileExt) {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), FileExt)
		session, err := s.loadUnlocked(id)
		if err != nil {
			continue
		}
		if !session.Archived {
			continue
		}
		summaries = append(summaries, session.Summary())
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})

	return summaries, nil
}

// Delete removes a session file from disk.
func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.sessionsDir, id+FileExt)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session %s not found", id)
		}
		return fmt.Errorf("failed to delete session %s: %w", id, err)
	}
	return nil
}

// Archive sets the archived flag on a session and saves it.
func (s *SessionStore) Archive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	session.Archived = true
	session.UpdatedAt = time.Now().UTC()
	return s.saveUnlocked(session)
}

// Unarchive clears the archived flag on a session and saves it.
func (s *SessionStore) Unarchive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	session.Archived = false
	session.UpdatedAt = time.Now().UTC()
	return s.saveUnlocked(session)
}

// loadUnlocked reads a session from disk without acquiring the lock.
// The caller must hold the appropriate lock (RLock for read, Lock for write).
func (s *SessionStore) loadUnlocked(id string) (*Session, error) {
	path := filepath.Join(s.sessionsDir, id+FileExt)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return nil, fmt.Errorf("failed to read session %s: %w", id, err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session %s: %w", id, err)
	}
	normalizeLegacyMessages(&session)
	return &session, nil
}

// normalizeLegacyMessages marks messages written by a pre-context-management
// version of hakase as in-context. Older session files have no in_context
// field, so every message unmarshals to false; without this pass those
// sessions would silently lose all history on resume. Sessions written by the
// context-management version always keep the tail in-context, so a session
// where every message is out-of-context can only be a legacy file.
func normalizeLegacyMessages(session *Session) {
	if len(session.Messages) == 0 {
		return
	}
	for _, msg := range session.Messages {
		if msg.InContext {
			return // already context-managed; leave as-is
		}
	}
	for i := range session.Messages {
		session.Messages[i].InContext = true
	}
}

// saveUnlocked writes a session to disk without acquiring the lock.
// The caller must hold the write lock.
func (s *SessionStore) saveUnlocked(session *Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := filepath.Join(s.sessionsDir, session.ID+FileExt)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	return nil
}
