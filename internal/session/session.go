package session

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// ExecSession tracks a task-scoped execution session (terminal/command session).
type ExecSession struct {
	TaskID    string
	CWD       string
	Env       []string // Captured parent environment for sub-agent isolation
	CreatedAt time.Time
}

// FileOpsSession tracks a task-scoped file operations context with a
// sandbox root directory that isolates file reads/writes per task.
type FileOpsSession struct {
	TaskID    string
	RootDir   string
	CreatedAt time.Time
}

// SessionManager tracks per-task resources with thread-safe maps.
// Each delegate_task call gets a unique task_id that keys all
// per-task resources: execution sessions, file ops contexts, and CWD
// records.
type SessionManager struct {
	mu           sync.RWMutex
	activeEnvs   map[string]*ExecSession
	fileOpsCache map[string]*FileOpsSession
	cwdRecords   map[string]string
	cleanupDone  chan struct{}
}

// NewSessionManager creates a SessionManager and starts the background
// cleanup goroutine with the given stale-session TTL.
func NewSessionManager(staleTTL time.Duration) *SessionManager {
	sm := &SessionManager{
		activeEnvs:   make(map[string]*ExecSession),
		fileOpsCache: make(map[string]*FileOpsSession),
		cwdRecords:   make(map[string]string),
		cleanupDone:  make(chan struct{}),
	}
	go sm.cleanupLoop(staleTTL)
	return sm
}

// StopCleanup stops the background cleanup goroutine.
func (sm *SessionManager) StopCleanup() {
	close(sm.cleanupDone)
}

// cleanupLoop periodically removes stale sessions older than maxAge.
func (sm *SessionManager) cleanupLoop(maxAge time.Duration) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sm.CleanupInactive(maxAge)
		case <-sm.cleanupDone:
			return
		}
	}
}

// GenerateTaskID returns a new unique task ID prefixed with "task_".
func GenerateTaskID() string {
	return "task_" + uuid.New().String()
}

// GetOrCreateSession returns an existing ExecSession for taskID, or
// creates a new one with the given CWD.
func (sm *SessionManager) GetOrCreateSession(taskID string, cwd string) *ExecSession {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.activeEnvs[taskID]
	if !ok {
		sess = &ExecSession{
			TaskID:    taskID,
			CWD:       cwd,
			CreatedAt: time.Now(),
		}
		sm.activeEnvs[taskID] = sess
	}
	return sess
}

// GetFileOps returns an existing FileOpsSession for taskID, or
// creates a new one with the given sandbox root directory.
func (sm *SessionManager) GetFileOps(taskID string, rootDir string) *FileOpsSession {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	fos, ok := sm.fileOpsCache[taskID]
	if !ok {
		fos = &FileOpsSession{
			TaskID:    taskID,
			RootDir:   rootDir,
			CreatedAt: time.Now(),
		}
		sm.fileOpsCache[taskID] = fos
	}
	return fos
}

// RecordCWD updates the recorded working directory for a task.
func (sm *SessionManager) RecordCWD(taskID string, dir string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.cwdRecords[taskID] = dir
}

// GetCWD returns the recorded working directory for a task, or
// the provided fallback.
func (sm *SessionManager) GetCWD(taskID string, fallback string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if dir, ok := sm.cwdRecords[taskID]; ok && dir != "" {
		return dir
	}
	return fallback
}

// CleanupInactive removes sessions that have been inactive for longer
// than maxAge.
func (sm *SessionManager) CleanupInactive(maxAge time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for id, sess := range sm.activeEnvs {
		if sess.CreatedAt.Before(cutoff) {
			delete(sm.activeEnvs, id)
		}
	}
	for id, fos := range sm.fileOpsCache {
		if fos.CreatedAt.Before(cutoff) {
			delete(sm.fileOpsCache, id)
		}
	}
	for id, cwd := range sm.cwdRecords {
		// We don't track creation time for cwd records separately;
		// clean up entries whose task is no longer in activeEnvs.
		if _, ok := sm.activeEnvs[id]; !ok {
			delete(sm.cwdRecords, id)
			_ = cwd // suppress unused warning
		}
	}
}

// SeedHintedContextPathsHook is set by the root package to restore the
// in-memory hinted context paths dedup set from persisted session data.
var SeedHintedContextPathsHook func(paths []string)

// BuildHintedPathsHook is set by the root package to export the in-memory
// hinted context paths dedup set for persistence on the session.
var BuildHintedPathsHook func() []string
