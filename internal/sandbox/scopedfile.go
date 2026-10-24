// scopedfile.go - scoped exclusive file creation (S24).
//
// Tool-path file writes (write_file/patch, save_skill, python scratch
// scripts, downloads) must not follow attacker-planted symlinks and must
// not clobber on a create-only path. Every write here goes through one
// choke with three properties:
//
//   - scoped: ScopedWriteFileInRoot confines the write inside root (Clean
//   - join + prefix check, then EvalSymlinks re-check after MkdirAll so
//     a pre-planted symlinked parent cannot redirect the write).
//   - O_EXCL on create-only writes (overwrite=false): the existence check
//     and the creation are one atomic step, so a plant between stat and
//     open cannot redirect the write.
//   - O_NOFOLLOW on every open (unix; Lstat pre-check everywhere): a
//     symlink at the final path refuses instead of being followed.
//
// Fail-closed throughout: any check failure returns an error and writes
// nothing. Hash-only audit unaffected (no content logged).
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// errScopedWrite marks S24 confinement refusals (callers surface these
// verbatim; they carry paths, so redaction stays the caller's job).
type scopedWriteError struct{ msg string }

func (e *scopedWriteError) Error() string { return e.msg }

// withinRoot reports whether abs stays inside root (equal or beneath).
func withinRoot(root, abs string) bool {
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	if abs == root {
		return true
	}
	return strings.HasPrefix(abs, root+string(os.PathSeparator))
}

// resolveInRoot joins name onto root and confines it (S24 scoped leg).
// Absolute names join under root (filepath.Join semantics); ".." escapes
// refuse. Empty names refuse.
func resolveInRoot(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &scopedWriteError{"scoped write refused: empty path"}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", &scopedWriteError{"scoped write refused: cannot resolve root"}
	}
	joined := filepath.Join(absRoot, name)
	if !withinRoot(absRoot, joined) {
		return "", &scopedWriteError{"scoped write refused: path escapes root"}
	}
	return joined, nil
}

// lstatChain refuses when final or any existing ancestor is a symlink and
// fails closed on unexpected stat errors (missing ancestors are fine:
// they are created by MkdirAll before this runs).
func lstatChain(path string, stopAt string) error {
	stopAt = filepath.Clean(stopAt)
	for p := filepath.Clean(path); ; {
		fi, err := os.Lstat(p)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return &scopedWriteError{"scoped write refused: symlink in path"}
			}
		} else if !os.IsNotExist(err) {
			return &scopedWriteError{"scoped write refused: cannot stat path"}
		}
		if p == stopAt {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
		// Bound the walk: pathological depths fail closed instead of
		// spinning (depths beyond 256 never occur from Join).
		if len(strings.Split(p, string(os.PathSeparator))) > 512 {
			return &scopedWriteError{"scoped write refused: path too deep"}
		}
	}
	return nil
}

// openNoFollow opens an existing-or-new file refusing symlinks (S24
// O_NOFOLLOW leg). overwrite=false adds O_EXCL (atomic create-only).
func openNoFollow(path string, perm os.FileMode, overwrite bool) (*os.File, error) {
	flag := os.O_WRONLY | os.O_CREATE | noFollowFlag
	if overwrite {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		if !overwrite && os.IsExist(err) {
			return nil, &scopedWriteError{"scoped write refused: file exists (overwrite=false)"}
		}
		return nil, &scopedWriteError{fmt.Sprintf("scoped write refused: open failed: %v", stripPath(err))}
	}
	return f, nil
}

// stripPath drops filesystem paths from an error for safe surfacing
// (paths can carry secrets); keeps the operation kind.
func stripPath(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Op + " failed"
	}
	return "open failed"
}

// ScopedWriteFileInRoot writes data under root/name (S24 full choke):
// confine, MkdirAll parents, EvalSymlinks re-check (parent-swap defense),
// Lstat symlink refusal, then a single O_NOFOLLOW open (O_EXCL when
// overwrite=false). Returns the resolved absolute path.
func ScopedWriteFileInRoot(root, name string, data []byte, perm os.FileMode, overwrite bool) (string, error) {
	joined, err := resolveInRoot(root, name)
	if err != nil {
		return "", err
	}
	absRoot, _ := filepath.Abs(root)
	dir := filepath.Dir(joined)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", &scopedWriteError{"scoped write refused: cannot create parent directories"}
	}
	// Parent-swap defense: a pre-planted symlinked parent redirects
	// MkdirAll; re-resolve and require containment after the fact.
	// M2 fail-closed: any EvalSymlinks error refuses (never proceeds on
	// an unresolved parent).
	realDir, rerr := filepath.EvalSymlinks(dir)
	if rerr != nil {
		return "", &scopedWriteError{"scoped write refused: cannot resolve parent directories"}
	}
	realRoot, rerr := filepath.EvalSymlinks(absRoot)
	if rerr != nil {
		return "", &scopedWriteError{"scoped write refused: cannot resolve root"}
	}
	if !withinRoot(realRoot, realDir) {
		return "", &scopedWriteError{"scoped write refused: resolved path escapes root"}
	}
	if err := lstatChain(joined, absRoot); err != nil {
		return "", err
	}
	f, err := openNoFollow(joined, perm, overwrite)
	if err != nil {
		return "", err
	}
	if len(data) > 0 {
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			return "", &scopedWriteError{"scoped write refused: write failed"}
		}
	}
	// M3: a deferred Close would drop flush/sync errors - check it.
	if cerr := f.Close(); cerr != nil {
		return "", &scopedWriteError{"scoped write refused: close failed"}
	}
	return joined, nil
}

// ScopedWriteFile writes data to an absolute-or-relative path with the
// S24 symlink/exclusive discipline but no root confinement (for callers
// whose path is already resolved by taskResolve/ResolveScopedPath).
//
// Sandbox-off note: without a root there is no containment to check
// against, so callers with a known root must prefer ScopedWriteFileInRoot
// (the confined variant). This path still refuses a symlinked parent
// (EvalSymlinks equality, fail-closed on error), refuses final-component
// symlinks via Lstat + O_NOFOLLOW, and uses O_EXCL for create-only. Tests
// resolve TempDir via EvalSymlinks first (darwin /tmp and /var are
// themselves symlinks, so unresolved fixtures would false-positive).
func ScopedWriteFile(path string, data []byte, perm os.FileMode, overwrite bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &scopedWriteError{"scoped write refused: cannot resolve path"}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return "", &scopedWriteError{"scoped write refused: cannot create parent directories"}
	}
	// M1/M2: no-root parent-swap defense - a symlinked parent must refuse
	// (fail closed when the parent cannot be resolved).
	realDir, rerr := filepath.EvalSymlinks(filepath.Dir(abs))
	if rerr != nil {
		return "", &scopedWriteError{"scoped write refused: cannot resolve parent directories"}
	}
	if filepath.Clean(realDir) != filepath.Clean(filepath.Dir(abs)) {
		return "", &scopedWriteError{"scoped write refused: symlink in path"}
	}
	if err := lstatChain(abs, filepath.Dir(abs)); err != nil {
		return "", err
	}
	f, err := openNoFollow(abs, perm, overwrite)
	if err != nil {
		return "", err
	}
	if len(data) > 0 {
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			return "", &scopedWriteError{"scoped write refused: write failed"}
		}
	}
	// M3: a deferred Close would drop flush errors - check it.
	if cerr := f.Close(); cerr != nil {
		return "", &scopedWriteError{"scoped write refused: close failed"}
	}
	return abs, nil
}

// ScopedCreateFile opens path for streaming writes (downloads) with the
// S24 discipline: confined under root when root != "", symlink refusal
// (Lstat + an EvalSymlinks parent re-check after MkdirAll, R33 - the
// streaming-create leg was missing it), O_NOFOLLOW, truncate semantics
// preserved (downloads overwrite the same name today). The caller streams
// then closes; on error the caller unlinks the partial file (existing
// saveDownloadBody contract).
func ScopedCreateFile(root, path string, perm os.FileMode) (*os.File, string, error) {
	abs := path
	rooted := strings.TrimSpace(root) != ""
	if rooted {
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil, "", &scopedWriteError{"scoped write refused: cannot relativize path"}
		}
		var err error
		abs, err = resolveInRoot(root, rel)
		if err != nil {
			return nil, "", err
		}
	} else {
		var err error
		abs, err = filepath.Abs(path)
		if err != nil {
			return nil, "", &scopedWriteError{"scoped write refused: cannot resolve path"}
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, "", &scopedWriteError{"scoped write refused: cannot create parent directories"}
	}
	// R33 parent-swap defense (was missing on this leg): a pre-planted
	// symlinked parent redirects MkdirAll; re-resolve and require
	// containment (rooted) or identity (rootless) after the fact.
	// Fail closed on any EvalSymlinks error (never proceed unresolved).
	if rooted {
		absRoot, _ := filepath.Abs(root)
		realRoot, rerr := filepath.EvalSymlinks(absRoot)
		if rerr != nil {
			return nil, "", &scopedWriteError{"scoped write refused: cannot resolve root"}
		}
		realDir, rerr := filepath.EvalSymlinks(filepath.Dir(abs))
		if rerr != nil {
			return nil, "", &scopedWriteError{"scoped write refused: cannot resolve parent directories"}
		}
		if !withinRoot(realRoot, realDir) {
			return nil, "", &scopedWriteError{"scoped write refused: resolved path escapes root"}
		}
	} else {
		realDir, rerr := filepath.EvalSymlinks(filepath.Dir(abs))
		if rerr != nil {
			return nil, "", &scopedWriteError{"scoped write refused: cannot resolve parent directories"}
		}
		if filepath.Clean(realDir) != filepath.Clean(filepath.Dir(abs)) {
			return nil, "", &scopedWriteError{"scoped write refused: symlink in path"}
		}
	}
	if err := lstatChain(abs, filepath.Dir(abs)); err != nil {
		return nil, "", err
	}
	f, err := openNoFollow(abs, perm, true)
	if err != nil {
		return nil, "", err
	}
	return f, abs, nil
}
