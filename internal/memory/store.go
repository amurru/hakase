package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"
)

// NewID returns a fresh note id: "mem_" + 16 hex chars from crypto/rand.
func NewID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Crypto/rand failing means the system entropy source is broken;
		// refuse to issue a predictable id.
		panic("memory: cannot generate note id: " + err.Error())
	}
	return "mem_" + hex.EncodeToString(buf[:])
}

// DefaultPath returns <hakase home>/memory/notes.json (or $HAKASE_HOME).
func DefaultPath() string {
	home := config.HakaseHome()
	if home == "" {
		return FileName
	}
	return filepath.Join(home, FileName)
}

// Store is a cross-process-safe accessor for the notes file. Get reloads
// from disk whenever the file changed underneath (the CLI, the web panel, or
// a second store instance may have written), so every reader sees current
// state without coordination.
type Store struct {
	mu    sync.Mutex
	path  string
	cache *State
	mtime time.Time
	size  int64
}

// Open loads the notes file (or starts empty when missing) and returns a
// store rooted at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenDefault opens the store at DefaultPath(). Callers open per use (the
// file is small and Update re-reads from disk anyway); there is deliberately
// no process-wide singleton so $HAKASE_HOME changes and tests stay
// deterministic. It fails closed when no home directory can be determined:
// the relative FileName fallback would scatter memory across working
// directories.
func OpenDefault() (*Store, error) {
	if config.HakaseHome() == "" {
		return nil, errors.New("memory: no home directory available (set HAKASE_HOME or HOME)")
	}
	return Open(DefaultPath())
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Get returns a copy of the current state, reloading from disk when the file
// was modified by another process or store instance since the last read.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, err := os.Stat(s.path); err != nil {
		// File gone (deleted underneath us): fall back to the cache.
		return cloneState(s.cache)
	} else if fi.ModTime() != s.mtime || fi.Size() != s.size {
		// The slow path reads (and, on a corrupt file, quarantines), so it
		// must hold the same flock Update holds: an unlocked quarantine
		// could otherwise race a concurrent writer and rename away a
		// freshly-written valid store. The fast path above only touches the
		// in-memory cache and stays lock-free.
		_ = s.withFileLock(func() error {
			fi, err := os.Stat(s.path)
			if err != nil || (fi.ModTime() == s.mtime && fi.Size() == s.size) {
				return nil // gone or already current: keep the cache
			}
			if st, err := loadFile(s.path); err == nil {
				s.cache = st
				s.mtime, s.size = fi.ModTime(), fi.Size()
			}
			return nil
		})
	}
	return cloneState(s.cache)
}

// withFileLock runs fn while holding the store's cross-process exclusive
// flock, creating the parent dir and lock file as needed. Callers must not
// nest it (a second open file description in the same process would block
// on its own lock); loadFile and writeFileAtomic are deliberately lock-free
// because every caller holds the lock by the time it reaches them.
func (s *Store) withFileLock(fn func() error) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing too-permissive directory untouched;
	// tighten it so notes.json is never resurrected world-readable.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := util.FlockExclusive(lf); err != nil {
		return err
	}
	defer util.FlockUnlock(lf)
	return fn()
}

// reloadLocked re-reads the on-disk state into the cache. It must be called
// with the flock held (see withFileLock).
func (s *Store) reloadLocked() error {
	fi, err := os.Stat(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	st, err := loadFile(s.path)
	if err != nil {
		return err
	}
	s.cache = st
	if fi != nil {
		s.mtime, s.size = fi.ModTime(), fi.Size()
	} else {
		s.mtime, s.size = time.Time{}, 0
	}
	return nil
}

// Update applies fn to the on-disk state under an exclusive flock held
// across the whole load-mutate-save transaction, so concurrent writers (the
// agent runtime, the CLI, the web panel) cannot interleave and overwrite
// each other's notes. fn may reject the mutation by returning an error, in
// which case nothing is written.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withFileLock(func() error {
		st, err := loadFile(s.path)
		if err != nil {
			return err
		}
		if err := fn(st); err != nil {
			return err
		}
		if err := writeFileAtomic(s.path, st); err != nil {
			return err
		}
		s.cache = st
		if fi, err := os.Stat(s.path); err == nil {
			s.mtime, s.size = fi.ModTime(), fi.Size()
		}
		return nil
	})
}

// Add validates and stamps a new note, dedupes an exact prior note (same
// category + project + content: bump UpdatedAt instead of stacking a copy),
// and persists. When the store already holds maxNotes entries (maxNotes > 0)
// it refuses with the remedy spelled out - the agent decides what to drop,
// never silent eviction.
func (s *Store) Add(category, content, project string, maxNotes int) (Note, error) {
	content, err := NormalizeContent(content)
	if err != nil {
		return Note{}, err
	}
	if !ValidCategory(category) {
		return Note{}, fmt.Errorf("unknown category %q; use one of: %s", category, strings.Join(Categories, ", "))
	}
	var note Note
	err = s.Update(func(st *State) error {
		now := time.Now().UTC()
		for i := range st.Notes {
			if st.Notes[i].Category == category && st.Notes[i].Project == project && st.Notes[i].Content == content {
				st.Notes[i].UpdatedAt = now
				note = st.Notes[i]
				return nil
			}
		}
		if maxNotes > 0 && len(st.Notes) >= maxNotes {
			return fmt.Errorf("memory is full (%d notes); forget a stale note with forget_memory before adding new ones", maxNotes)
		}
		note = Note{
			ID:        NewID(),
			Category:  category,
			Content:   content,
			Project:   project,
			CreatedAt: now,
			UpdatedAt: now,
		}
		st.Notes = append(st.Notes, note)
		return nil
	})
	if err != nil {
		return Note{}, err
	}
	return note, nil
}

// Remove deletes the note with the given id and reports whether it existed.
// Unknown ids are a clean false, not an error (idempotent cleanup).
func (s *Store) Remove(id string) (bool, error) {
	removed := false
	err := s.Update(func(st *State) error {
		for i := range st.Notes {
			if st.Notes[i].ID == id {
				st.Notes = append(st.Notes[:i], st.Notes[i+1:]...)
				removed = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// FindByID returns the note with the given id.
func (s *Store) FindByID(id string) (Note, bool) {
	for _, n := range s.Get().Notes {
		if n.ID == id {
			return n, true
		}
	}
	return Note{}, false
}

// Touch updates an existing note's content (and optionally category) and
// bumps UpdatedAt. Unknown ids are an error so the caller can tell the model
// the id is stale.
func (s *Store) Touch(id, category, content string) (Note, error) {
	content, err := NormalizeContent(content)
	if err != nil {
		return Note{}, err
	}
	if !ValidCategory(category) {
		return Note{}, fmt.Errorf("unknown category %q; use one of: %s", category, strings.Join(Categories, ", "))
	}
	var note Note
	err = s.Update(func(st *State) error {
		for i := range st.Notes {
			if st.Notes[i].ID == id {
				st.Notes[i].Category = category
				st.Notes[i].Content = content
				st.Notes[i].UpdatedAt = time.Now().UTC()
				note = st.Notes[i]
				return nil
			}
		}
		return fmt.Errorf("no note with id %s; the ids in your context are the source of truth", id)
	})
	if err != nil {
		return Note{}, err
	}
	return note, nil
}

// reload unconditionally (re)loads the on-disk state into the cache. The
// read (and, on a corrupt file, the quarantine rename) happens under the
// same flock Update holds, so it can never race a concurrent write into
// quarantining a freshly-written valid store.
func (s *Store) reload() error {
	return s.withFileLock(s.reloadLocked)
}

func loadFile(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Version: Version}, nil
		}
		return nil, fmt.Errorf("memory: cannot read notes: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		// A corrupt notes file must not wedge the agent: quarantine it for
		// inspection (same rollback as the sleep state store) and restart
		// empty - the next Update rewrites the file.
		sidecar := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405") + ".json"
		_ = os.Rename(path, sidecar)
		return &State{Version: Version}, nil
	}
	if st.Version == 0 {
		st.Version = Version
	}
	return &st, nil
}

// writeFileAtomic persists st as path: tmp + rename, so POSIX readers never
// observe a torn file. The tmp file is created 0600 and re-chmodded before
// the rename — os.WriteFile-style mode arguments only apply at creation, so
// a pre-existing permissive tmp would otherwise leak its mode into the
// renamed store.
func writeFileAtomic(path string, st *State) error {
	st.Version = Version
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func cloneState(st *State) State {
	if st == nil {
		return State{Version: Version}
	}
	out := State{Version: st.Version}
	out.Notes = append(out.Notes, st.Notes...)
	return out
}
