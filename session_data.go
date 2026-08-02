package main

import (
	"time"

	"github.com/google/uuid"
)

const (
	sessionsDir      = "./sessions"
	sessionsFileExt  = ".json"
)

// Session represents a user chat session — a persistent conversation
// container that holds messages and optional task references.
// Sessions are stored as individual JSON files in ./sessions/.
type Session struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	TaskRefs    []TaskRef `json:"task_refs,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Archived    bool      `json:"archived"`
	Messages    []Message `json:"messages"`
}

// Message represents a single turn in a chat session.
type Message struct {
	Role      string    `json:"role"`       // "user" or "agent"
	Content   string    `json:"content"`    // Main message text
	Thinking  string    `json:"thinking,omitempty"` // Optional reasoning text
	Timestamp time.Time `json:"timestamp"`
}

// TaskRef is an embedded snapshot of task metadata stored directly
// in the session file. This ensures the session remains self-contained
// even if tasks.json is deleted or corrupted.
type TaskRef struct {
	TaskID      string    `json:"task_id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SessionSummary is a lightweight view of a session used for listing.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Archived     bool      `json:"archived"`
}

// NewSession creates a new Session with a generated ID and current timestamps.
func NewSession(title string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        generateSessionID(),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]Message, 0),
		TaskRefs:  make([]TaskRef, 0),
	}
}

// FilePath returns the full path to the session's JSON file on disk.
func (s *Session) FilePath() string {
	return sessionsDir + "/" + s.ID + sessionsFileExt
}

// AddMessage appends a message to the session and updates the timestamp.
func (s *Session) AddMessage(role, content, thinking string) {
	s.Messages = append(s.Messages, Message{
		Role:      role,
		Content:   content,
		Thinking:  thinking,
		Timestamp: time.Now().UTC(),
	})
	s.UpdatedAt = time.Now().UTC()
}

// AddTaskRef associates a task with the session. If a ref with the same
// task_id already exists, it is replaced with the new metadata.
func (s *Session) AddTaskRef(ref TaskRef) {
	for i, existing := range s.TaskRefs {
		if existing.TaskID == ref.TaskID {
			s.TaskRefs[i] = ref
			s.UpdatedAt = time.Now().UTC()
			return
		}
	}
	s.TaskRefs = append(s.TaskRefs, ref)
	s.UpdatedAt = time.Now().UTC()
}

// Summary returns a lightweight SessionSummary for listing purposes.
func (s *Session) Summary() SessionSummary {
	return SessionSummary{
		ID:           s.ID,
		Title:        s.Title,
		Description:  s.Description,
		UpdatedAt:    s.UpdatedAt,
		MessageCount: len(s.Messages),
		Archived:     s.Archived,
	}
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	return "sess_" + uuid.New().String()
}
