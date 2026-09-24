// snapshot.go - per-session point-in-time snapshots for restore-to-message
// rewind (docs/session-rewind/spec.md, issue #21). One snapshot is taken
// just before each user message is recorded, plus one before every restore
// (so restore is itself undoable). Snapshots live under
// <sessionsDir>/.snapshots/<sha256 of session id>/ — a dot-subdirectory
// that listSessionIDs ignores, so the summary index never sees them, and a
// hash keeps request-derived ids out of file-path expressions while keeping
// session ids out of directory listings — and reuse the session store's
// flock, atomic writes, and 0600/0700 discipline.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// snapshotKey maps a session id to its snapshot directory name: a SHA-256
// of the id. Snapshot paths are built from request-derived ids, so the hash
// keeps tainted data out of file-path expressions entirely — and keeps
// session ids out of directory listings (privacy bonus). The id is still
// charset-validated before hashing.
func snapshotKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])
}

// DefaultSnapshotMax is the per-session snapshot ring size used when the
// config does not say otherwise.
const DefaultSnapshotMax = 50

// snapshotDirName is the dot-directory inside the sessions dir that holds
// per-session snapshot folders.
const snapshotDirName = ".snapshots"

// Snapshot triggers, encoded in the file name: "pre" is taken just before a
// user message is recorded; "pre-restore" captures the session state before
// a restore overwrites it (the undo point).
const (
	SnapshotTriggerPre        = "pre"
	SnapshotTriggerPreRestore = "pre-restore"
)

// validSnapshotName accepts only names the store itself generates:
// <unixNano>-<trigger>.json with a known trigger. Names arrive from the
// network via the restore endpoint.
var validSnapshotName = regexp.MustCompile(`^([0-9]{19})-(pre|pre-restore)\.json$`)

// SnapshotInfo describes one snapshot for listings (the restore dialog).
type SnapshotInfo struct {
	// Name is the snapshot file name, the restore API's address.
	Name string `json:"name"`
	// CreatedAt is derived from the name's unix-nano stamp.
	CreatedAt time.Time `json:"created_at"`
	// Messages is the message count the session had when snapshotted: the
	// snapshot taken before message at index N has exactly N messages.
	Messages int `json:"messages"`
	// Trigger is "pre" (before a user turn) or "pre-restore" (undo point).
	Trigger string `json:"trigger"`
	// Preview is the snapshot's last message content, truncated.
	Preview string `json:"preview"`
}

// snapshotLimit returns the configured per-session ring size; <= 0 disables
// snapshot writes.
func (s *SessionStore) snapshotLimit() int { return s.snapshotMax }

// snapshotsDir returns the base snapshots directory (created on demand).
func (s *SessionStore) snapshotsDir() string {
	return filepath.Join(s.sessionsDir, snapshotDirName)
}

// validSnapshotID guards the session-id join for the snapshot paths.
func validSnapshotID(id string) bool { return validSessionID(id) }

// SaveSnapshot writes one point-in-time copy of the session under
// .snapshots/<id>/ and prunes the ring to the configured limit. The session
// is marshaled exactly like a session file (same struct, 0600, atomic).
// Returns the generated snapshot name.
func (s *SessionStore) SaveSnapshot(session *Session, trigger string) (string, error) {
	if trigger != SnapshotTriggerPre && trigger != SnapshotTriggerPreRestore {
		return "", fmt.Errorf("invalid snapshot trigger %q", trigger)
	}
	if !validSnapshotID(session.ID) {
		return "", fmt.Errorf("invalid session id %q", session.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return "", err
	}
	defer unlockDir(lf)

	data, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("failed to marshal snapshot for %s: %w", session.ID, err)
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), trigger)
	dir := filepath.Join(s.snapshotsDir(), snapshotKey(session.ID))
	if err := writeFileAtomic(filepath.Join(dir, name), data, 0600); err != nil {
		return "", fmt.Errorf("failed to write snapshot for %s: %w", session.ID, err)
	}
	s.pruneSnapshotsLocked(snapshotKey(session.ID))
	return name, nil
}

// pruneSnapshotsLocked enforces the per-session ring (newest kept). Callers
// hold s.mu and the dir flock. Best-effort: removal failures are logged and
// never fail the save that triggered pruning. key is the hashed session
// directory name (snapshotKey).
func (s *SessionStore) pruneSnapshotsLocked(key string) {
	limit := s.snapshotLimit()
	if limit <= 0 {
		return
	}
	names, err := s.snapshotNamesLocked(key)
	if err != nil || len(names) <= limit {
		return
	}
	// Names sort chronologically by their unix-nano prefix.
	sort.Strings(names)
	for _, name := range names[:len(names)-limit] {
		if err := os.Remove(filepath.Join(s.snapshotsDir(), key, name)); err != nil {
			log.Printf("session store: failed to prune snapshot %s/%s: %v", key, name, err)
		}
	}
}

// snapshotNamesLocked lists snapshot file names for a session key, oldest
// first.
func (s *SessionStore) snapshotNamesLocked(key string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.snapshotsDir(), key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list snapshots for %s: %w", key, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), FileExt) {
			continue
		}
		if !validSnapshotName.MatchString(e.Name()) {
			continue // foreign/cruft files are ignored, never served
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// ListSnapshots returns the session's snapshots, newest first. A missing
// snapshot directory is an empty list, not an error.
func (s *SessionStore) ListSnapshots(sessionID string) ([]SnapshotInfo, error) {
	if !validSnapshotID(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	key := snapshotKey(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	lf, err := s.lockDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	names, err := s.snapshotNamesLocked(key)
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotInfo, 0, len(names))
	// Walk newest-first so a read error still yields the most recent entries.
	for i := len(names) - 1; i >= 0; i-- {
		info, err := s.readSnapshotInfoLocked(key, names[i])
		if err != nil {
			log.Printf("session store: skipping unreadable snapshot %s/%s: %v", key, names[i], err)
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// readSnapshotInfoLocked decodes one snapshot just far enough for a listing.
func (s *SessionStore) readSnapshotInfoLocked(key, name string) (SnapshotInfo, error) {
	m := validSnapshotName.FindStringSubmatch(name)
	if m == nil {
		return SnapshotInfo{}, fmt.Errorf("invalid snapshot name %q", name)
	}
	ns, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("invalid snapshot timestamp %q", m[1])
	}
	data, err := os.ReadFile(filepath.Join(s.snapshotsDir(), key, name))
	if err != nil {
		return SnapshotInfo{}, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return SnapshotInfo{}, fmt.Errorf("corrupt snapshot: %w", err)
	}
	info := SnapshotInfo{
		Name:      name,
		CreatedAt: time.Unix(0, ns).UTC(),
		Messages:  len(sess.Messages),
		Trigger:   m[2],
	}
	if n := len(sess.Messages); n > 0 {
		info.Preview = truncateSnapshotPreview(sess.Messages[n-1].Content)
	}
	return info, nil
}

// truncateSnapshotPreview caps a snapshot preview for the dialog listing.
func truncateSnapshotPreview(s string) string {
	const max = 160
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Typed snapshot errors, for HTTP classification at the restore endpoint:
// ErrSnapshotNotFound → 404, ErrInvalidSnapshotName → 400, anything else →
// 500 (storage/corruption).
var (
	ErrSnapshotNotFound     = errors.New("snapshot not found")
	ErrInvalidSnapshotName  = errors.New("invalid snapshot name")
	ErrSnapshotSessionMismatch = errors.New("snapshot belongs to a different session")
)

// LoadSnapshot reads one snapshot as a Session (normalized like a store
// load, so legacy snapshots behave identically). Errors wrap
// ErrSnapshotNotFound / ErrInvalidSnapshotName so callers can classify with
// errors.Is; storage and corruption errors stay untyped (500).
func (s *SessionStore) LoadSnapshot(sessionID, name string) (*Session, error) {
	if !validSnapshotID(sessionID) {
		return nil, fmt.Errorf("%w: invalid session id %q", ErrInvalidSnapshotName, sessionID)
	}
	if !validSnapshotName.MatchString(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSnapshotName, name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	lf, err := s.lockDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	data, err := os.ReadFile(filepath.Join(s.snapshotsDir(), snapshotKey(sessionID), name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotNotFound, name)
		}
		return nil, fmt.Errorf("failed to read snapshot %s: %w", name, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("snapshot %s corrupt: %w", name, err)
	}
	if sess.ID != sessionID {
		return nil, fmt.Errorf("%w: %s belongs to %s", ErrSnapshotSessionMismatch, name, sess.ID)
	}
	normalizeLegacyMessages(&sess)
	return &sess, nil
}

// DeleteSnapshots removes all snapshots for a session (session deleted ⇒
// snapshots deleted). Missing directory is success.
func (s *SessionStore) DeleteSnapshots(sessionID string) error {
	if !validSnapshotID(sessionID) {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	if err := os.RemoveAll(filepath.Join(s.snapshotsDir(), snapshotKey(sessionID))); err != nil {
		return fmt.Errorf("failed to delete snapshots for %s: %w", sessionID, err)
	}
	return nil
}
