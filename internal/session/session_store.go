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

	"amurru/hakase/internal/util"
)

// SessionStore handles persisting sessions as JSON files in a local directory.
//
// Writer rules (mirroring channel/state and internal/sleep SL-005):
//   - 0700 dir / 0600 files, tmp+rename atomic saves with fsync
//   - cross-process flock via <dir>/.lock during read-modify-write
//   - torn files are quarantined aside (<id>.json.corrupt-<ts>) instead of
//     wedging List/Load
//   - a lightweight index.json (id -> summary) serves List/ListArchived so
//     listings never unmarshal full transcripts
type SessionStore struct {
	mu          sync.RWMutex
	sessionsDir string
}

// Index file and lock file names inside the sessions dir.
const (
	indexFileName = "index.json"
	dirLockName   = ".lock"
)

// sessionIndex is the on-disk summary cache. Version guards future schema
// changes; Entries maps session id -> summary.
type sessionIndex struct {
	Version int                       `json:"version"`
	Entries map[string]SessionSummary `json:"entries"`
}

const sessionIndexVersion = 1

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
	// Best-effort index rebuild so a fresh process gets fast listings even
	// when the index is missing (e.g. upgrade from a pre-index version).
	store.ensureIndex()
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

// lockDir takes the cross-process exclusive flock for the sessions dir.
// Callers must hold the appropriate in-process mu and release via unlockDir.
func (s *SessionStore) lockDir() (*os.File, error) {
	lockPath := filepath.Join(s.sessionsDir, dirLockName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open sessions lock: %w", err)
	}
	if err := util.FlockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock sessions dir: %w", err)
	}
	return f, nil
}

func unlockDir(f *os.File) {
	if f == nil {
		return
	}
	_ = util.FlockUnlock(f)
	_ = f.Close()
}

// writeFileAtomic durably writes data to path: temp file in the same dir,
// chmod 0600, fsync file, rename, fsync dir. Readers on POSIX never observe
// a torn file, so killing the process mid-save cannot corrupt the target.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sess-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup; a crash may leave a dot-tmp behind, which List
	// ignores (only *.json is scanned, index.json excluded by name).
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	// Fsync the directory so the rename itself is durable.
	if df, err := os.Open(dir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	return nil
}

// quarantineCorruptFile moves a torn/unparseable session file aside for
// forensics and returns the aside path. Best-effort: rename failures are
// returned so the caller can surface them.
func quarantineCorruptFile(path string) (string, error) {
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	return aside, nil
}

// validSessionID reports whether id is a well-formed session identifier:
// only characters that cannot carry path semantics (alphanumerics, '_', '-',
// '.'), so joining it into the sessions directory can never escape the store.
// IDs arrive from request path values and tool callers, not just from
// NewSession; legitimate ids are "task_" + UUID.
func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// Save writes a session to disk as a JSON file, atomically and under the
// cross-process dir lock. The summary index is updated in the same critical
// section so List never falls behind a successful Save.
func (s *SessionStore) Save(session *Session) error {
	if !validSessionID(session.ID) {
		return fmt.Errorf("invalid session id %q", session.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	return s.saveUnlockedFlocked(session)
}

// saveUnlockedFlocked writes one session file atomically and refreshes its
// index entry. Callers must hold s.mu (write) and the dir flock.
func (s *SessionStore) saveUnlockedFlocked(session *Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := filepath.Join(s.sessionsDir, session.ID+FileExt)
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	// Index refresh is best-effort: a failed index write is rebuilt on the
	// next List, and must never fail the Save itself.
	_ = s.updateIndexEntryLocked(session.Summary())
	return nil
}

// Load reads a session from disk by its ID. A torn file is quarantined aside
// and reported as a corruption error (not "not found") so callers can
// distinguish a crash artifact from a missing session.
func (s *SessionStore) Load(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadUnlocked(id)
}

// List returns all non-archived sessions sorted by updated_at descending.
// It is served from the summary index and never unmarshals transcripts.
func (s *SessionStore) List() ([]SessionSummary, error) {
	return s.listFiltered(false)
}

// ListArchived returns all archived sessions sorted by updated_at descending.
// It is served from the summary index and never unmarshals transcripts.
func (s *SessionStore) ListArchived() ([]SessionSummary, error) {
	return s.listFiltered(true)
}

func (s *SessionStore) listFiltered(archived bool) ([]SessionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lf, err := s.lockDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	summaries, err := s.listFromIndexLocked()
	if err != nil {
		// Index missing/corrupt/stale: full-scan rebuild (quarantines torn
		// files), then serve from the fresh index.
		summaries, err = s.rebuildIndexLocked()
		if err != nil {
			return nil, err
		}
	}

	var out []SessionSummary
	for _, sum := range summaries {
		if sum.Archived == archived {
			out = append(out, sum)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// Delete removes a session file from disk and drops its index entry.
func (s *SessionStore) Delete(id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	path := filepath.Join(s.sessionsDir, id+FileExt)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			// Keep the index consistent even when the file is already gone.
			_ = s.removeIndexEntryLocked(id)
			return fmt.Errorf("session %s not found", id)
		}
		return fmt.Errorf("failed to delete session %s: %w", id, err)
	}
	_ = s.removeIndexEntryLocked(id)
	return nil
}

// Archive sets the archived flag on a session and saves it.
func (s *SessionStore) Archive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	session, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	session.Archived = true
	session.UpdatedAt = time.Now().UTC()
	return s.saveUnlockedFlocked(session)
}

// Unarchive clears the archived flag on a session and saves it.
func (s *SessionStore) Unarchive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lf, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	session, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	session.Archived = false
	session.UpdatedAt = time.Now().UTC()
	return s.saveUnlockedFlocked(session)
}

// loadUnlocked reads a session from disk without acquiring the lock.
// The caller must hold the appropriate lock (RLock for read, Lock for write).
// Torn files are quarantined aside so one bad write never wedges the store.
func (s *SessionStore) loadUnlocked(id string) (*Session, error) {
	if !validSessionID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
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
		aside, qerr := quarantineCorruptFile(path)
		if qerr != nil {
			return nil, fmt.Errorf("failed to parse session %s (quarantine failed: %v): %w", id, qerr, err)
		}
		// Drop the stale index entry best-effort; the torn file is gone.
		_ = s.removeIndexEntryLocked(id)
		return nil, fmt.Errorf("session %s corrupt; preserved as %s: %w", id, aside, err)
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
// The caller must hold the write lock. It keeps the atomic/flocked path so
// legacy callers (Archive/Unarchive via older builds) stay crash-safe.
func (s *SessionStore) saveUnlocked(session *Session) error {
	// Note: Archive/Unarchive now go through saveUnlockedFlocked under the
	// dir flock; this fallback keeps direct saveUnlocked users safe even
	// without the flock (atomic rename alone prevents torn files).
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session %s: %w", session.ID, err)
	}

	path := filepath.Join(s.sessionsDir, session.ID+FileExt)
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("failed to save session %s: %w", session.ID, err)
	}
	_ = s.updateIndexEntryLocked(session.Summary())
	return nil
}

// indexPath returns the summary-index file path.
func (s *SessionStore) indexPath() string {
	return filepath.Join(s.sessionsDir, indexFileName)
}

// ensureIndex rebuilds the summary index when missing. Best-effort and
// lock-free (startup only); runtime paths rebuild under lock on demand.
func (s *SessionStore) ensureIndex() {
	if _, err := os.Stat(s.indexPath()); err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lf, err := s.lockDir()
	if err != nil {
		return
	}
	defer unlockDir(lf)
	// Another process may have created it between Stat and lock.
	if _, err := os.Stat(s.indexPath()); err == nil {
		return
	}
	_, _ = s.rebuildIndexLocked()
}

// listFromIndexLocked serves summaries from index.json without touching
// session files. It validates freshness against the directory listing: a
// file-count/id mismatch (e.g. crash between file write and index update, or
// a foreign writer) forces a rebuild by returning an error.
func (s *SessionStore) listFromIndexLocked() ([]SessionSummary, error) {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil, err
	}
	var idx sessionIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("session index corrupt: %w", err)
	}
	if idx.Version != sessionIndexVersion || idx.Entries == nil {
		return nil, fmt.Errorf("session index version mismatch")
	}

	dirIDs, err := s.listSessionIDs()
	if err != nil {
		return nil, err
	}
	if len(dirIDs) != len(idx.Entries) {
		return nil, fmt.Errorf("session index stale: %d files vs %d entries", len(dirIDs), len(idx.Entries))
	}
	for _, id := range dirIDs {
		if _, ok := idx.Entries[id]; !ok {
			return nil, fmt.Errorf("session index stale: missing %s", id)
		}
	}

	out := make([]SessionSummary, 0, len(idx.Entries))
	for _, sum := range idx.Entries {
		out = append(out, sum)
	}
	return out, nil
}

// listSessionIDs enumerates session ids on disk (index.json and tmp/dot
// files excluded).
func (s *SessionStore) listSessionIDs() ([]string, error) {
	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions directory: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), FileExt) {
			continue
		}
		if e.Name() == indexFileName {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), FileExt))
	}
	return ids, nil
}

// rebuildIndexLocked full-scans session files (quarantining torn ones),
// rewrites index.json atomically, and returns all summaries. Callers must
// hold s.mu and the dir flock.
func (s *SessionStore) rebuildIndexLocked() ([]SessionSummary, error) {
	ids, err := s.listSessionIDs()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]SessionSummary, len(ids))
	var out []SessionSummary
	for _, id := range ids {
		sess, err := s.loadUnlocked(id)
		if err != nil {
			// loadUnlocked already quarantined torn files; skip them.
			continue
		}
		sum := sess.Summary()
		entries[id] = sum
		out = append(out, sum)
	}
	idx := sessionIndex{Version: sessionIndexVersion, Entries: entries}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session index: %w", err)
	}
	if err := writeFileAtomic(s.indexPath(), data, 0600); err != nil {
		// Serve the scan even when the index write fails; next List retries.
		return out, nil
	}
	return out, nil
}

// updateIndexEntryLocked upserts one summary into index.json. Missing/corrupt
// indexes trigger a full rebuild instead. Callers must hold s.mu and the dir
// flock. Failures are returned but treated best-effort by Save paths.
func (s *SessionStore) updateIndexEntryLocked(sum SessionSummary) error {
	data, err := os.ReadFile(s.indexPath())
	var idx sessionIndex
	if err != nil || json.Unmarshal(data, &idx) != nil || idx.Version != sessionIndexVersion || idx.Entries == nil {
		// Rebuild from disk; the just-saved file is already durable, so the
		// rebuild picks it up even if we cannot incrementally update.
		_, rerr := s.rebuildIndexLocked()
		return rerr
	}
	idx.Entries[sum.ID] = sum
	blob, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.indexPath(), blob, 0600)
}

// removeIndexEntryLocked drops one id from index.json. Callers must hold s.mu
// and the dir flock. Best-effort like the upsert path.
func (s *SessionStore) removeIndexEntryLocked(id string) error {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil // no index: nothing to drop
	}
	var idx sessionIndex
	if err := json.Unmarshal(data, &idx); err != nil || idx.Version != sessionIndexVersion || idx.Entries == nil {
		_, rerr := s.rebuildIndexLocked()
		return rerr
	}
	if _, ok := idx.Entries[id]; !ok {
		return nil
	}
	delete(idx.Entries, id)
	blob, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.indexPath(), blob, 0600)
}
