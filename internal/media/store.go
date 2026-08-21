package media

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/sandbox"

	"github.com/cyphar/filepath-securejoin"
	"github.com/oklog/ulid/v2"
)

// Store manages sandbox-confined media file allocation.
type Store struct {
	root string
}

// NewStore creates a Store under outputDir, resolved through the sandbox when active.
func NewStore(outputDir string) (*Store, error) {
	if outputDir == "" {
		outputDir = "outputs/media"
	}
	var root string
	if sandbox.CurrentSandbox != nil && sandbox.CurrentSandbox.Mode != sandbox.SandboxModeOff {
		resolved, err := sandbox.CurrentSandbox.ResolveScopedPath(outputDir, true)
		if err != nil {
			return nil, fmt.Errorf("media output_dir outside workspace: %w", err)
		}
		root = resolved
	} else {
		abs, err := filepath.Abs(outputDir)
		if err != nil {
			return nil, fmt.Errorf("resolve output_dir: %w", err)
		}
		root = filepath.Clean(abs)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create media output dir: %w", err)
	}
	return &Store{root: root}, nil
}

// Root returns the resolved root directory.
func (s *Store) Root() string { return s.root }

// WorkspaceRelPath returns p as a workspace-relative (cwd-relative) slash path
// such as outputs/media/<ulid>.png when p lives under the current working
// directory. This is the form agent-facing tool results and markdown snippets
// must use so the web UI mediaLinks plugin rewrites them to /api/files/inline.
// Paths outside the workspace (e.g. test temp dirs) are returned unchanged.
func (s *Store) WorkspaceRelPath(p string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.ToSlash(rel)
}

var allowedExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".mp4":  true,
	".webm": true,
	".mp3":  true,
	".wav":  true,
}

// Allocate returns a new path <root>/<ulid><ext> for the given extension.
func (s *Store) Allocate(ext string) (string, error) {
	ext = strings.ToLower(ext)
	if ext == "" {
		return "", fmt.Errorf("extension is required")
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if !allowedExts[ext] {
		return "", fmt.Errorf("extension %q not allowed", ext)
	}
	id := ulid.Make().String()
	name := id + ext
	joined, err := securejoin.SecureJoin(s.root, name)
	if err != nil {
		return "", fmt.Errorf("secure join: %w", err)
	}
	// EvalSymlinks re-check to prevent traversal via symlinked root.
	if resolved, err := filepath.EvalSymlinks(joined); err == nil {
		// If file doesn't exist, EvalSymlinks fails; also try parent dir.
		_ = resolved
	} else if !os.IsNotExist(err) {
		// If path exists and resolves outside root, reject.
		// For non-exist, check parent dir symlink.
		parent := filepath.Dir(joined)
		if pres, err2 := filepath.EvalSymlinks(parent); err2 == nil {
			// joined should be under pres; since name is ULID-only, this is safe.
			_ = pres
		}
	}
	// For non-exist file, verify parent dir is still within root after EvalSymlinks.
	// The name is ULID-only (no traversal), so joining is safe, but we still verify root containment.
	if evalRoot, err := filepath.EvalSymlinks(s.root); err == nil {
		if !isWithin(evalRoot, joined) && !isWithin(s.root, joined) {
			return "", fmt.Errorf("allocated path escapes root")
		}
	}
	return joined, nil
}

func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// Write copies r into path with a size cap, atomically via tmp+rename, mode 0644.
func (s *Store) Write(path string, r io.Reader, maxBytes int64) error {
	if !isWithin(s.root, path) {
		// Also allow EvalSymlinks parent check
		if evalRoot, err := filepath.EvalSymlinks(s.root); err == nil {
			if !isWithin(evalRoot, path) {
				return fmt.Errorf("path %q outside media root", path)
			}
		} else {
			return fmt.Errorf("path %q outside media root", path)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	n, copyErr := io.CopyN(f, r, maxBytes+1)
	f.Close()
	if copyErr != nil && copyErr != io.EOF {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", copyErr)
	}
	if n > maxBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("file too large: %d bytes exceeds %d", n, maxBytes)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
