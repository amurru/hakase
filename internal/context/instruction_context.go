// instruction_context.go - discovery and rendering of project context files
// (AGENTS.md, with a project-scoped CLAUDE.md fallback) into agent system
// instructions. The semantics follow the conventions used by OpenCode and
// Hermes Agent so context files authored for other agents work unchanged:
//
//   - Project scope: AGENTS.md files are collected from cwd up to the git
//     root (closest first, nested files stack). Only when no AGENTS.md exists
//     anywhere in the walk is CLAUDE.md consulted, and only inside the
//     project - the user-global ~/.claude/CLAUDE.md is deliberately never
//     loaded.
//   - User scope: ~/.hakase/AGENTS.md (or $HAKASE_HOME/AGENTS.md) when
//     present.
//   - config.Config extras: instruction_files (absolute paths, "~/"-prefixed paths,
//     project-relative paths, or http(s) URLs).
//
// Loaded content is prompt-injection scanned (matching files are blocked and
// replaced with a warning) and per-file truncated (Hermes-style 70% head /
// 20% tail split) before being rendered.
package context

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/session"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultContextFileMaxChars caps the per-file content contributed to the
// system instruction (Hermes Agent uses the same 20k default). Content beyond
// the cap is truncated so both the leading instructions and the trailing
// metadata survive.
const DefaultContextFileMaxChars = 20000

// contextFetchTimeout bounds remote instruction URL fetches so a flaky
// network can never hang agent startup.
const contextFetchTimeout = 5 * time.Second

// contextFetchMaxBytes is a hard safety cap on remote instruction content
// (content is truncated to DefaultContextFileMaxChars afterwards anyway).
const contextFetchMaxBytes = 1 << 20 // 1 MiB

// contextThreatPatterns are lightweight prompt-injection markers checked
// against every loaded context file (Hermes Agent's context-scope scan,
// simplified). A match blocks the file and replaces its content with a
// warning so an attacker-controlled AGENTS.md cannot rewrite the agent.
// Matching is case-insensitive substring; the list is deliberately
// conservative to avoid false positives on legitimate content.
var contextThreatPatterns = []string{
	"ignore all previous instructions",
	"ignore previous instructions",
	"ignore all prior instructions",
	"ignore your previous instructions",
	"disregard previous instructions",
	"disregard all previous instructions",
	"override your instructions",
	"reveal your system prompt",
	"print your system prompt",
	"output your system prompt",
	"print your instructions",
	"you are now a",
	"<|im_start|>system",
	"<system prompt>",
	"</system prompt>",
}

// contextThreatRegexps are regex patterns that catch obfuscated prompt-injection
// variants (extra whitespace, filler words, alternative phrasings) that the
// fixed-phrase substring list would miss. Matching runs against the
// whitespace-normalized lowercase input.
var contextThreatRegexps = []*regexp.Regexp{
	regexp.MustCompile(`ignore\s+.{0,40}instructions?`),
	regexp.MustCompile(`disregard\s+.{0,40}(directive|instruction|prompt)`),
	regexp.MustCompile(`forget\s+.{0,40}(previous|prior|all).{0,40}(instruction|directive|prompt|rule)`),
	regexp.MustCompile(`you\s+are\s+now\s+(an?\s+)?(unrestricted|different|new)\s+(model|assistant|ai)`),
	regexp.MustCompile(`<\|im_start\|>(user|system)`),
	regexp.MustCompile(`system:\s*new\s+instructions?\s+follow`),
}

// whitespaceRun is used to collapse all whitespace runs into a single space
// during normalization.
var whitespaceRun = regexp.MustCompile(`\s+`)

// InstructionFile is a single discovered context file.
type InstructionFile struct {
	Path    string // absolute path or URL, used in the "Instructions from:" header
	Content string // full content (truncated and injection-scanned at render time)
}

// ContextBlockTokens is the token estimate of the rendered project-context
// block, set once in setupRunner. It is folded into the compaction reserve in
// context.go so large AGENTS.md files do not silently blow the token budget.
var ContextBlockTokens int

// GitWorkspaceBlockTokens is the token estimate of the rendered git workspace
// snapshot block, set once in setupRunner next to ContextBlockTokens. Folded
// into the same compaction reserve so the snapshot cannot silently blow the
// token budget.
var GitWorkspaceBlockTokens int

// FindProjectRootFunc resolves the project root from a starting directory.
// Set by root's skill_discovery.go in sandbox_hooks_init.go.
var FindProjectRootFunc func(cwd string) string

// findProjectRoot calls the hook; returns cwd when unset.
func findProjectRoot(cwd string) string {
	if FindProjectRootFunc != nil {
		return FindProjectRootFunc(cwd)
	}
	return cwd
}

// DiscoveredInstructionFiles returns the context files that apply to the
// current workspace, in render order:
//
//  1. Project AGENTS.md files from cwd up to the git root (closest first).
//     Only when no AGENTS.md exists anywhere in the walk is CLAUDE.md
//     consulted as a project-scoped fallback (never user-global).
//  2. User-level ~/.hakase/AGENTS.md when present. The Claude Code global
//     (~/.claude/CLAUDE.md) is deliberately NOT loaded.
//  3. Extra files from cfg.InstructionFiles (paths or http(s) URLs).
//
// Missing, unreadable, or injection-flagged files are never fatal to
// discovery; they are skipped with a logged warning (or blocked with a
// placeholder). cfg may be nil (config extras skipped).
func DiscoveredInstructionFiles(cwd string, cfg *config.Config, log interfaces.LogFunc) []InstructionFile {
	var out []InstructionFile
	seen := make(map[string]bool)

	add := func(p string, remote bool) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		content := readContextFile(p, remote, log)
		if content == "" {
			return
		}
		if _, reason := ScanContextForInjection(content); reason != "" && log != nil {
			log(fmt.Sprintf("[context] instruction file %q blocked: potential prompt injection (%s)", p, reason))
		}
		content = SanitizeContextContent(content)
		out = append(out, InstructionFile{Path: p, Content: content})
	}

	root := findProjectRoot(cwd)

	// Project scope: AGENTS.md at every level from cwd up to the git root
	// (closest first); CLAUDE.md only as a whole-walk fallback.
	for _, p := range findProjectContextFiles(cwd, root) {
		add(p, false)
	}

	// User scope: ~/.hakase/AGENTS.md only.
	if home := config.HakaseHome(); home != "" {
		add(filepath.Join(home, "AGENTS.md"), false)
	}

	// config.Config extras.
	if cfg != nil {
		for _, raw := range cfg.InstructionFiles {
			if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
				add(raw, true)
				continue
			}
			add(expandContextPath(raw, root), false)
		}
	}

	return out
}

// findProjectContextFiles walks from dir up to root (inclusive), collecting
// AGENTS.md files closest-first. If no AGENTS.md exists anywhere in the walk,
// it falls back to collecting project-scoped CLAUDE.md files the same way.
// Only the first matching type is ever used.
func findProjectContextFiles(dir, root string) []string {
	if candidates := walkUpFor(dir, root, "AGENTS.md"); len(candidates) > 0 {
		return candidates
	}
	return walkUpFor(dir, root, "CLAUDE.md")
}

// walkUpFor collects every occurrence of name from dir up to root
// (inclusive), closest first.
func walkUpFor(dir, root, name string) []string {
	var found []string
	for d := dir; ; {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			found = append(found, p)
		}
		if d == root {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return found
}

// expandContextPath expands a config instruction path: a leading "~/" is
// expanded against the user home, absolute paths pass through, and relative
// paths resolve against the project root.
func expandContextPath(raw, root string) string {
	if strings.HasPrefix(raw, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, raw[2:])
		}
		return raw
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	return filepath.Join(root, raw)
}

// readContextFile reads a local file or fetches a remote URL. Local files
// are read directly; remote URLs use a short timeout with a hard byte cap.
// Any failure returns "" after logging a warning - context loading must
// never fail agent startup.
func readContextFile(p string, remote bool, log interfaces.LogFunc) string {
	if remote {
		client := &http.Client{Timeout: contextFetchTimeout}
		resp, err := client.Get(p)
		if err != nil {
			if log != nil {
				log(fmt.Sprintf("[context] failed to fetch instruction URL %q: %v", p, err))
			}
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if log != nil {
				log(fmt.Sprintf("[context] instruction URL %q returned status %d", p, resp.StatusCode))
			}
			return ""
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, contextFetchMaxBytes))
		if err != nil {
			if log != nil {
				log(fmt.Sprintf("[context] failed to read instruction URL %q: %v", p, err))
			}
			return ""
		}
		return string(b)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("[context] skipped unreadable instruction file %q: %v", p, err))
		}
		return ""
	}
	return string(b)
}

// ScanContextForInjection checks content against contextThreatPatterns and
// contextThreatRegexps. Content is normalized (lowercased, whitespace collapsed)
// before matching to catch obfuscated variants with extra spaces or newlines.
// It returns (false, matchedPattern) when the content looks like a prompt
// injection attempt, (true, "") otherwise.
func ScanContextForInjection(content string) (bool, string) {
	lower := strings.ToLower(content)
	normalized := whitespaceRun.ReplaceAllString(lower, " ")
	for _, pat := range contextThreatPatterns {
		if strings.Contains(normalized, pat) {
			return false, pat
		}
	}
	for _, re := range contextThreatRegexps {
		if loc := re.FindStringIndex(normalized); loc != nil {
			return false, normalized[loc[0]:loc[1]]
		}
	}
	return true, ""
}

// SanitizeContextContent scans content for prompt-injection patterns and
// returns it unchanged when safe, or a blocked placeholder when a threat
// pattern matches. Used for every piece of file-derived content that enters
// the model context: context files, subdirectory hints, knowledge notes, and
// markdown skill bodies.
func SanitizeContextContent(content string) string {
	if ok, reason := ScanContextForInjection(content); !ok {
		return fmt.Sprintf("[BLOCKED: potential prompt injection detected (%s); content omitted]", reason)
	}
	return content
}

// WrapUntrustedData frames the given string in <UNTRUSTED_DATA> tags after
// sanitizing it through SanitizeContextContent, so untrusted user-supplied
// content that enters the model context is clearly delimited. Content that
// already contains <UNTRUSTED_DATA> is returned unchanged (double-wrap
// prevention) after sanitization.
func WrapUntrustedData(s string) string {
	if strings.Contains(s, "<UNTRUSTED_DATA>") {
		return SanitizeContextContent(s)
	}
	sanitized := SanitizeContextContent(s)
	return "\n<UNTRUSTED_DATA>\n" + sanitized + "\n</UNTRUSTED_DATA>\n"
}

// RenderInstructionBlock renders the discovered context files into the
// instruction section appended to agent system prompts. The header mirrors
// hakase's existing "### SECTION:" prompt style; each file uses the
// cross-agent "Instructions from: <path>" convention so content stays
// identifiable. cfgInstruction (the historical config.instruction field, now
// active as an additional customization - never a replacement) is rendered
// as its own section. Returns "" when there is nothing to add.
func RenderInstructionBlock(files []InstructionFile, cfgInstruction string, maxChars int) string {
	var sections []string
	if len(files) > 0 {
		var b strings.Builder
		b.WriteString("### PROJECT CONTEXT FILES:")
		b.WriteString("\nThe following project context files have been loaded and should be followed:")
		for _, f := range files {
			b.WriteString("\n\nInstructions from: " + f.Path + "\n" + TruncateContextFile(f.Content, maxChars))
		}
		sections = append(sections, b.String())
	}
	if inst := strings.TrimSpace(cfgInstruction); inst != "" {
		sections = append(sections, "### USER CONFIG INSTRUCTION:\n"+inst)
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// TruncateContextFile caps content at maxChars using a 70% head / 20% tail
// split with a marker in the middle (Hermes Agent's approach). This keeps
// the leading instructions and trailing metadata while bounding prompt cost.
// maxChars <= 0 falls back to DefaultContextFileMaxChars.
func TruncateContextFile(content string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultContextFileMaxChars
	}
	if len(content) <= maxChars {
		return content
	}
	marker := "\n... [truncated] ...\n"
	budget := maxChars - len(marker)
	if budget < 1 {
		budget = 1
	}
	head := budget * 70 / 100
	tail := budget - head
	return content[:head] + marker + content[len(content)-tail:]
}

// ---------------------------------------------------------------------------
// Phase 2: progressive subdirectory context hints and live reconcile.
//
// Subdirectory hints (Hermes Agent's SubdirectoryHintTracker): when the agent
// reads a file or searches a directory below the workspace root, any AGENTS.md
// in that directory tree (not already in the system prompt block) is attached
// to the tool result, capped per file, once per session.
//
// Live reconcile: the BeforeModelCallback checks a cheap fingerprint of the
// loaded context files; when one changes mid-session, an update notice is
// injected once so the model follows the new instructions.
// ---------------------------------------------------------------------------

// defaultHintMaxChars caps each subdirectory hint file (Hermes uses 8k).
const defaultHintMaxChars = 8000

// maxHintParentLevels bounds how many parent directories are walked when
// attaching a subdirectory context hint (Hermes checks dir + 5 parents).
const maxHintParentLevels = 5

// Session-scoped state for progressive hints and live reconcile, set once in
// setupRunner via initContextState.
var (
	// CurrentContextCwd is the workspace root captured at startup; hints walk
	// from a file's directory up to here (the root's own context file is
	// already in the system prompt).
	CurrentContextCwd string
	// CurrentContextType is the winning project context file name from
	// startup ("AGENTS.md" or "CLAUDE.md"); hints look for that type only.
	CurrentContextType string
	// startupContextPaths are the context file paths already rendered into
	// the system prompt block; subdirectory hints never re-attach them.
	startupContextPaths map[string]bool
	// hintedContextPaths dedups hint attachment across the session.
	hintedContextPaths map[string]bool
	// currentContextFiles is the last discovered file set (used to recompute
	// the fingerprint for live reconcile).
	currentContextFiles []InstructionFile
	// currentContextCfg/Instruction/MaxChars snapshot the config used to
	// re-render the block when a file changes mid-session.
	currentContextCfg         *config.Config
	currentContextInstruction string
	currentContextMaxChars    int
	// currentContextFingerprint hashes (path, size, mtime) of the local
	// context files; a change triggers the update notice.
	currentContextFingerprint string
)

// InitContextState records the session-scoped state for progressive
// subdirectory hints and live reconcile. Called once in setupRunner.
func InitContextState(cwd string, cfg *config.Config, files []InstructionFile) {
	CurrentContextCwd = cwd
	currentContextFiles = files
	startupContextPaths = make(map[string]bool)
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "http://") && !strings.HasPrefix(f.Path, "https://") {
			startupContextPaths[f.Path] = true
		}
	}
	hintedContextPaths = make(map[string]bool)
	CurrentContextType = ProjectContextType(cwd, findProjectRoot(cwd))
	if cfg != nil {
		currentContextCfg = cfg
		currentContextInstruction = cfg.Instruction
		currentContextMaxChars = cfg.ContextFiles.MaxChars
	}
	currentContextFingerprint = contextFilesFingerprint()
}

// ProjectContextType returns the winning project context file name for the
// walk from cwd up to root: "AGENTS.md" when one exists anywhere, otherwise
// the project-scoped "CLAUDE.md" fallback.
func ProjectContextType(cwd, root string) string {
	if len(walkUpFor(cwd, root, "AGENTS.md")) > 0 {
		return "AGENTS.md"
	}
	return "CLAUDE.md"
}

// subdirContextHint returns a rendered context hint for dir: the project
// context files (the winning type from startup) from dir up to the workspace
// root that are not already part of the system prompt block. Each file is
// attached at most once per session, is injection-scanned, and is capped at
// defaultHintMaxChars. Returns "" when there is nothing to attach or the
// feature is uninitialized.
func SubdirContextHint(dir string) string {
	if CurrentContextCwd == "" || CurrentContextType == "" {
		return ""
	}
	var hints []string
	levels := 0
	for d := dir; ; {
		// The workspace root's context file is already in the system prompt.
		if d == CurrentContextCwd {
			break
		}
		p := filepath.Join(d, CurrentContextType)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if !startupContextPaths[p] && !hintedContextPaths[p] {
				hintedContextPaths[p] = true
				if data, rerr := os.ReadFile(p); rerr == nil {
					hints = append(hints, "Instructions from: "+p+"\n"+TruncateContextFile(SanitizeContextContent(string(data)), defaultHintMaxChars))
				}
			}
		}
		levels++
		if levels >= maxHintParentLevels {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if len(hints) == 0 {
		return ""
	}
	return "\n\n### SUBDIRECTORY CONTEXT:\n" + strings.Join(hints, "\n\n")
}

// contextFilesFingerprint hashes (path, size, mtime) of every local context
// file. Remote URLs are skipped (they cannot be stat'd and are only fetched
// at startup). Returns "" when no local files exist.
func contextFilesFingerprint() string {
	h := sha256.New()
	n := 0
	for _, f := range currentContextFiles {
		if strings.HasPrefix(f.Path, "http://") || strings.HasPrefix(f.Path, "https://") {
			continue
		}
		st, err := os.Stat(f.Path)
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s:%d:%d\n", f.Path, st.Size(), st.ModTime().UnixNano())
		n++
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ContextUpdateNotice checks whether any project context file changed since
// startup (or the last check). On a change it re-discovers the context files,
// re-renders the block, updates the stored state, and returns an update
// notice for the model to follow. Returns "" when nothing changed.
func ContextUpdateNotice() string {
	if CurrentContextCwd == "" {
		return ""
	}
	if contextFilesFingerprint() == currentContextFingerprint {
		return ""
	}
	files := DiscoveredInstructionFiles(CurrentContextCwd, currentContextCfg, nil)
	currentContextFiles = files
	currentContextFingerprint = contextFilesFingerprint()
	block := RenderInstructionBlock(files, currentContextInstruction, currentContextMaxChars)
	if block == "" {
		return ""
	}
	return "### PROJECT CONTEXT UPDATE:\nThe project context files changed during this session. Follow the updated instructions:\n" + strings.TrimPrefix(block, "\n\n")
}

// EffectiveMaxChars returns the configured per-file cap, falling back to the
// 20k default. Used by the rules CLI to report truncation status.
func EffectiveMaxChars(cfg *config.Config) int {
	if cfg != nil && cfg.ContextFiles.MaxChars > 0 {
		return cfg.ContextFiles.MaxChars
	}
	return DefaultContextFileMaxChars
}

// BuildHintedPaths exports the current hint-dedup set as a sorted slice.
// Used by sandbox_hooks_init.go to wire session.BuildHintedPathsHook.
func BuildHintedPaths() []string {
	paths := make([]string, 0, len(hintedContextPaths))
	for p := range hintedContextPaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// ResetHintedContextPaths clears the in-memory hint dedup set. Used by tests.
func ResetHintedContextPaths() {
	hintedContextPaths = make(map[string]bool)
}

// SeedHintedContextPaths merges persisted hinted paths (from a resumed
// session) into the in-memory dedup set so hints attached in a previous run
// are not re-attached. Called whenever a session is loaded.
func SeedHintedContextPaths(paths []string) {
	if hintedContextPaths == nil {
		hintedContextPaths = make(map[string]bool)
	}
	for _, p := range paths {
		hintedContextPaths[p] = true
	}
}

// SyncHintedPaths persists the in-memory hint dedup set onto the session so
// a resumed session does not re-attach subdirectory context hints.
func SyncHintedPaths(s *session.Session) {
	if s == nil {
		return
	}
	paths := make([]string, 0, len(hintedContextPaths))
	for p := range hintedContextPaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	s.HintedContextFiles = paths
}
