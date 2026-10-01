package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectHooksFile is the project hooks filename, always relative to the
// project root. The path is fixed (not configurable): a configurable path
// is trust confusion with no user benefit.
const ProjectHooksFile = ".hakase/hooks.json"

// ProjectHooksPath returns the project hooks file for root, or "" when root
// is empty (no project identity, e.g. session-less surfaces and unit tests).
func ProjectHooksPath(root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	return filepath.Join(root, ProjectHooksFile)
}

// ProjectFile is the on-disk project hooks file. It can only ADD hooks:
// there is deliberately no enabled switch and no other key, so a cloned
// repo can never disable the user's hooks or the trust gate (the
// managed-tier-lite property). Unknown keys are load-time errors.
type ProjectFile struct {
	PreToolUse       []Group `json:"PreToolUse,omitempty"`
	PostToolUse      []Group `json:"PostToolUse,omitempty"`
	SessionStart     []Group `json:"SessionStart,omitempty"`
	UserPromptSubmit []Group `json:"UserPromptSubmit,omitempty"`
}

// UnmarshalJSON rejects unknown keys (including "enabled") so a project
// file that tries to widen its own authority fails loudly.
func (f *ProjectFile) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type plain ProjectFile // avoid recursion
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	for k := range raw {
		switch k {
		case "PreToolUse", "PostToolUse", "SessionStart", "UserPromptSubmit":
		default:
			return fmt.Errorf("invalid project hooks.%s: unknown key (project files hold only PreToolUse, PostToolUse, SessionStart, UserPromptSubmit)", k)
		}
	}
	*f = ProjectFile(p)
	return nil
}

// Validate checks every group with the same handler rules as user config.
func (f *ProjectFile) Validate() error {
	if f == nil {
		return nil
	}
	for i := range f.PreToolUse {
		if err := f.PreToolUse[i].validate(fmt.Sprintf("project.PreToolUse[%d]", i), EventPreToolUse); err != nil {
			return err
		}
	}
	for i := range f.PostToolUse {
		if err := f.PostToolUse[i].validate(fmt.Sprintf("project.PostToolUse[%d]", i), EventPostToolUse); err != nil {
			return err
		}
	}
	for i := range f.SessionStart {
		if err := f.SessionStart[i].validate(fmt.Sprintf("project.SessionStart[%d]", i), EventSessionStart); err != nil {
			return err
		}
	}
	for i := range f.UserPromptSubmit {
		if err := f.UserPromptSubmit[i].validate(fmt.Sprintf("project.UserPromptSubmit[%d]", i), EventUserPromptSubmit); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDefaults fills handler defaults (timeout, type, on_failure).
func (f *ProjectFile) ApplyDefaults() {
	if f == nil {
		return
	}
	for i := range f.PreToolUse {
		f.PreToolUse[i].applyDefaults()
	}
	for i := range f.PostToolUse {
		f.PostToolUse[i].applyDefaults()
	}
	for i := range f.SessionStart {
		f.SessionStart[i].applyDefaults()
	}
	for i := range f.UserPromptSubmit {
		f.UserPromptSubmit[i].applyDefaults()
	}
}

// Empty reports whether the file carries no groups at all.
func (f *ProjectFile) Empty() bool {
	return f == nil || (len(f.PreToolUse) == 0 && len(f.PostToolUse) == 0 && len(f.SessionStart) == 0 && len(f.UserPromptSubmit) == 0)
}

// LoadProjectFile reads and validates the project hooks file for root.
// A missing file (or empty root) is not an error: it yields (nil, nil), so
// projects without hooks cost one stat per lookup. A present-but-broken
// file IS an error, surfaced to the warn log by the caller (never fatal:
// a broken project file must not break the user's own hooks).
func LoadProjectFile(root string) (*ProjectFile, error) {
	path := ProjectHooksPath(root)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read project hooks %s: %v", path, err)
	}
	// Symlink-escape guard: the file that actually gets read must live
	// under the project root. A repo that symlinks .hakase/hooks.json at
	// /tmp/evil does not get its hooks loaded (CVE-2026-40068 class:
	// trust-relevant paths must not be attacker-redirectable).
	// Both sides resolve first: temp dirs routinely carry aliases
	// (Windows 8.3 short names, symlinked /tmp on macOS), and comparing
	// a resolved file against an unresolved root false-positives.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve project hooks %s: %v", path, err)
	}
	resolvedRoot := root
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = rr
	}
	if rel, err := filepath.Rel(resolvedRoot, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("project hooks %s escapes the project root (resolves to %s)", path, resolved)
	}
	var f ProjectFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid project hooks %s: %v", path, err)
	}
	f.ApplyDefaults()
	if err := f.Validate(); err != nil {
		return nil, err
	}
	f.resolveRelativeCommands(root)
	return &f, nil
}

// resolveRelativeCommands anchors relative script paths at the project
// root, so a project hook means the same thing regardless of the process
// cwd. Absolute paths are kept as-is. Bare command names (no separator,
// not an existing file under root) are left for PATH lookup: joining
// "python3" onto the root would manufacture a non-existent path and the
// hook would fail open instead of running. Elements that resolve to an
// existing regular file under root are absolutized (interpreter-form
// argv[1:] included), which is exec-equivalent — hook processes run with
// Dir=root — and lets Fingerprint hash every script body. Missing targets
// are NOT a load error: exec fails open at runtime per on_failure, same
// as user hooks.
func (f *ProjectFile) resolveRelativeCommands(root string) {
	resolve := func(groups []Group) {
		for gi := range groups {
			for hi := range groups[gi].Hooks {
				cmd := groups[gi].Hooks[hi].Command
				for i, a := range cmd {
					if a == "" || filepath.IsAbs(a) {
						continue
					}
					if i > 0 && !isPathLike(a) {
						continue
					}
					if !isPathLike(a) {
						// Bare argv[0]: anchor only when it names a real
						// file under root, else PATH lookup.
						if fi, err := os.Stat(filepath.Join(root, a)); err != nil || !fi.Mode().IsRegular() {
							continue
						}
					}
					cmd[i] = filepath.Join(root, a)
				}
			}
		}
	}
	resolve(f.PreToolUse)
	resolve(f.PostToolUse)
	resolve(f.SessionStart)
	resolve(f.UserPromptSubmit)
}

// isPathLike reports whether s looks like a path rather than a bare
// command name or flag value.
func isPathLike(s string) bool {
	return strings.ContainsAny(s, `/\`)
}
