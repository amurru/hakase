package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"amurru/hakase/internal/interfaces"

	"github.com/cyphar/filepath-securejoin"
)

// SandboxMode selects the sandboxing strategy.
type SandboxMode string

const (
	SandboxModeOff        SandboxMode = "off"
	SandboxModePaths      SandboxMode = "paths"
	SandboxModeBubblewrap SandboxMode = "bubblewrap"
	SandboxModeLandlock   SandboxMode = "landlock"
)

// SandboxConfig is the resolved, normalized sandbox configuration used
// throughout the runtime. Roots are absolute, cleaned, and symlink-evaluated.
// A nil *SandboxConfig means confinement is disabled.
type SandboxConfig struct {
	Mode           SandboxMode
	WorkspaceRoots []string
	ReadRoots      []string
	DenyRoots      []string
	// DenyBasenames rejects any file with one of these base names inside a
	// scoped root regardless of location (e.g. nested dotenv files such as
	// services/api/.env). Populated implicitly by LoadSandboxConfig.
	DenyBasenames   []string
	AllowNetwork    bool
	AllowPipInstall bool
	Permissions     map[string]string
	AllowedCommands []string
	DenyPatterns    []string
	RiskThreshold   string
	AllowFallback   bool
}

// SandboxJSON is the on-disk JSON shape for the "sandbox" config block.
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

// CurrentSandbox is the package-level sandbox configuration consulted by
// buildExecCommand and file-ops tools. It is nil when sandboxing is disabled.
// Set at startup by the main package.
var CurrentSandbox *SandboxConfig

// Context-scoped sandbox override (project-registry DP-7): a project-bound
// agent run pins the run's workspace/read roots to the project checkout
// without touching the process-wide CurrentSandbox. The exec/file/git
// resolvers prefer the context value over CurrentSandbox.
type sandboxCtxKey struct{}

// WithConfig returns ctx carrying sb as the effective sandbox for that scope.
func WithConfig(ctx context.Context, sb *SandboxConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sandboxCtxKey{}, sb)
}

// ConfigFrom returns the sandbox that applies to ctx: the context-scoped
// override when present, otherwise CurrentSandbox.
func ConfigFrom(ctx context.Context) *SandboxConfig {
	if ctx != nil {
		if sb, ok := ctx.Value(sandboxCtxKey{}).(*SandboxConfig); ok {
			return sb
		}
	}
	return CurrentSandbox
}

// PinnedTo returns a copy of base whose workspace and read roots are pinned to
// root alone (typically a project checkout). Mode, deny roots/basenames,
// permissions, allowlist, deny patterns, risk threshold, and fallback are
// preserved. Returns nil when base is nil - confinement disabled stays
// disabled, and a pinned copy is only ever built on top of an active sandbox.
func PinnedTo(base *SandboxConfig, root string) *SandboxConfig {
	if base == nil {
		return nil
	}
	cp := *base
	cp.WorkspaceRoots = normalizeRoots([]string{root})
	cp.ReadRoots = append([]string(nil), cp.WorkspaceRoots...)
	return &cp
}

// LoadSandboxConfig converts a *SandboxJSON into a normalized *SandboxConfig,
// applying defaults and resolving roots.
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

	switch sb.Mode {
	case SandboxModeOff, SandboxModePaths, SandboxModeBubblewrap, SandboxModeLandlock:
	default:
		sb.Mode = SandboxModePaths
	}

	// WIN-005: bubblewrap and landlock do not exist on Windows - coerce to
	// paths mode (the only supported v1 mode) with a logged warning. No
	// hard error, no silent bare-exec fallback; the audit trail shows the
	// effective mode.
	if runtime.GOOS == "windows" &&
		(sb.Mode == SandboxModeBubblewrap || sb.Mode == SandboxModeLandlock) {
		fmt.Printf("hakase: sandbox mode %q is not supported on Windows; coercing to %q\n",
			sb.Mode, SandboxModePaths)
		sb.Mode = SandboxModePaths
	}

	if len(sb.WorkspaceRoots) == 0 {
		sb.WorkspaceRoots = []string{"."}
	}
	sb.WorkspaceRoots = normalizeRoots(sb.WorkspaceRoots)

	if len(sb.ReadRoots) == 0 {
		sb.ReadRoots = append([]string(nil), sb.WorkspaceRoots...)
	} else {
		sb.ReadRoots = normalizeRoots(sb.ReadRoots)
	}

	sb.DenyRoots = normalizeRoots(append(sensitiveFilePaths(), sb.DenyRoots...))
	sb.DenyBasenames = []string{".env"}

	if sb.Permissions == nil {
		sb.Permissions = map[string]string{
			"system_exec":        "ask",
			"python_interpreter": "allow",
			"write_file":         "allow",
		}
	}

	return sb
}

// hakaseHomeDir mirrors config.HakaseHome without importing internal/config
// (which imports this package): $HAKASE_HOME when set, otherwise ~/.hakase.
func hakaseHomeDir() string {
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hakase")
}

// sensitiveFilePaths lists well-known hakase-owned files whose contents are
// secrets: the project config with provider API keys, dotenv files, the
// user-level config fallback, MCP server tokens, the admin credential hash,
// and the JWT signing secret. LoadSandboxConfig appends them to DenyRoots on
// every construction, so no read or write scope can serve their contents no
// matter how permissive the configured workspace/read roots are.
func sensitiveFilePaths() []string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths,
			filepath.Join(cwd, "config.json"),
			filepath.Join(cwd, ".env"),
		)
	}
	if home := hakaseHomeDir(); home != "" {
		paths = append(paths,
			filepath.Join(home, "config.json"),
			filepath.Join(home, "mcp.json"),
			filepath.Join(home, "credentials.json"),
			filepath.Join(home, "jwt-secret"),
			filepath.Join(home, "cronjobs.json"),
		)
	}
	return paths
}

// normalizeRoots cleans, evaluates symlinks, and de-duplicates a list of root paths.
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

// within reports whether target is contained under root. On Windows the
// comparison is case-insensitive (NTFS/Win32 path case-insensitivity:
// C:\Foo and c:\foo are the same path).
func within(root, target string) bool {
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
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

// ResolveScopedPath is the core path-confinement helper. When the sandbox
// is off (sb == nil or Mode == off) it returns filepath.Abs(path).
func (sb *SandboxConfig) ResolveScopedPath(path string, write bool) (string, error) {
	if sb == nil || sb.Mode == SandboxModeOff {
		return filepath.Abs(path)
	}

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

	// WIN-005: reject Win32 path aliases (trailing dots/spaces, ADS,
	// device namespaces, drive-relative forms) on the raw input before any
	// containment math - the OS would resolve them onto the base path the
	// string checks just approved.
	if err := checkPathAlias(path); err != nil {
		return "", fmt.Errorf("path %q rejected: %w", path, err)
	}
	// Canonicalize (handle-based on Windows): folds junctions, reparse
	// points, 8.3 short names, and case, so every check below runs against
	// the path the OS would actually open. Existing paths are opened with
	// the caller's intended access mode.
	if canon, cerr := canonicalizePath(p, write); cerr == nil {
		p = filepath.Clean(canon)
	}

	for _, d := range sb.DenyRoots {
		if within(d, p) {
			return "", fmt.Errorf("path %q is in a denied root", path)
		}
	}
	if sb.deniedBasename(p) {
		return "", fmt.Errorf("path %q is a denied sensitive file", path)
	}

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
		joined, err := securejoin.SecureJoin(root, rel)
		if err != nil {
			continue
		}
		resolved := joined
		if r, rerr := filepath.EvalSymlinks(joined); rerr == nil {
			if !within(root, r) {
				return "", fmt.Errorf("path %q escapes workspace root after symlink resolution", path)
			}
			resolved = r
		} else if r, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if !within(root, r) {
				return "", fmt.Errorf("path %q escapes workspace root after symlink resolution", path)
			}
			resolved = r
		}
		// Re-check deny rules against the fully resolved path: a symlink
		// inside an approved root may resolve into a denied root or onto a
		// sensitive file, which would otherwise bypass the pre-join check.
		for _, d := range sb.DenyRoots {
			if within(d, resolved) {
				return "", fmt.Errorf("path %q resolves into a denied root", path)
			}
		}
		if sb.deniedBasename(resolved) {
			return "", fmt.Errorf("path %q resolves to a denied sensitive file", path)
		}
		return joined, nil
	}

	if write {
		return "", fmt.Errorf("path %q is outside approved workspace", path)
	}
	return "", fmt.Errorf("path %q is outside approved read roots", path)
}

// WorkspaceRoot returns the first workspace root, used by other agents to
// pin cmd.Dir. Returns "" when sb is nil or the sandbox is off.
func (sb *SandboxConfig) WorkspaceRoot() string {
	if sb == nil || sb.Mode == SandboxModeOff {
		return ""
	}
	if len(sb.WorkspaceRoots) == 0 {
		return ""
	}
	return sb.WorkspaceRoots[0]
}

// Permitted returns the permission action for a tool name and whether it
// was explicitly set. ok is false when the tool is not in the Permissions map.
func (sb *SandboxConfig) Permitted(tool string) (action string, ok bool) {
	if sb == nil || sb.Permissions == nil {
		return "", false
	}
	action, ok = sb.Permissions[tool]
	return action, ok
}

// deniedBasename reports whether the base name of p matches an implicitly
// denied sensitive basename (e.g. .env anywhere under a scoped root). The
// comparison is case-insensitive on Windows, where file names are too.
func (sb *SandboxConfig) deniedBasename(p string) bool {
	if sb == nil || len(sb.DenyBasenames) == 0 {
		return false
	}
	base := filepath.Base(p)
	for _, b := range sb.DenyBasenames {
		if runtime.GOOS == "windows" {
			if strings.EqualFold(base, b) {
				return true
			}
			continue
		}
		if base == b {
			return true
		}
	}
	return false
}

// DeniedPath reports whether target falls under any deny root (including the
// implicit sensitive-file denies added by LoadSandboxConfig) or carries a
// denied sensitive basename. It is a cheap
// string containment check for hiding entries from directory listings; the
// authoritative enforcement stays in ResolveScopedPath.
func (sb *SandboxConfig) DeniedPath(target string) bool {
	if sb == nil {
		return false
	}
	// WIN-005: alias rejection first - a listing entry like "config.json."
	// or "config.json:ads" must be treated as the denied base file.
	if err := checkPathAlias(target); err != nil {
		return true
	}
	if sb.deniedBasename(target) {
		return true
	}
	p := filepath.Clean(target)
	for _, d := range sb.DenyRoots {
		if within(d, p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Cross-package function hooks set by the main package at startup.
// These bridge the sandbox package to root-level gate, approval, audit,
// and context functions that have not yet been migrated to internal packages.
// ---------------------------------------------------------------------------

// CommandAuditEntry records one command-execution decision.
type CommandAuditEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Tool        string    `json:"tool"`
	Command     string    `json:"command"`
	Args        []string  `json:"args"`
	CWD         string    `json:"cwd"`
	SandboxMode string    `json:"sandbox_mode"`
	Decision    string    `json:"decision"`
	Risk        string    `json:"risk"`
	Reason      string    `json:"reason"`
	DurationMs  int64     `json:"duration_ms"`
	ExitCode    int       `json:"exit_code"`
}

// GateDecision is the outcome of evaluating one command.
type GateDecision struct {
	Action GateAction
	Risk   CommandRisk
	Reason string
}

// GateAction is the policy outcome.
type GateAction string

const (
	ActionAllow GateAction = "allow"
	ActionAsk   GateAction = "ask"
	ActionDeny  GateAction = "deny"
)

// CommandRisk classifies a parsed command's danger level.
type CommandRisk int

const (
	RiskLow CommandRisk = iota
	RiskMedium
	RiskHigh
	RiskUnknown
)

func (r CommandRisk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// EvaluateCommandFunc is set by main to gate-evaluate a system command.
// When nil, gate evaluation is skipped (fail-open; tests with nil sandbox).
var EvaluateCommandFunc func(sb *SandboxConfig, command string, args []string) GateDecision

// AuditCommandFunc is set by main to audit-log a command execution.
// When nil, auditing is no-op.
var AuditCommandFunc func(entry CommandAuditEntry)

// ApproveFunc is set by main to ask the user for approval.
// When nil, approval is denied (fail-closed).
var ApproveFunc func(req interfaces.ApprovalRequest) (bool, error)

// ApprovalExpiryFunc is set by main to return the configured approval expiry.
// When nil, defaults to 60s.
var ApprovalExpiryFunc func() time.Duration

// SubdirContextHintFunc is set by main to return a subdirectory context hint.
// When nil, returns "" (no-op).
var SubdirContextHintFunc func(dir string) string

// WrapUntrustedDataFunc is set by main to wrap untrusted content (file reads,
// search matches) in <UNTRUSTED_DATA> tags after injection scanning.
// When nil, returns the input unchanged (no-op).
var WrapUntrustedDataFunc func(s string) string

// fileOpsInfo mirrors the root FileOpsSession type for RootDir access.
type fileOpsInfo struct {
	RootDir string
}

// safeSubdirHint calls SubdirContextHintFunc when non-nil, returning empty otherwise.
func safeSubdirHint(dir string) string {
	if SubdirContextHintFunc != nil {
		return SubdirContextHintFunc(dir)
	}
	return ""
}
