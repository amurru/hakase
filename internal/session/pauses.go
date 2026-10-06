// pauses.go - durable registry of in-flight gate pauses.
//
// Resume (docs/durable-resume/plan.md Phase 5) re-enters the runner
// against the paused turn's ADK session ID. Per-turn ADK sessions are
// otherwise ephemeral and the in-memory task→session map dies with the
// process, so the mapping must be persisted at gate-block time. The
// wrappers in internal/agent (ApproveExec, askClarify) record here when
// a gate blocks and remove the record when it resolves; leftover
// records after a restart are interrupted pauses.
//
// Same writer rules as the sibling stores: 0600 file, tmp+rename,
// cross-process flock on every read and write.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"amurru/hakase/internal/util"
)

// Gate types recorded in PauseRecord.Gate.
const (
	PauseGateApproval = "approval"
	PauseGateClarify  = "clarify"
)

// pausesFileName is the registry file inside the ADK state dir.
const pausesFileName = "pauses.json"

// PauseRecord describes one blocked gate: everything the resume
// driver (Phase 5) and run-view resurrection (Phase 7) need to find
// the paused ADK session and re-emit the prompt.
type PauseRecord struct {
	// PauseID is the registry primary key (UUID).
	PauseID string `json:"pause_id"`
	// HakaseSessionID is the conversation the asking run serves.
	HakaseSessionID string `json:"hakase_session_id"`
	// ADKSessionID is the paused turn's ADK session (the per-turn
	// task ID). Empty when recorded outside a registered run; such
	// records aid resurrection display but cannot resume.
	ADKSessionID string `json:"adk_session_id"`
	// Gate is PauseGateApproval or PauseGateClarify.
	Gate string `json:"gate"`
	// Summary is a one-line human-readable prompt description.
	Summary string `json:"summary"`
	// Detail carries the gate payload (approval: tool/command/risk;
	// clarify: question/choices) for prompt re-emission.
	Detail map[string]any `json:"detail,omitempty"`
	// CreatedAt bounds resume age (MaxResumeAgeMinutes).
	CreatedAt time.Time `json:"created_at"`
}

// PauseRegistry persists pause records. Read-through on every call:
// records are few and TUI + web share one directory, so freshness
// across processes beats caching.
type PauseRegistry struct {
	mu  sync.RWMutex
	dir string // ADK state dir (<sessionsDir>/adk)
}

// NewPauseRegistry creates the registry rooted at <sessionsDir>/adk
// (created 0700 when missing).
func NewPauseRegistry(sessionsDir string) (*PauseRegistry, error) {
	dir := filepath.Join(sessionsDir, adkSubdir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create ADK state directory: %w", err)
	}
	return &PauseRegistry{dir: dir}, nil
}

func (r *PauseRegistry) pausesPath() string {
	return filepath.Join(r.dir, pausesFileName)
}

func (r *PauseRegistry) lockPauses() (*os.File, error) {
	lockPath := filepath.Join(r.dir, dirLockName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open ADK pauses lock: %w", err)
	}
	if err := util.FlockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock ADK pauses: %w", err)
	}
	return f, nil
}

// readAllLocked loads the registry; missing file = empty map.
// Callers must hold mu + flock.
func (r *PauseRegistry) readAllLocked() (map[string]PauseRecord, error) {
	data, err := os.ReadFile(r.pausesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]PauseRecord), nil
		}
		return nil, err
	}
	all := make(map[string]PauseRecord)
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("failed to decode pause registry: %w", err)
	}
	return all, nil
}

// writeAllLocked persists the registry atomically (0600).
// Callers must hold mu + flock.
func (r *PauseRegistry) writeAllLocked(all map[string]PauseRecord) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal pause registry: %w", err)
	}
	if err := writeFileAtomic(r.pausesPath(), data, 0600); err != nil {
		return fmt.Errorf("failed to save pause registry: %w", err)
	}
	return nil
}

// Record persists rec, assigning a PauseID and CreatedAt when empty.
// Returns the PauseID.
func (r *PauseRegistry) Record(rec PauseRecord) (string, error) {
	if rec.PauseID == "" {
		rec.PauseID = uuid.NewString()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lf, err := r.lockPauses()
	if err != nil {
		return "", err
	}
	defer unlockDir(lf)

	all, err := r.readAllLocked()
	if err != nil {
		return "", err
	}
	all[rec.PauseID] = rec
	if err := r.writeAllLocked(all); err != nil {
		return "", err
	}
	return rec.PauseID, nil
}

// Unrecord removes a pause; missing IDs are a no-op (resolve and
// timeout paths must never fail on a double remove).
func (r *PauseRegistry) Unrecord(pauseID string) error {
	if pauseID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lf, err := r.lockPauses()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	all, err := r.readAllLocked()
	if err != nil {
		return err
	}
	if _, ok := all[pauseID]; !ok {
		return nil
	}
	delete(all, pauseID)
	return r.writeAllLocked(all)
}

// List returns all recorded pauses, oldest first.
func (r *PauseRegistry) List() ([]PauseRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lf, err := r.lockPauses()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	all, err := r.readAllLocked()
	if err != nil {
		return nil, err
	}
	out := make([]PauseRecord, 0, len(all))
	for _, rec := range all {
		out = append(out, rec)
	}
	sortPausesByAge(out)
	return out, nil
}

// ListForSession returns pauses for one hakase session, oldest first.
func (r *PauseRegistry) ListForSession(hakaseSessionID string) ([]PauseRecord, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	out := make([]PauseRecord, 0, len(all))
	for _, rec := range all {
		if rec.HakaseSessionID == hakaseSessionID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func sortPausesByAge(recs []PauseRecord) {
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j].CreatedAt.Before(recs[j-1].CreatedAt); j-- {
			recs[j], recs[j-1] = recs[j-1], recs[j]
		}
	}
}
