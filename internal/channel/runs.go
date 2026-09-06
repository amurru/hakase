package channel

import (
	"context"
	"sync"
	"time"
)

// ActiveRun describes one in-flight agent turn owned by a chat.
type ActiveRun struct {
	ChatKey   string
	SessionID string
	Cancel    context.CancelFunc
	StartedAt time.Time
}

// RunManager enforces the one-run-per-chat policy: a chat with a running turn
// must /stop it before issuing another (the web handler instead uses a
// per-session semaphore; a chat is a stricter, more conversational surface).
type RunManager struct {
	mu     sync.Mutex
	active map[string]*ActiveRun
}

// NewRunManager creates an empty run manager.
func NewRunManager() *RunManager {
	return &RunManager{active: map[string]*ActiveRun{}}
}

// TryStart registers a run for chatKey. It returns false when the chat
// already has an active run (the caller should answer "busy").
func (m *RunManager) TryStart(chatKey, sessionID string, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.active[chatKey]; ok {
		return false
	}
	m.active[chatKey] = &ActiveRun{
		ChatKey:   chatKey,
		SessionID: sessionID,
		Cancel:    cancel,
		StartedAt: time.Now(),
	}
	return true
}

// Finish unregisters the chat's run (idempotent).
func (m *RunManager) Finish(chatKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, chatKey)
}

// Cancel cancels the chat's active run, returning true when there was one.
func (m *RunManager) Cancel(chatKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.active[chatKey]
	if !ok {
		return false
	}
	run.Cancel()
	return true
}

// Running reports the chat's active run, if any.
func (m *RunManager) Running(chatKey string) (*ActiveRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.active[chatKey]
	if !ok {
		return nil, false
	}
	cp := *run
	return &cp, true
}

// ActiveChatKeys lists chats with in-flight runs (used to route delegation
// pushes only where someone is watching a run).
func (m *RunManager) ActiveChatKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.active))
	for k := range m.active {
		keys = append(keys, k)
	}
	return keys
}
