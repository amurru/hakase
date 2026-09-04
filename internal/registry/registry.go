// Package registry stores registered remote projects: explicit entries
// {name, clone source} that a hakase host materializes into managed
// checkouts. It complements the derived project identity (internal/project,
// which anchors sessions to the git root under the launch cwd) for remote
// web deployments, where the client's repository is not on the host and must
// be cloned first. Design: docs/git-tools/project-registry.md (DP-6..DP-10).
package registry

import (
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
type Store struct {
	path     string
	mu       sync.Mutex
	projects map[string]*Project // keyed by ID
}

// NewStore loads the registry file at path (creating an empty registry when
// absent) and creates the parent directory. The file is re-read on every
// construction; processes hold one Store for their lifetime.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, projects: map[string]*Project{}}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("registry: create dir: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	var entries []*Project
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	for _, p := range entries {
		if p != nil && p.ID != "" {
			s.projects[p.ID] = p
		}
	}
	return s, nil
}

// DefaultPath returns the registry file location under the hakase home.
func DefaultPath() string {
	return filepath.Join(hakaseHome(), "projects.json")
}

// List returns all projects sorted by name.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		delete(s.projects, p.ID)
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
	return s.saveLocked()
}

// Delete removes the project entry (never the remote; the caller is
// responsible for removing the local checkout, see DP-10).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(s.projects, id)
	return s.saveLocked()
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
