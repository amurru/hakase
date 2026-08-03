package main

import (
	"fmt"
	"time"
)

// SessionService manages the active session and provides
// higher-level session operations for the TUI and CLI.
type SessionService struct {
	store           *SessionStore
	activeSessionID string
}

// NewSessionService creates a SessionService backed by the given store.
// On creation, it attempts to load the most recently updated
// non-archived session as the active session.
func NewSessionService(store *SessionStore) (*SessionService, error) {
	svc := &SessionService{
		store: store,
	}

	// Try to restore the most recent non-archived session as active.
	summaries, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions on startup: %w", err)
	}
	if len(summaries) > 0 {
		svc.activeSessionID = summaries[0].ID
	}

	return svc, nil
}

// CreateSession creates a new session and sets it as active.
func (s *SessionService) CreateSession(title string) (*Session, error) {
	session := NewSession(title)
	if err := s.store.Save(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}
	s.activeSessionID = session.ID
	return session, nil
}

// GetActiveSession returns the currently active session, or nil if none.
func (s *SessionService) GetActiveSession() (*Session, error) {
	if s.activeSessionID == "" {
		return nil, nil
	}
	return s.store.Load(s.activeSessionID)
}

// SetActiveSession switches to the session with the given ID.
func (s *SessionService) SetActiveSession(id string) error {
	if _, err := s.store.Load(id); err != nil {
		return fmt.Errorf("cannot switch to session %s: %w", id, err)
	}
	s.activeSessionID = id
	return nil
}

// ClearActiveSession clears the active session.
// The next user message will create a new session.
func (s *SessionService) ClearActiveSession() {
	s.activeSessionID = ""
}

// HasActiveSession returns whether there is a currently active session.
func (s *SessionService) HasActiveSession() bool {
	return s.activeSessionID != ""
}

// AddMessage appends a message to the active session.
// If no active session exists, a new one is created with a title
// derived from the first 60 characters of the content.
func (s *SessionService) AddMessage(role, content, thinking string) error {
	session, err := s.ensureActiveSession(role, content)
	if err != nil {
		return err
	}
	session.AddMessage(role, content, thinking)
	return s.store.Save(session)
}

// GetMessages returns the full message list for the session with the
// given ID. Returns an empty slice (nil, nil) when the ID is empty.
func (s *SessionService) GetMessages(id string) ([]Message, error) {
	if id == "" {
		return nil, nil
	}
	session, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	return session.Messages, nil
}

// ActiveSessionID returns the ID of the currently active session, or ""
// when no session is active.
func (s *SessionService) ActiveSessionID() string {
	return s.activeSessionID
}

// RecordUsage appends a message with a token count to the active session.
// Like AddMessage, it creates a new session when none is active. The token
// count is the provider-reported usage when known, or an estimate.
func (s *SessionService) RecordUsage(role, content, thinking string, tokens int) error {
	session, err := s.ensureActiveSession(role, content)
	if err != nil {
		return err
	}
	session.AddMessageWithMeta(role, content, thinking, tokens, MessageKindText)
	return s.store.Save(session)
}

// SetSummary persists the SummaryMessageID on the session. The ID is the
// sequence of the summary message that the summarizer appended.
func (s *SessionService) SetSummary(id, summaryMessageID string) error {
	if id == "" {
		return fmt.Errorf("no session id")
	}
	session, err := s.store.Load(id)
	if err != nil {
		return err
	}
	session.SummaryMessageID = summaryMessageID
	session.UpdatedAt = time.Now().UTC()
	return s.store.Save(session)
}

// AppendSummary appends a kind==summary message to the given session and
// updates its SummaryMessageID to point at the new message's sequence.
// This is the persistence path used by the async summarizer.
func (s *SessionService) AppendSummary(id, content string) error {
	if id == "" {
		return fmt.Errorf("no session id")
	}
	session, err := s.store.Load(id)
	if err != nil {
		return err
	}
	session.AddMessageWithMeta("agent", content, "", 0, MessageKindSummary)
	seq := session.Messages[len(session.Messages)-1].Sequence
	session.SummaryMessageID = fmt.Sprintf("%d", seq)
	session.UpdatedAt = time.Now().UTC()
	return s.store.Save(session)
}

// AddTaskRef associates a task with the active session.
func (s *SessionService) AddTaskRef(ref TaskRef) error {
	session, err := s.GetActiveSession()
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("no active session")
	}
	session.AddTaskRef(ref)
	return s.store.Save(session)
}

// DeleteSession removes a session by ID.
// If the deleted session was active, the active session is cleared.
func (s *SessionService) DeleteSession(id string) error {
	if id == s.activeSessionID {
		s.activeSessionID = ""
	}
	return s.store.Delete(id)
}

// ArchiveSession sets the archived flag on a session.
func (s *SessionService) ArchiveSession(id string) error {
	return s.store.Archive(id)
}

// UnarchiveSession clears the archived flag on a session.
func (s *SessionService) UnarchiveSession(id string) error {
	return s.store.Unarchive(id)
}

// ListSessions returns all non-archived sessions sorted by updated_at desc.
func (s *SessionService) ListSessions() ([]SessionSummary, error) {
	return s.store.List()
}

// ListArchivedSessions returns all archived sessions sorted by updated_at desc.
func (s *SessionService) ListArchivedSessions() ([]SessionSummary, error) {
	return s.store.ListArchived()
}

// CleanupStale removes sessions that have had no activity for longer
// than maxAge. Returns the number of sessions removed.
func (s *SessionService) CleanupStale(maxAge time.Duration) (int, error) {
	summaries, err := s.store.List()
	if err != nil {
		return 0, err
	}

	// Also check archived sessions for cleanup.
	archived, err := s.store.ListArchived()
	if err != nil {
		return 0, err
	}
	all := append(summaries, archived...)

	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, summary := range all {
		if summary.UpdatedAt.Before(cutoff) {
			if err := s.store.Delete(summary.ID); err != nil {
				// Log but continue cleaning up other sessions
				continue
			}
			removed++
			if summary.ID == s.activeSessionID {
				s.activeSessionID = ""
			}
		}
	}
	return removed, nil
}

// ensureActiveSession returns the active session, creating a new one
// if none exists. The title is derived from the first 60 chars of content.
func (s *SessionService) ensureActiveSession(role, content string) (*Session, error) {
	if s.activeSessionID != "" {
		session, err := s.store.Load(s.activeSessionID)
		if err == nil {
			return session, nil
		}
		// Active session file missing; clear and create new.
		s.activeSessionID = ""
	}

	// Create a new session. Title is the first 60 chars of the user's message.
	title := content
	if len(title) > 60 {
		title = title[:60] + "..."
	}
	if title == "" {
		title = "Untitled session"
	}

	session, err := s.CreateSession(title)
	if err != nil {
		return nil, err
	}
	return session, nil
}
