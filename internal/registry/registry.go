// Package registry stores registered remote projects: explicit entries
// {name, clone source} that a hakase host materializes into managed
// checkouts. It complements the derived project identity (internal/project,
// which anchors sessions to the git root under the launch cwd) for remote
// web deployments, where the client's repository is not on the host and must
// be cloned first. Design: docs/git-tools/project-registry.md (DP-6..DP-10).
package registry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Project statuses. Materialization (clone) and sync are performed by the
// git tools; the registry only records the state transitions.
const (
	StatusReady     = "ready"
	StatusCloning   = "cloning"
	StatusSyncError = "sync_error"
)

// Project is one registered remote project entry.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SourceURL string    `json:"source_url"`
	Ref       string    `json:"ref,omitempty"`
	Checkout  string    `json:"checkout,omitempty"` // managed host-side dir, set after materialization
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrNotFound is returned when a project id/name is unknown.
var ErrNotFound = errors.New("project not found")

// ErrBusy is returned when a clone/sync is already in flight for the project:
// the operation is refused rather than queued so two pulls/clones never race
// on the same checkout (project-ui.md "syncing guard").
var ErrBusy = errors.New("a clone or sync is already running for this project")

// ErrWorkingTreeDirty is returned by Sync when the checkout holds uncommitted
// tracked changes. Pulling under in-progress work is refused (project-ui.md);
// untracked files alone do not trip it - git handles those without touching
// them, and refusing on mere untracked presence would block syncs in
// agent-shaped checkouts.
var ErrWorkingTreeDirty = errors.New("refusing to sync: the checkout has uncommitted tracked changes")

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// urlRe accepts the encrypted/authenticated network schemes (https, git,
	// ssh) plus file:// for a local bare remote. Plain http is excluded: a
	// clone over http sends the repository unencrypted and would leak
	// credentials when the source requires auth.
	urlRe = regexp.MustCompile(`^(https|git|ssh|file)://`)
)

// ValidName reports whether name is usable as a project name.
func ValidName(name string) bool {
	return nameRe.MatchString(name)
}

// ValidSourceURL reports whether url is an acceptable clone source for a
// registered project. Network schemes (https/git/ssh) are always fine - they
// are the point of a remote-web host. file:// points at a local bare remote
// and is accepted for local/CLI operation; per D9 the sandbox gate rejects it
// again at materialization while a sandbox is active.
func ValidSourceURL(url string) bool {
	return urlRe.MatchString(strings.TrimSpace(url))
}

// Store is the projects.json registry: a mutex-guarded in-memory map with
// atomic persistence. One file, small registry; no DB.
//
// Mutations are additionally serialized across processes (a sidecar .lock file,
// see lockRegistry): every Create/Update/Delete re-reads the file under the
// cross-process lock before applying its change, so two concurrent `hakase
// projects register` invocations cannot let the last rename discard the other
// process's entries.
type Store struct {
	path     string
	mu       sync.Mutex
	projects map[string]*Project // keyed by ID
}

// NewStore loads the registry file at path (creating an empty registry when
// absent) and creates the parent directory. Mutations re-read the file under
// the cross-process lock before writing, so a Store constructed earlier picks
// up entries another process committed by the time it next mutates.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, projects: map[string]*Project{}}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("registry: create dir: %w", err)
		}
	}
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// reloadLocked replaces s.projects with the registry file's current contents
// (empty when the file is absent or blank). Callers hold s.mu. Re-reading
// under the cross-process lock on every mutation is what stops two processes
// from overwriting each other's entries with a stale in-memory snapshot.
func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.projects = map[string]*Project{}
			return nil
		}
		return fmt.Errorf("registry: read %s: %w", s.path, err)
	}
	next := map[string]*Project{}
	if len(strings.TrimSpace(string(data))) > 0 {
		var entries []*Project
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("registry: parse %s: %w", s.path, err)
		}
		for _, p := range entries {
			if p != nil && p.ID != "" {
				next[p.ID] = p
			}
		}
	}
	s.projects = next
	return nil
}

// Registry writes hold the inter-process lock for milliseconds (reload +
// rename), so the only way a lock file outlives its operation is a crashed
// holder. Locks older than registryLockStaleAge are reclaimed as abandoned;
// waiters keep retrying for up to registryLockWait so a fresh crash
// self-heals without a manual delete.
const (
	registryLockWait     = 2 * time.Minute
	registryLockStaleAge = 1 * time.Minute
	registryLockRetry    = 50 * time.Millisecond
)

// lockRegistry acquires the cross-process registry lock: an atomically created
// sidecar file next to the registry (<file>.lock). In-process Store.mu still
// serializes goroutines within one process; this lock extends the same mutual
// exclusion to concurrent hakase processes (two CLI invocations, or a CLI next
// to a running web server). Returns a release func that removes only this
// process's lock.
func (s *Store) lockRegistry() (func(), error) {
	lockPath := s.path + ".lock"
	tokenBytes := make([]byte, 8)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("registry: lock token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	deadline := time.Now().Add(registryLockWait)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(token + "\n")
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(lockPath)
				if werr != nil {
					return nil, fmt.Errorf("registry: lock write: %w", werr)
				}
				return nil, fmt.Errorf("registry: lock close: %w", cerr)
			}
			return func() { s.unlockRegistry(lockPath, token) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("registry: lock %s: %w", lockPath, err)
		}
		// The lock is held by another process; reclaim it when it looks
		// abandoned (holder died between create and remove).
		if st, serr := os.Stat(lockPath); serr == nil && time.Since(st.ModTime()) > registryLockStaleAge {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("registry: %s is locked by another process (delete the file if it is stale)", lockPath)
		}
		time.Sleep(registryLockRetry)
	}
}

// unlockRegistry removes the lock file only when it still carries this
// process's token, so an abandoned lock reclaimed after a very long pause is
// never deleted from under its new holder.
func (s *Store) unlockRegistry(lockPath, token string) {
	if data, err := os.ReadFile(lockPath); err == nil && strings.TrimSpace(string(data)) == token {
		_ = os.Remove(lockPath)
	}
}

// DefaultPath returns the registry file location under the hakase home.
func DefaultPath() string {
	return filepath.Join(hakaseHome(), "projects.json")
}

// List returns all projects sorted by name. Reads refresh from the file first
// so a Store that outlives a concurrent process still reports its committed
// entries.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked() // best effort: a corrupt file keeps the last good map
	out := make([]Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a copy of the project with the given id.
func (s *Store) Get(id string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked() // best effort: a corrupt file keeps the last good map
	p, ok := s.projects[id]
	if !ok {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return *p, nil
}

// GetByName returns a copy of the project whose name matches (case-insensitive;
// names are unique per registry).
func (s *Store) GetByName(name string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked() // best effort: a corrupt file keeps the last good map
	for _, p := range s.projects {
		if strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			return *p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// CheckoutRoot returns the managed directory that holds every project
// checkout. It is derived from the store's own location (<store dir>/projects)
// so checkouts always live under the same tree as the registry that owns them.
func (s *Store) CheckoutRoot() string {
	return filepath.Join(filepath.Dir(s.path), "projects")
}

// CheckoutDir returns the managed checkout directory for p. The path is
// derived from the project id (never from user input), so it is always inside
// CheckoutRoot.
func (s *Store) CheckoutDir(p Project) string {
	return filepath.Join(s.CheckoutRoot(), p.ID)
}

// Create registers a new project with status cloning. Name and source URL
// are validated; both id and name must be unique.
func (s *Store) Create(name, sourceURL, ref string) (Project, error) {
	name = strings.TrimSpace(name)
	sourceURL = strings.TrimSpace(sourceURL)
	if !ValidName(name) {
		return Project{}, fmt.Errorf("registry: invalid project name %q", name)
	}
	if !ValidSourceURL(sourceURL) {
		return Project{}, fmt.Errorf("registry: unsupported source URL %q (allowed: https://, git://, ssh://, or file:// for a local bare remote)", sourceURL)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockRegistry()
	if err != nil {
		return Project{}, err
	}
	defer unlock()
	// Re-read before mutating: the in-memory map may predate a concurrent
	// process's committed entries, and the file is authoritative under the
	// lock.
	if err := s.reloadLocked(); err != nil {
		return Project{}, err
	}
	for _, p := range s.projects {
		if strings.EqualFold(p.Name, name) {
			return Project{}, fmt.Errorf("registry: a project named %q already exists", name)
		}
	}
	now := time.Now().UTC()
	p := &Project{
		ID:        "proj_" + uuid.New().String(),
		Name:      name,
		SourceURL: sourceURL,
		Ref:       strings.TrimSpace(ref),
		Status:    StatusCloning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.projects[p.ID] = p
	if err := s.saveLocked(); err != nil {
		_ = s.reloadLocked() // drop the unpersisted entry
		return Project{}, err
	}
	return *p, nil
}

// Update replaces the stored project with the same id (upsert semantics for
// status/checkout/ref transitions) and persists.
func (s *Store) Update(p Project) error {
	if p.ID == "" {
		return fmt.Errorf("registry: project id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	existing, ok := s.projects[p.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, p.ID)
	}
	for _, other := range s.projects {
		if other.ID != p.ID && strings.EqualFold(other.Name, p.Name) {
			return fmt.Errorf("registry: a project named %q already exists", p.Name)
		}
	}
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now().UTC()
	s.projects[p.ID] = &p
	if err := s.saveLocked(); err != nil {
		_ = s.reloadLocked() // restore the last persisted state
		return err
	}
	return nil
}

// Delete removes the project entry (never the remote; the caller is
// responsible for removing the local checkout, see DP-10).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if _, ok := s.projects[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(s.projects, id)
	if err := s.saveLocked(); err != nil {
		_ = s.reloadLocked() // restore the last persisted state
		return err
	}
	return nil
}

// saveLocked writes the registry atomically: temp file in the same directory
// + rename, 0600 perms (clone URLs only - never credentials - but the file
// sits next to config.json and inherits its sensitivity).
func (s *Store) saveLocked() error {
	entries := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		entries = append(entries, p)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".projects-*.tmp")
	if err != nil {
		return fmt.Errorf("registry: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("registry: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("registry: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("registry: close: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("registry: rename: %w", err)
	}
	return nil
}

// hakaseHome mirrors config.HakaseHome without importing internal/config
// (registry stays importable by packages config already imports).
func hakaseHome() string {
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hakase")
}
