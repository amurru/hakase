package main

import (
	"encoding/json"
	"fmt"
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
// The directory is created if it does not exist.
func NewSessionStore(sessionsDir string) (*SessionStore, error) {
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}
	return &SessionStore{
		sessionsDir: sessionsDir,
	}, nil
}

// Save writes a session to disk as a JSON file.
func (s *SessionStore) Save(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := session.FilePath()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	return nil
}

// Load reads a session from disk by its ID.
func (s *SessionStore) Load(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := filepath.Join(s.sessionsDir, id+sessionsFileExt)
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionsFileExt) {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), sessionsFileExt)
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionsFileExt) {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), sessionsFileExt)
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

	path := filepath.Join(s.sessionsDir, id+sessionsFileExt)
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
	path := filepath.Join(s.sessionsDir, id+sessionsFileExt)
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

// saveUnlocked writes a session to disk without acquiring the lock.
// The caller must hold the write lock.
func (s *SessionStore) saveUnlocked(session *Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := session.FilePath()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	return nil
}
