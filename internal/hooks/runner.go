package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
)

// AdditionalContextKey is the result key a PostToolUse hook's context is
// appended under when it overrides the tool result.
const AdditionalContextKey = "hook_additional_context"

// SessionStartContextCap bounds the total context SessionStart handlers
// may inject (Claude parity: 10k chars, then truncate).
const SessionStartContextCap = 10000

// compiledGroup is a Group with its matcher compiled. A nil matcher matches
// every tool name.
type compiledGroup struct {
	raw      string
	matcher  *regexp.Regexp
	handlers []Handler
}

func (g compiledGroup) matches(toolName string) bool {
	if g.matcher == nil {
		return true
	}
	return g.matcher.MatchString(toolName)
}

// Runner holds the validated, compiled hook set for one process. The zero
// Runner is disabled and every Check method is a no-op on it, so call sites
// never need a nil check.
type Runner struct {
	log  func(string)
	pre  []compiledGroup
	post []compiledGroup
	// session holds the user-layer SessionStart groups.
	session []compiledGroup
	// projectEnabled gates the project layer (<root>/.hakase/hooks.json).
	projectEnabled bool
	// trust gates project handlers. Nil means trust nothing (safe default:
	// every project handler is skipped).
	trust TrustChecker

	// projCache memoizes loaded project files by root, keyed on
	// (mtime, size) so edits — and mid-session trust grants, via the
	// store's own mtime cache — take effect without a restart.
	projMu    sync.Mutex
	projCache map[string]projectCacheEntry

	// warned remembers skipped-as-untrusted fingerprints already warned
	// about, so every tool call doesn't re-log. Trust grants are NOT
	// forgotten here (a lapse re-warns only after a process restart;
	// the skip itself happens every call regardless).
	warnedMu sync.Mutex
	warned   map[string]bool

	// fired tracks sessions already served by RunSessionStart (plus
	// in-flight claims), mirroring HistoryBuilder's reserve/rollback so
	// SessionStart context injects exactly once per session.
	firedMu  sync.Mutex
	fired    map[string]bool
	inflight map[string]bool
}

type projectCacheEntry struct {
	mtime   time.Time
	size    int64
	pre     []compiledGroup
	post    []compiledGroup
	session []compiledGroup
}

// compileGroups compiles validated groups (compile cannot fail after
// Validate; a failure is skipped loudly rather than panicking).
func compileGroups(groups []Group) []compiledGroup {
	var out []compiledGroup
	for _, g := range groups {
		cg, err := compileGroup(g)
		if err != nil {
			continue
		}
		out = append(out, cg)
	}
	return out
}

// NewRunner validates cfg (bad regex/type/on_failure fail here, so a bad
// block fails startup via LoadConfig, never silently) and compiles matchers.
// An explicitly disabled or group-less config yields a disabled runner.
func NewRunner(cfg Config) (*Runner, error) {
	c := cfg
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{projectEnabled: Enabled(&c) && ProjectLayerEnabled(&c)}
	if !Enabled(&c) {
		return r, nil
	}
	for _, g := range c.PreToolUse {
		cg, err := compileGroup(g)
		if err != nil {
			return nil, err
		}
		r.pre = append(r.pre, cg)
	}
	for _, g := range c.PostToolUse {
		cg, err := compileGroup(g)
		if err != nil {
			return nil, err
		}
		r.post = append(r.post, cg)
	}
	for _, g := range c.SessionStart {
		cg, err := compileGroup(g)
		if err != nil {
			return nil, err
		}
		r.session = append(r.session, cg)
	}
	return r, nil
}

func compileGroup(g Group) (compiledGroup, error) {
	cg := compiledGroup{raw: g.Matcher, handlers: g.Hooks}
	if g.Matcher != "" {
		re, err := regexp.Compile(g.Matcher)
		if err != nil {
			return compiledGroup{}, fmt.Errorf("invalid hooks matcher %q: %v", g.Matcher, err)
		}
		cg.matcher = re
	}
	return cg, nil
}

// SetLog installs an optional warn sink (hook errors on the fail-open path,
// dropped allow-path context). Nil disables logging.
func (r *Runner) SetLog(fn func(string)) {
	r.log = fn
}

func (r *Runner) warnf(format string, args ...any) {
	if r != nil && r.log != nil {
		r.log(fmt.Sprintf(format, args...))
	}
}

// SetTrustStore installs the content-hash trust source for project-layer
// handlers. Nil (the default) trusts nothing: every project handler is
// skipped with a warning.
func (r *Runner) SetTrustStore(s TrustChecker) {
	if r == nil {
		return
	}
	r.trust = s
}

// Enabled reports whether any hook group is loaded. A disabled runner's
// Check methods return allow/passthrough without spawning anything.
func (r *Runner) Enabled() bool {
	return r != nil && (len(r.pre) > 0 || len(r.post) > 0 || len(r.session) > 0)
}

// HasSessionStart reports whether a SessionStart event could ever fire:
// user groups exist, or the project layer is enabled (a project file may
// appear — or be trusted — mid-process).
func (r *Runner) HasSessionStart() bool {
	return r != nil && (len(r.session) > 0 || r.projectEnabled)
}

// Snapshot describes one loaded handler for `hakase hooks list` and the
// Phase-2 trust store. Fingerprint is content-addressed (see Fingerprint).
type Snapshot struct {
	Event       string
	Matcher     string
	Name        string
	Command     []string
	Timeout     int
	OnFailure   string
	Fingerprint string
	// Layer is "user" (own config, always runs) or "project" (trust-gated).
	Layer string
	// Trusted is true for user-layer handlers and for trusted project
	// handlers; false for skipped-as-untrusted project handlers.
	Trusted bool
}

// Snapshots lists every loaded user-layer handler in config order.
func (r *Runner) Snapshots() []Snapshot {
	if r == nil {
		return nil
	}
	var out []Snapshot
	for _, g := range r.pre {
		for _, h := range g.handlers {
			out = append(out, Snapshot{Event: EventPreToolUse, Matcher: g.raw, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true})
		}
	}
	for _, g := range r.post {
		for _, h := range g.handlers {
			out = append(out, Snapshot{Event: EventPostToolUse, Matcher: g.raw, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true})
		}
	}
	for _, g := range r.session {
		for _, h := range g.handlers {
			out = append(out, Snapshot{Event: EventSessionStart, Matcher: g.raw, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true})
		}
	}
	return out
}

// ProjectSnapshots lists the project-layer handlers for root with their
// trust status. Nil when the layer is disabled, rootless, or file-less.
// Used by `hakase hooks list`; the agent path uses projectGroups instead
// (ctx-rooted, mtime-cached).
func (r *Runner) ProjectSnapshots(root string) []Snapshot {
	if r == nil || !r.projectEnabled || strings.TrimSpace(root) == "" {
		return nil
	}
	f, err := LoadProjectFile(root)
	if err != nil || f == nil {
		return nil
	}
	var out []Snapshot
	collect := func(event string, groups []Group) {
		for _, g := range groups {
			for _, h := range g.Hooks {
				fp := h.Fingerprint()
				out = append(out, Snapshot{Event: event, Matcher: g.Matcher, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: fp, Layer: "project", Trusted: r.isTrusted(fp)})
			}
		}
	}
	collect(EventPreToolUse, f.PreToolUse)
	collect(EventPostToolUse, f.PostToolUse)
	collect(EventSessionStart, f.SessionStart)
	return out
}

// payload is the JSON object piped to a hook's stdin (Claude-compatible
// subset; see spec HK-002).
type payload struct {
	HookEventName string         `json:"hook_event_name"`
	SessionID     string         `json:"session_id,omitempty"`
	InvocationID  string         `json:"invocation_id,omitempty"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	ToolResponse  map[string]any `json:"tool_response,omitempty"`
	CWD           string         `json:"cwd,omitempty"`
	Timestamp     string         `json:"timestamp"`
}

func buildPayload(ctx context.Context, event, toolName string, toolInput, toolResponse map[string]any) payload {
	p := payload{
		HookEventName: event,
		ToolName:      toolName,
		ToolInput:     toolInput,
		ToolResponse:  toolResponse,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	// Session identity is best-effort by contract: "" outside a probed run
	// (CLI utilities, tests), which keeps those paths transport-agnostic.
	// Every probe is recover-guarded: ADK test doubles (ContextMock) panic
	// on context methods, and a hook must never kill a turn over identity.
	p.SessionID = safeSessionID(ctx)
	p.InvocationID = safeInvocationID(ctx)
	p.CWD = safeCWD(ctx)
	return p
}

// safeSessionID resolves the hakase session (falling back to the ADK task
// id) without ever panicking on a hostile context.
func safeSessionID(ctx context.Context) (id string) {
	defer func() { _ = recover() }()
	if ctx == nil {
		return ""
	}
	// Both helpers are themselves recover-guarded (interfaces/tasksession.go).
	if s := interfaces.SessionIDFromCtx(ctx); s != "" {
		return s
	}
	return interfaces.TaskIDFromCtx(ctx)
}

func safeInvocationID(ctx context.Context) (id string) {
	defer func() { _ = recover() }()
	if ctx == nil {
		return ""
	}
	if ic, ok := ctx.(interface{ InvocationID() string }); ok {
		return ic.InvocationID()
	}
	return ""
}

func safeCWD(ctx context.Context) string {
	defer func() { _ = recover() }()
	if ctx != nil {
		if root := project.RootFrom(ctx); root != "" {
			return root
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// CheckPreToolUse runs the matching PreToolUse handlers sequentially in
// config order. It returns blocked=true with a model-legible result map when
// a hook denies (first block wins, deny-wins); otherwise blocked=false and
// the tool must run. A nil/empty toolInput is fine.
//
// Layers run user-first, then the trust-gated project layer (HK-103): an
// untrusted project handler is skipped with a once-per-process warning, and
// the tool proceeds.
func (r *Runner) CheckPreToolUse(ctx context.Context, toolName string, toolInput map[string]any) (bool, map[string]any) {
	if r == nil {
		return false, nil
	}
	p := buildPayload(ctx, EventPreToolUse, toolName, toolInput, nil)
	if blocked, res := r.runPreGroups(ctx, p, r.pre, toolName); blocked {
		return true, res
	}
	// Project layer: each handler runs only when its content fingerprint
	// is trusted; anything else is skipped loudly and the tool proceeds.
	pre, _, _ := r.projectGroups(ctx)
	for _, g := range pre {
		if !g.matches(toolName) {
			continue
		}
		for _, h := range g.handlers {
			fp := h.Fingerprint()
			if !r.isTrusted(fp) {
				r.warnUntrustedOnce(fp, displayName(h))
				continue
			}
			if blocked, res := r.stepPre(ctx, p, h, toolName); blocked {
				return true, res
			}
		}
	}
	return false, nil
}

// runPreGroups runs compiled user-layer groups (no trust gate: own config).
func (r *Runner) runPreGroups(ctx context.Context, p payload, groups []compiledGroup, toolName string) (bool, map[string]any) {
	for _, g := range groups {
		if !g.matches(toolName) {
			continue
		}
		for _, h := range g.handlers {
			if blocked, res := r.stepPre(ctx, p, h, toolName); blocked {
				return true, res
			}
		}
	}
	return false, nil
}

// stepPre runs one PreToolUse handler: block=true stops the whole chain.
func (r *Runner) stepPre(ctx context.Context, p payload, h Handler, toolName string) (bool, map[string]any) {
	v := r.runHandler(ctx, EventPreToolUse, h, p)
	switch {
	case v.block:
		return true, blockResult(toolName, h, v.reason)
	case v.hookErr != "":
		if h.OnFailure == OnFailureBlock {
			return true, blockResult(toolName, h, "hook error (fail-closed): "+v.hookErr)
		}
		r.warnf("hooks: PreToolUse %q failed open: %s", displayName(h), v.hookErr)
	default:
		// Allow-path additionalContext has no ADK delivery channel:
		// BeforeToolCallback can only allow (nil,nil) or skip the
		// tool (non-nil). Log it so it is not silently lost.
		if v.additionalContext != "" {
			r.warnf("hooks: PreToolUse %q context (allow path, not delivered to model): %s", displayName(h), v.additionalContext)
		}
	}
	return false, nil
}

// CheckPostToolUse runs the matching PostToolUse handlers and returns nil to
// pass the tool result through unchanged, or a copy of the result with
// collected hook context appended under AdditionalContextKey. PostToolUse
// can never block (ecosystem-wide rule); an exit-2 there is folded into the
// collected context, never a denial.
//
// When toolErr is non-nil the override is suppressed and nil is returned:
// ADK drops the error whenever an AfterTool callback returns a non-nil
// result, so overriding a failed call would convert the failure into a
// success and the model would never see it. The failure reaches the model
// intact; the undelivered context goes to the warn log instead of vanishing.
func (r *Runner) CheckPostToolUse(ctx context.Context, toolName string, toolInput, toolResult map[string]any, toolErr error) map[string]any {
	if r == nil {
		return nil
	}
	p := buildPayload(ctx, EventPostToolUse, toolName, toolInput, toolResult)
	var contexts []string
	for _, g := range r.post {
		if !g.matches(toolName) {
			continue
		}
		for _, h := range g.handlers {
			r.stepPost(ctx, p, h, &contexts)
		}
	}
	// Project layer, trust-gated like PreToolUse.
	_, post, _ := r.projectGroups(ctx)
	for _, g := range post {
		if !g.matches(toolName) {
			continue
		}
		for _, h := range g.handlers {
			fp := h.Fingerprint()
			if !r.isTrusted(fp) {
				r.warnUntrustedOnce(fp, displayName(h))
				continue
			}
			r.stepPost(ctx, p, h, &contexts)
		}
	}
	if len(contexts) == 0 {
		return nil
	}
	joined := strings.Join(contexts, "\n\n")
	if toolErr != nil {
		r.warnf("hooks: PostToolUse context on failed tool %q not delivered (error preserved): %s", toolName, joined)
		return nil
	}
	out := make(map[string]any, len(toolResult)+1)
	for k, v := range toolResult {
		out[k] = v
	}
	out[AdditionalContextKey] = joined
	return out
}

// stepPost runs one PostToolUse handler, appending any context to dst.
// Hook errors are fail-open by construction (Validate rejects
// on_failure:block off PreToolUse).
func (r *Runner) stepPost(ctx context.Context, p payload, h Handler, dst *[]string) {
	v := r.runHandler(ctx, EventPostToolUse, h, p)
	if v.hookErr != "" {
		r.warnf("hooks: PostToolUse %q failed open: %s", displayName(h), v.hookErr)
		return
	}
	if v.block {
		*dst = append(*dst, fmt.Sprintf("hook %q reported: %s", displayName(h), v.reason))
		return
	}
	if v.additionalContext != "" {
		*dst = append(*dst, v.additionalContext)
	}
}

// isTrusted reports whether a project-handler fingerprint is trusted. A
// nil store (or nil runner) trusts nothing: the safe default that keeps
// existing unit tests meaningful.
func (r *Runner) isTrusted(fp string) bool {
	if r == nil || r.trust == nil || fp == "" {
		return false
	}
	return r.trust.Trusted(fp)
}

// warnUntrustedOnce warns about a skipped-as-untrusted project hook once
// per process per fingerprint, naming the trust command. The skip itself
// happens on every call regardless; only the log is deduplicated.
func (r *Runner) warnUntrustedOnce(fp, name string) {
	r.warnedMu.Lock()
	defer r.warnedMu.Unlock()
	if r.warned == nil {
		r.warned = map[string]bool{}
	}
	if r.warned[fp] {
		return
	}
	r.warned[fp] = true
	r.warnf("hooks: skipping untrusted project hook %q (%s) — run `hakase hooks trust` to review and trust it", name, fp)
}

// safeProjectRoot resolves the per-turn project root without ever
// panicking on a hostile context. "" means no project identity: no
// project layer, no project hooks.
func safeProjectRoot(ctx context.Context) string {
	defer func() { _ = recover() }()
	if ctx == nil {
		return ""
	}
	return project.RootFrom(ctx)
}

// projectGroups returns the compiled project layer for this turn's root.
// Results are mtime-cached per root so edits take effect without a
// restart; a broken file warns (once per root per mtime) and yields
// nothing, never breaking the user's own hooks.
func (r *Runner) projectGroups(ctx context.Context) (pre, post, sess []compiledGroup) {
	if r == nil || !r.projectEnabled {
		return nil, nil, nil
	}
	root := safeProjectRoot(ctx)
	if root == "" {
		return nil, nil, nil
	}
	path := ProjectHooksPath(root)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil // absent (or transient stat failure): skip quietly
	}
	r.projMu.Lock()
	defer r.projMu.Unlock()
	if r.projCache == nil {
		r.projCache = map[string]projectCacheEntry{}
	}
	if e, ok := r.projCache[root]; ok && e.mtime.Equal(fi.ModTime()) && e.size == fi.Size() {
		return e.pre, e.post, e.session
	}
	f, err := LoadProjectFile(root)
	if err != nil {
		r.warnf("hooks: ignoring broken project hooks %s: %v (`hakase hooks test` diagnoses it)", path, err)
		r.projCache[root] = projectCacheEntry{mtime: fi.ModTime(), size: fi.Size()}
		return nil, nil, nil
	}
	if f == nil {
		return nil, nil, nil
	}
	e := projectCacheEntry{mtime: fi.ModTime(), size: fi.Size(), pre: compileGroups(f.PreToolUse), post: compileGroups(f.PostToolUse), session: compileGroups(f.SessionStart)}
	r.projCache[root] = e
	return e.pre, e.post, e.session
}

// RunSessionStart runs SessionStart handlers once per session and returns
// the context to inject ("" = nothing). The once-per-session claim mirrors
// HistoryBuilder's reserve/rollback: an empty result unmarks the session,
// so a later call — e.g. after a mid-session `hakase hooks trust` — can
// still fire. Session-less surfaces share the "process" key (fire once per
// process).
func (r *Runner) RunSessionStart(ctx context.Context) string {
	if r == nil || !r.HasSessionStart() {
		return ""
	}
	key := safeSessionID(ctx)
	if key == "" {
		key = "process"
	}
	r.firedMu.Lock()
	if r.fired == nil {
		r.fired = map[string]bool{}
		r.inflight = map[string]bool{}
	}
	if r.fired[key] || r.inflight[key] {
		r.firedMu.Unlock()
		return ""
	}
	r.inflight[key] = true
	r.firedMu.Unlock()

	p := buildPayload(ctx, EventSessionStart, "", nil, nil)
	var parts []string
	for _, g := range r.session {
		for _, h := range g.handlers {
			if c := r.runSessionHandler(ctx, p, h); c != "" {
				parts = append(parts, c)
			}
		}
	}
	_, _, sess := r.projectGroups(ctx)
	for _, g := range sess {
		for _, h := range g.handlers {
			fp := h.Fingerprint()
			if !r.isTrusted(fp) {
				r.warnUntrustedOnce(fp, displayName(h))
				continue
			}
			if c := r.runSessionHandler(ctx, p, h); c != "" {
				parts = append(parts, c)
			}
		}
	}
	out := TruncateRunes(strings.TrimSpace(strings.Join(parts, "\n\n")), SessionStartContextCap)

	r.firedMu.Lock()
	delete(r.inflight, key)
	if out != "" {
		r.fired[key] = true
	}
	r.firedMu.Unlock()
	return out
}

// runSessionHandler runs one SessionStart handler and returns its context
// contribution ("" = none). The output contract differs from tool events
// on purpose: exit-0 plain stdout IS model-visible context here (there is
// no tool result to protect), alongside JSON additionalContext. Exit 2
// cannot block a session start — it is a warn-and-continue error like any
// other failure (on_failure:block is rejected on SessionStart at Validate,
// so every error here is fail-open by construction).
func (r *Runner) runSessionHandler(ctx context.Context, p payload, h Handler) string {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds
	}
	stdin, err := json.Marshal(p)
	if err != nil {
		r.warnf("hooks: SessionStart %q payload error: %v", displayName(h), err)
		return ""
	}
	stdout, stderr, timedOut, runErr := runCommand(ctx, time.Duration(timeout)*time.Second, h.Command, hookEnv(EventSessionStart, p), p.CWD, stdin)
	name := displayName(h)
	if timedOut {
		r.warnf("hooks: SessionStart %q timed out after %ds", name, timeout)
		return ""
	}
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			r.warnf("hooks: SessionStart %q exec failed: %v", name, runErr)
			return ""
		}
	}
	if exitCode != 0 {
		msg := TruncateRunes(strings.TrimSpace(string(stderr)), 200)
		if msg == "" {
			msg = fmt.Sprintf("exit %d", exitCode)
		}
		r.warnf("hooks: SessionStart %q failed open (%s)", name, msg)
		return ""
	}
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] != '{' {
		return string(trimmed)
	}
	ctxOut, ignored, err := sessionContextFromStdout(stdout)
	if err != nil {
		r.warnf("hooks: SessionStart %q emitted invalid JSON: %v", name, err)
		return ""
	}
	if ignored {
		r.warnf("hooks: SessionStart %q returned updatedInput, which is ignored (v1 has no arg-rewrite plumbing)", name)
	}
	return ctxOut
}

// sessionContextFromStdout extracts SessionStart context from exit-0
// output: plain text passes through verbatim; JSON yields additionalContext.
// ("", nil) for empty output.
func sessionContextFromStdout(stdout []byte) (string, bool, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return "", false, nil
	}
	if trimmed[0] != '{' {
		return string(trimmed), false, nil
	}
	var m map[string]any
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return "", false, err
	}
	ignored := hasKey(m, "updatedInput", "updated_input")
	if hso := subMap(m, "hookSpecificOutput", "hook_specific_output"); hso != nil && hasKey(hso, "updatedInput", "updated_input") {
		ignored = true
	}
	return joinContext(firstStrList(subMap(m, "hookSpecificOutput", "hook_specific_output"), "additionalContext", "additional_context")), ignored, nil
}

// blockResult is the model-legible denial returned as the tool result so the
// block lands in the audit/canvas trail like any other tool outcome.
func blockResult(toolName string, h Handler, reason string) map[string]any {
	if reason == "" {
		reason = "blocked by hook"
	}
	return map[string]any{
		"error":      fmt.Sprintf("tool %q blocked by hook %q: %s", toolName, displayName(h), reason),
		"blocked_by": "hook",
		"hook":       displayName(h),
		"tool":       toolName,
	}
}

func displayName(h Handler) string {
	if h.Name != "" {
		return h.Name
	}
	if len(h.Command) > 0 {
		return h.Command[0]
	}
	return "unnamed"
}

// verdict is one handler run's parsed outcome.
type verdict struct {
	block             bool
	reason            string
	additionalContext string
	hookErr           string
	// ignoredUpdate is true when the hook returned updatedInput: v1 has no
	// arg-rewrite plumbing, so the field is dropped. Callers warn rather
	// than failing, so a Claude-ported hook degrades loudly, not silently.
	ignoredUpdate bool
}

// runHandler execs one handler (argv, no shell) with the payload on stdin
// and parses the verdict. The parent ctx carries cancellation; the per-hook
// timeout bounds the run and kills the process group on expiry.
func (r *Runner) runHandler(ctx context.Context, event string, h Handler, p payload) verdict {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds
	}
	stdin, err := json.Marshal(p)
	if err != nil {
		return verdict{hookErr: fmt.Sprintf("cannot marshal hook payload: %v", err)}
	}
	stdout, stderr, timedOut, runErr := runCommand(ctx, time.Duration(timeout)*time.Second, h.Command, hookEnv(event, p), p.CWD, stdin)
	v := parseVerdict(timeout, stdout, stderr, timedOut, runErr)
	if v.ignoredUpdate {
		r.warnf("hooks: %q returned updatedInput, which is ignored (v1 has no arg-rewrite plumbing)", displayName(h))
	}
	return v
}

// runCommand execs argv with env, dir, and stdin, bounded by timeout. It
// returns stdout/stderr, whether the timeout fired, and the raw run error
// (non-zero exits surface as *exec.ExitError, which is NOT a hook error by
// itself — parseVerdict decides that from the exit code).
func runCommand(parent context.Context, timeout time.Duration, argv []string, env []string, dir string, stdin []byte) (stdout, stderr []byte, timedOut bool, runErr error) {
	if len(argv) == 0 {
		return nil, nil, false, fmt.Errorf("empty hook command")
	}
	// The hook run is bounded by its OWN timeout on a Background-derived
	// context, never by the caller's context directly: ADK test doubles
	// (ContextMock) panic on Deadline/Done, and a hook must never kill a
	// turn over context plumbing. Caller cancellation still propagates via
	// parentDone (recover-guarded).
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if done := parentDone(parent); done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	setProcessGroup(cmd)
	cmd.Env = env
	if dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr = cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
		// CommandContext already SIGKILLed the direct child; reap runaway
		// grandchildren sharing its process group (unix; no-op on windows).
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
		}
	}
	return stdout, stderr, timedOut, runErr
}

// parentDone returns the caller's Done channel, or nil when the caller has
// none (nil context or a hostile test double). Recover-guarded: Done itself
// may panic on ADK ContextMock, and hooks must never kill a turn over it.
func parentDone(parent context.Context) <-chan struct{} {
	if parent == nil {
		return nil
	}
	defer func() { _ = recover() }()
	return parent.Done()
}

// parseVerdict maps (timeout, exit code, stdout JSON) to a verdict.
// Contract (universal across harnesses): exit 2 = block with stderr as the
// reason; exit 0 + JSON decision = block/allow; anything else is a hook
// error resolved per on_failure by the caller.
func parseVerdict(timeoutSecs int, stdout, stderr []byte, timedOut bool, runErr error) verdict {
	if timedOut {
		return verdict{hookErr: fmt.Sprintf("hook timed out after %ds", timeoutSecs)}
	}
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return verdict{hookErr: fmt.Sprintf("hook exec failed: %v", runErr)}
		}
	}
	return parseExit(timeoutSecs, exitCode, stdout, stderr)
}

// parseExit evaluates a known exit code. Split from parseVerdict so the
// verdict matrix is unit-testable without spawning processes.
func parseExit(timeoutSecs, exitCode int, stdout, stderr []byte) verdict {
	switch {
	case exitCode == 2:
		reason := TruncateRunes(strings.TrimSpace(string(stderr)), MaxReasonRunes)
		if reason == "" {
			reason = "hook exited 2"
		}
		return verdict{block: true, reason: reason}
	case exitCode == 0:
		return parseSuccessOutput(stdout)
	default:
		msg := TruncateRunes(strings.TrimSpace(string(stderr)), 200)
		if msg == "" {
			msg = fmt.Sprintf("hook exited %d", exitCode)
		} else {
			msg = fmt.Sprintf("hook exited %d: %s", exitCode, msg)
		}
		return verdict{hookErr: msg}
	}
}

// parseSuccessOutput evaluates exit-0 stdout. Only output starting with "{"
// is treated as a JSON verdict; plain text is ignored (allow), matching
// Claude Code. A "{"-led body that fails to parse IS a hook error (a stray
// echo is the classic broken-hook mistake, and failing open loudly beats
// guessing).
func parseSuccessOutput(stdout []byte) verdict {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return verdict{}
	}
	var m map[string]any
	if err := json.Unmarshal(trimmed, &m); err != nil {
		preview := TruncateRunes(string(trimmed), 200)
		return verdict{hookErr: fmt.Sprintf("hook emitted invalid JSON verdict: %v (output: %s)", err, preview)}
	}
	var v verdict
	if hso := subMap(m, "hookSpecificOutput", "hook_specific_output"); hso != nil {
		pd := strings.ToLower(firstStr(hso, "permissionDecision", "permission_decision"))
		switch pd {
		case "deny", "block":
			v.block = true
		case "ask", "defer":
			// v1 has no approval-prompt or suspend/resume plumbing, so a
			// hook asking to escalate is fail-closed with its reason.
			v.block = true
		}
		v.reason = firstStr(hso, "permissionDecisionReason", "permission_decision_reason", "reason")
		v.additionalContext = joinContext(firstStrList(hso, "additionalContext", "additional_context"))
	}
	if d := strings.ToLower(firstStr(m, "decision")); d == "block" || d == "deny" {
		v.block = true
		if v.reason == "" {
			v.reason = firstStr(m, "reason", "stopReason", "stop_reason")
		}
	}
	if v.reason == "" {
		v.reason = firstStr(m, "reason", "stopReason", "stop_reason")
	}
	// updatedInput (arg rewriting) is not plumbed in v1: flag it so the
	// caller warns instead of silently dropping a Claude-ported hook's
	// rewrite.
	if hasKey(m, "updatedInput", "updated_input") {
		v.ignoredUpdate = true
	} else if hso := subMap(m, "hookSpecificOutput", "hook_specific_output"); hso != nil && hasKey(hso, "updatedInput", "updated_input") {
		v.ignoredUpdate = true
	}
	if c, ok := m["continue"].(bool); ok && !c && !v.block {
		// continue:false with no explicit decision stops the run; on a gate
		// event that is a block.
		v.block = true
		if v.reason == "" {
			v.reason = "hook stopped the run (continue:false)"
		}
	}
	if v.block && v.reason == "" {
		v.reason = "blocked by hook"
	}
	v.reason = TruncateRunes(strings.TrimSpace(v.reason), MaxReasonRunes)
	return v
}

func subMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v, ok := m[k].(map[string]any); ok {
			return v
		}
	}
	return nil
}

// hasKey reports whether any of keys is present in m (value may be nil;
// presence is what matters for unsupported-field detection).
func hasKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// firstStrList reads a string-or-string-list field (additionalContext may be
// either across harnesses).
func firstStrList(m map[string]any, keys ...string) []string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return []string{v}
			}
		case []any:
			var out []string
			for _, e := range v {
				if s, ok := e.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func joinContext(parts []string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// redactNameSubstrings drops env vars whose NAME contains secrets material.
// Name-based (not value-based): matching values would still leak names, and
// Gemini ships redaction off by default, which is the wrong default.
var redactNameSubstrings = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"}

// hookEnv builds the hook child environment: the parent env minus redacted
// names, plus the HAKASE_* hook context.
func hookEnv(event string, p payload) []string {
	out := make([]string, 0, 64)
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.Index(kv, "="); i >= 0 {
			name = kv[:i]
		}
		up := strings.ToUpper(name)
		drop := false
		for _, s := range redactNameSubstrings {
			if strings.Contains(up, s) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	out = append(out,
		"HAKASE_HOOK_EVENT="+event,
		"HAKASE_SESSION_ID="+p.SessionID,
		"HAKASE_PROJECT_DIR="+p.CWD,
		"HAKASE_TOOL_NAME="+p.ToolName,
	)
	return out
}
