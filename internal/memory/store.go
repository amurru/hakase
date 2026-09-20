package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
// deterministic.
func OpenDefault() (*Store, error) {
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
		if st, err := loadFile(s.path); err == nil {
			s.cache = st
			s.mtime, s.size = fi.ModTime(), fi.Size()
		}
	}
	return cloneState(s.cache)
}

// Update applies fn to the on-disk state under an exclusive flock and
// refreshes the cache. fn may reject the mutation by returning an error, in
// which case nothing is written.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := loadFile(s.path)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	if err := saveFile(s.path, st); err != nil {
		return err
	}
	s.cache = st
	if fi, err := os.Stat(s.path); err == nil {
		s.mtime, s.size = fi.ModTime(), fi.Size()
	}
	return nil
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

// reload unconditionally (re)loads the on-disk state into the cache.
func (s *Store) reload() error {
	st, err := loadFile(s.path)
	if err != nil {
		return err
	}
	s.cache = st
	if fi, err := os.Stat(s.path); err == nil {
		s.mtime, s.size = fi.ModTime(), fi.Size()
	}
	return nil
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

func saveFile(path string, st *State) error {
	st.Version = Version
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	lockFile := path + ".lock"

	// The notes dir may not exist yet on first write; the lock file needs it
	// before the open below.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Exclusive flock for cross-process safety (the server, the CLI, and the
	// web panel may all write), mirroring channels.json.
	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := util.FlockExclusive(lf); err != nil {
		return err
	}
	defer util.FlockUnlock(lf)

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
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
