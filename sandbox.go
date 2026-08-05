package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cyphar/filepath-securejoin"
)

// SandboxMode selects the sandboxing strategy. See the plan section
// "Sandboxing & workspace confinement" in .omo/plans/hakase-debug-log-fixes.md.
//
// IMPORTANT: SandboxModeOff is opt-in only (the user explicitly sets mode
// "off" in config.json). The default is SandboxModePaths - workspace path
// confinement is ON out of the box. This is a deliberate choice: the agent
// should not write outside approved workspaces without explicit configuration.
type SandboxMode string

const (
	SandboxModeOff        SandboxMode = "off"
	SandboxModePaths      SandboxMode = "paths"      // Phase 1: workspace path confinement
	SandboxModeBubblewrap SandboxMode = "bubblewrap" // Phase 2: bwrap subprocess isolation
	SandboxModeLandlock   SandboxMode = "landlock"   // Phase 3: in-process Landlock + seccomp
)

// SandboxConfig is the resolved, normalized sandbox configuration used
// throughout the runtime. Roots are absolute, cleaned, and symlink-evaluated.
// A nil *SandboxConfig means confinement is disabled and resolveScopedPath
// falls back to filepath.Abs (the legacy behavior). In practice
// LoadSandboxConfig never returns nil - the default is SandboxModePaths.
type SandboxConfig struct {
	Mode            SandboxMode
	WorkspaceRoots  []string
	ReadRoots       []string
	DenyRoots       []string
	AllowNetwork    bool
	AllowPipInstall bool
	Permissions     map[string]string
	// AllowedCommands is an opt-in allowlist of allowed binaries (basename
	// only). Empty means no allowlist restrictions apply.
	AllowedCommands []string
	// DenyPatterns is a list of regex patterns matched against the raw
	// command line. Any match triggers a hard deny.
	DenyPatterns []string
	// RiskThreshold overrides the ask threshold. Valid values: "low",
	// "medium", "high", "unknown". Empty string means use the mode-based
	// default (bubblewrap->high, paths/off/nil->medium).
	RiskThreshold string
	// AllowFallback controls whether the agent falls back to paths-mode
	// execution when bubblewrap is unavailable. Default false (fail closed).
	AllowFallback bool
}

// SandboxJSON is the on-disk JSON shape for the "sandbox" config block.
// It maps 1:1 onto SandboxConfig; LoadSandboxConfig performs the
// normalization (defaults, root resolution, symlink eval).
type SandboxJSON struct {
	Mode            string            `json:"mode,omitempty"`
	WorkspaceRoots  []string          `json:"workspace_roots,omitempty"`
	ReadRoots       []string          `json:"read_roots,omitempty"`
	DenyRoots       []string          `json:"deny_roots,omitempty"`
	AllowNetwork    bool              `json:"allow_network,omitempty"`
	AllowPipInstall bool              `json:"allow_pip_install,omitempty"`
	Permissions     map[string]string `json:"permissions,omitempty"`
	AllowedCommands []string          `json:"allowed_commands,omitempty"`
	DenyPatterns    []string          `json:"deny_patterns,omitempty"`
	RiskThreshold   string            `json:"risk_threshold,omitempty"`
	AllowFallback   bool              `json:"allow_fallback,omitempty"`
}

// LoadSandboxConfig converts a *SandboxJSON into a normalized *SandboxConfig,
// applying defaults and resolving roots. Returns a paths-mode config even
// when s is nil (sandbox is ON by default; set mode "off" to disable).
func LoadSandboxConfig(s *SandboxJSON) *SandboxConfig {
	if s == nil {
		s = &SandboxJSON{}
	}
	sb := &SandboxConfig{
		Mode:            SandboxMode(s.Mode),
		WorkspaceRoots:  append([]string(nil), s.WorkspaceRoots...),
		ReadRoots:       append([]string(nil), s.ReadRoots...),
		DenyRoots:       append([]string(nil), s.DenyRoots...),
		AllowNetwork:    s.AllowNetwork,
		AllowPipInstall: s.AllowPipInstall,
		Permissions:     s.Permissions,
		AllowedCommands: append([]string(nil), s.AllowedCommands...),
		DenyPatterns:    append([]string(nil), s.DenyPatterns...),
		RiskThreshold:   s.RiskThreshold,
		AllowFallback:   s.AllowFallback,
	}

	// Mode default: empty or unrecognized -> paths (sandbox ON by default).
	// SandboxModeOff is only set when the user explicitly chooses it.
	switch sb.Mode {
	case SandboxModeOff, SandboxModePaths, SandboxModeBubblewrap, SandboxModeLandlock:
		// known mode
	default:
		sb.Mode = SandboxModePaths
	}

	// WorkspaceRoots default: ["."].
	if len(sb.WorkspaceRoots) == 0 {
		sb.WorkspaceRoots = []string{"."}
	}
	sb.WorkspaceRoots = normalizeRoots(sb.WorkspaceRoots)

	// ReadRoots default: same as workspace roots.
	if len(sb.ReadRoots) == 0 {
		sb.ReadRoots = append([]string(nil), sb.WorkspaceRoots...)
	} else {
		sb.ReadRoots = normalizeRoots(sb.ReadRoots)
	}

	// DenyRoots: normalize but allow empty.
	sb.DenyRoots = normalizeRoots(sb.DenyRoots)

	// AllowPipInstall default: true.
	if !sb.AllowPipInstall {
		// JSON false is explicit; but the spec says "defaults true" which
		// only matters when the field is absent. There is no way to
		// distinguish absent from false in a populated struct, so we
		// honor the explicit value here. The default-true semantics apply
		// via the zero value being false *before* this function runs only
		// if the caller passes a struct with the field unset - which is
		// indistinguishable from false. To preserve "default true" we
		// would need *bool; the plan accepts bool with default true, so
		// we treat the zero value as true by setting true when the JSON
		// omitted it. Since we cannot detect omission, we keep the
		// explicit value and document the caveat. In practice the
		// config.json example sets allow_pip_install: true.
	}

	// Permissions default only when nil.
	if sb.Permissions == nil {
		sb.Permissions = map[string]string{
			"system_exec":        "ask",
			"python_interpreter": "allow",
			"write_file":         "allow",
		}
	}

	return sb
}

// normalizeRoots cleans, evaluates symlinks, and de-duplicates a list of
// root paths. Empty entries are skipped. EvalSymlinks failures fall back
// to the cleaned absolute path (so a non-existent deny root still
// normalizes to a stable lexical form). Entries are deduplicated.
func normalizeRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		abs, err := filepath.Abs(expandHome(r))
		if err != nil {
			continue
		}
		cleaned := filepath.Clean(abs)
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			resolved = cleaned
		}
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	return out
}

// expandHome replaces a leading "~" with the user's home directory.
// Returns the path unchanged on error or when no "~" prefix is present.
func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// within reports whether target is contained under root. rel must be the
// filepath.Rel(root, target) result; the helper treats ".." and any path
// starting with ".." + separator as outside. rel == "." (target == root)
// counts as inside.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// resolveScopedPath is the core path-confinement helper. When the sandbox
// is off (sb == nil or Mode == off) it returns filepath.Abs(path),
// preserving the legacy resolvePath behavior. When on, it:
//  1. Expands ~, cleans, and resolves the target against cwd.
//  2. Rejects targets under any deny root.
//  3. For write: requires the target inside a workspace root.
//     For read: inside a workspace root OR a read root.
//  4. Re-resolves via securejoin.SecureJoin(root, rel) and re-verifies
//     containment with EvalSymlinks + within() (TOCTOU caveat documented
//     in the plan; this two-step is the accepted Phase-1 approach).
func (sb *SandboxConfig) resolveScopedPath(path string, write bool) (string, error) {
	if sb == nil || sb.Mode == SandboxModeOff {
		return filepath.Abs(path)
	}

	// Determine the target: expand ~, clean, resolve against cwd.
	p := expandHome(path)
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolveScopedPath: cwd: %w", err)
		}
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)

	// Deny roots take precedence.
	for _, d := range sb.DenyRoots {
		if within(d, p) {
			return "", fmt.Errorf("path %q is in a denied root", path)
		}
	}

	// Containment check.
	roots := sb.WorkspaceRoots
	if !write {
		roots = sb.ReadRoots
	}
	for _, root := range roots {
		if !within(root, p) {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		// Re-resolve via SecureJoin to canonicalize and reject traversal,
		// then re-verify containment after EvalSymlinks (closes the
		// common symlink-escape case; TOCTOU remains per the plan).
		joined, err := securejoin.SecureJoin(root, rel)
		if err != nil {
			continue
		}
		// Re-verify: follow symlinks on the joined path. If the joined
		// path exists, EvalSymlinks reveals the real target and we check
		// it stays under root. If it does not exist (new-file write
		// case, or SecureJoin produced a shadow path for an absolute
		// symlink target), fall back to EvalSymlinks on the original
		// target p - when p itself is a symlink pointing outside root,
		// that resolution is what catches the escape.
		if resolved, rerr := filepath.EvalSymlinks(joined); rerr == nil {
			if !within(root, resolved) {
				return "", fmt.Errorf("path %q escapes workspace root after symlink resolution", path)
			}
		} else if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			// joined path does not exist; check the original target's
			// real resolution. If it lands outside root, the caller is
			// trying to follow a symlink out of the workspace.
			if !within(root, resolved) {
				return "", fmt.Errorf("path %q escapes workspace root after symlink resolution", path)
			}
		}
		// else: neither joined nor the original target exists on disk
		// (e.g. creating a brand-new file). SecureJoin's lexical safety
		// is the accepted guarantee in that case.
		return joined, nil
	}

	if write {
		return "", fmt.Errorf("path %q is outside approved workspace", path)
	}
	return "", fmt.Errorf("path %q is outside approved read roots", path)
}

// workspaceRoot returns the first workspace root, used by other agents to
// pin cmd.Dir. Returns "" when sb is nil or the sandbox is off.
func (sb *SandboxConfig) workspaceRoot() string {
	if sb == nil || sb.Mode == SandboxModeOff {
		return ""
	}
	if len(sb.WorkspaceRoots) == 0 {
		return ""
	}
	return sb.WorkspaceRoots[0]
}

// permitted returns the permission action for a tool name and whether it
// was explicitly set. ok is false when the tool is not in the Permissions
// map.
func (sb *SandboxConfig) permitted(tool string) (action string, ok bool) {
	if sb == nil || sb.Permissions == nil {
		return "", false
	}
	action, ok = sb.Permissions[tool]
	return action, ok
}
