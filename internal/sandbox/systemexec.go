package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// DefaultSystemExecTimeout is applied to a synchronous system_exec run when
// the caller omits timeout_seconds.
const DefaultSystemExecTimeout = 120 * time.Second

// runningProcess tracks the live state of a single spawned system process.
// The output buffer and state fields are guarded by mu so they can be read
// while the process is still collecting output in a background goroutine.
type runningProcess struct {
	id        int64
	cmd       *exec.Cmd
	cmdName   string
	args      []string
	startTime time.Time
	doneCh    chan struct{}

	mu       sync.Mutex
	out      bytes.Buffer
	exitCode int
	timedOut bool
	canceled bool
	finished bool
}

// newRunningProcess creates a runningProcess with sane defaults.
func newRunningProcess(id int64, cmd *exec.Cmd, cmdName string, args []string) *runningProcess {
	return &runningProcess{
		id:        id,
		cmd:       cmd,
		cmdName:   cmdName,
		args:      args,
		startTime: time.Now(),
		doneCh:    make(chan struct{}),
		exitCode:  -1,
	}
}

// appendOutput appends newly captured stdout/stderr bytes to the combined buffer.
func (rp *runningProcess) appendOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	_, _ = rp.out.Write(data)
}

// outputString returns all combined output collected so far.
func (rp *runningProcess) outputString() string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.out.String()
}

// stateSnapshot atomically reads the mutable process state fields.
func (rp *runningProcess) stateSnapshot() (exitCode int, timedOut, canceled, finished bool) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.exitCode, rp.timedOut, rp.canceled, rp.finished
}

// isTimedOut reports whether the process was killed due to a timeout.
func (rp *runningProcess) isTimedOut() bool {
	_, timedOut, _, _ := rp.stateSnapshot()
	return timedOut
}

// markTimedOut records that the process was killed by a timeout.
func (rp *runningProcess) markTimedOut() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.timedOut = true
}

// markCanceled records that the process was explicitly killed by system_exec_kill.
func (rp *runningProcess) markCanceled() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.canceled = true
}

// setFinished records the exit code, marks the process finished, and closes
// doneCh so that any goroutine waiting on termination is released.
func (rp *runningProcess) setFinished(exitCode int) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.finished {
		return
	}
	rp.finished = true
	rp.exitCode = exitCode
	close(rp.doneCh)
}

// waitFinished blocks until the process has been reaped by its wait goroutine.
func (rp *runningProcess) waitFinished() {
	<-rp.doneCh
}

// processOutputWriter funnels combined stdout+stderr into a runningProcess.
type processOutputWriter struct {
	rp *runningProcess
}

// Write implements io.Writer.
func (w processOutputWriter) Write(p []byte) (int, error) {
	w.rp.appendOutput(p)
	return len(p), nil
}

// systemExecManager is a concurrency-safe in-process registry of background
// system processes, owned once per setupRunner call and captured by all tool
// closures.
type systemExecManager struct {
	mu     sync.Mutex
	procs  map[int64]*runningProcess
	nextID int64
}

// allocateID reserves a unique process ID without registering a process.
func (m *systemExecManager) allocateID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return m.nextID
}

// register stores a newly started background process in the registry and
// returns it with a freshly allocated ID.
func (m *systemExecManager) register(cmd *exec.Cmd, cmdName string, args []string) *runningProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	rp := newRunningProcess(m.nextID, cmd, cmdName, args)
	m.procs[rp.id] = rp
	return rp
}

// get looks up a registered background process by ID.
func (m *systemExecManager) get(id int64) (*runningProcess, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, ok := m.procs[id]
	return rp, ok
}

// remove deletes a process from the registry.
func (m *systemExecManager) remove(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.procs, id)
}

// snapshot returns a copy of all registered processes for listing.
func (m *systemExecManager) snapshot() []*runningProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	procs := make([]*runningProcess, 0, len(m.procs))
	for _, rp := range m.procs {
		procs = append(procs, rp)
	}
	return procs
}

// BuildExecCommand constructs an exec.Cmd for a system command. The spawned
// process always inherits the agent process's environment (cmd.Env =
// os.Environ()) and any input env map is merged on top (input overrides).
//
// P0-1 shell routing: when args is empty the whole command line is passed to
// "sh -c" so pipes, redirects, globs, &&/||, and compound commands work. When
// args is non-empty the explicit (command, args...) form is used so callers
// that pre-tokenize keep full control. This project is Linux-only (README);
// Windows has no "sh" - the sh path would need a runtime.GOOS guard if this
// code ever ports.
//
// Process hardening: every spawned process gets Setpgid:true (new process
// group) and Pdeathsig:SIGKILL (kernel reaps the child if the agent dies).
// Callers must wrap Start+Wait in runtime.LockOSThread/UnlockOSThread so the
// Pdeathsig thread stays alive for the child's lifetime (golang/go#27505).
//
// Sandbox integration: when CurrentSandbox is non-nil and not off, cmd.Dir is
// pinned to the workspace root (or verified against it when workingDir is
// set), and sensitive env entries (HAKASE_*, AWS_*, GITHUB_*, OPENAI_*) are
// scrubbed so they never leak into sandboxed subprocesses.
func BuildExecCommand(command string, args []string, workingDir string, env map[string]string) (*exec.Cmd, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command must not be empty")
	}

	// Harmful-command protection gate: policy decision + approval.
	// Runs BEFORE AuditSystemCommandPaths so denied commands never reach
	// path auditing. The audit entries record the decision at the gate
	// level (DurationMs=0, ExitCode=0) - the post-execution audit is in
	// the sync/start handlers.
	decision := EvaluateCommandFunc(CurrentSandbox, command, args)
	var sandboxMode string
	if CurrentSandbox != nil {
		sandboxMode = string(CurrentSandbox.Mode)
	} else {
		sandboxMode = "off"
	}
	cd := workingDir
	if cd == "" {
		cd, _ = os.Getwd()
	}
	switch decision.Action {
	case ActionDeny:
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SandboxMode: sandboxMode,
			Decision:    "denied",
			Risk:        decision.Risk.String(),
			Reason:      decision.Reason,
		})
		return nil, fmt.Errorf("command denied by protection policy: %s", decision.Reason)
	case ActionAsk:
		approved, aerr := ApproveFunc(interfaces.ApprovalRequest{
			Tool:      "system_exec",
			Command:   command,
			Args:      args,
			Risk:      decision.Risk.String(),
			Reason:    decision.Reason,
			Source:    "direct",
			ExpiresAt: time.Now().Add(ApprovalExpiryFunc()),
		})
		if aerr != nil || !approved {
			AuditCommandFunc(CommandAuditEntry{
				Timestamp:   time.Now(),
				Tool:        "system_exec",
				Command:     command,
				Args:        args,
				CWD:         cd,
				SandboxMode: sandboxMode,
				Decision:    "not_approved",
				Risk:        decision.Risk.String(),
				Reason:      decision.Reason,
			})
			if aerr != nil {
				return nil, fmt.Errorf("command approval failed: %w", aerr)
			}
			return nil, fmt.Errorf("command not approved by user: %s", command)
		}
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SandboxMode: sandboxMode,
			Decision:    "approved",
			Risk:        decision.Risk.String(),
			Reason:      decision.Reason,
		})
	case ActionAllow:
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SandboxMode: sandboxMode,
			Decision:    "allowed",
			Risk:        decision.Risk.String(),
			Reason:      decision.Reason,
		})
	}

	// Sandbox confinement: reject commands that reference paths outside the
	// sandbox's trusted folders (read roots + system dirs). Applies to both
	// the sync and background tools since both go through here. workingDir
	// threads through so relative operands resolve the way the process will.
	if err := AuditSystemCommandPaths(CurrentSandbox, command, args, workingDir); err != nil {
		return nil, err
	}

	// P0-1: route through sh -c when no args are provided so the model's
	// natural whole-command-line input (pipes, redirects, globs) works.
	// When args are provided, use the explicit executable+args form.
	ctx := context.Background()

	// Phase 2: when bubblewrap mode is active, wrap the inner command in
	// bwrap for kernel-enforced filesystem + network isolation. The inner
	// argv (sh -c or direct) becomes the command bwrap executes.
	if CurrentSandbox != nil && CurrentSandbox.Mode == SandboxModeBubblewrap {
		var innerArgv []string
		if len(args) == 0 {
			innerArgv = []string{"sh", "-c", command}
		} else {
			innerArgv = append([]string{command}, args...)
		}
		wd := workingDir
		if wd == "" {
			wd = CurrentSandbox.WorkspaceRoot()
		}
		bwCmd, err := wrapBwrapCmd(CurrentSandbox, innerArgv, wd, CurrentSandbox.AllowNetwork, nil)
		if err != nil {
			if CurrentSandbox.AllowFallback {
				// Explicitly configured to allow fallback: warn and
				// fall through to the plain exec path below.
				util.DebugWarn("sandbox_bwrap_fallback", "error", err)
			} else {
				return nil, fmt.Errorf("bubblewrap sandbox unavailable (bwrap not installed?) and sandbox.allow_fallback is false: %w", err)
			}
		} else {
			bwCmd.Env = os.Environ()
			for k, v := range env {
				bwCmd.Env = append(bwCmd.Env, k+"="+v)
			}
			bwCmd.Env = ScrubEnv(bwCmd.Env)
			if wd != "" {
				bwCmd.Dir = wd
			}
			bwCmd.SysProcAttr = &syscall.SysProcAttr{
				Setpgid:   true,
				Pdeathsig: syscall.SIGKILL,
			}
			return bwCmd, nil
		}
	}

	var cmd *exec.Cmd
	if len(args) == 0 {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	} else {
		cmd = exec.CommandContext(ctx, command, args...)
	}

	// Env merge: start from the agent process env, overlay caller overrides.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Always scrub sensitive env prefixes so secrets (HAKASE_*, AWS_*,
	// GITHUB_*, OPENAI_*) never leak into subprocesses, even in sandbox-off
	// mode (Phase 2.5).
	cmd.Env = ScrubEnv(cmd.Env)

	// Working directory: sandbox-aware resolution.
	if CurrentSandbox != nil && CurrentSandbox.Mode != SandboxModeOff {
		if workingDir == "" {
			if root := CurrentSandbox.WorkspaceRoot(); root != "" {
				cmd.Dir = root
			}
		} else {
			resolved, err := CurrentSandbox.ResolveScopedPath(workingDir, false)
			if err != nil {
				return nil, fmt.Errorf("working_dir %q rejected by sandbox: %w", workingDir, err)
			}
			cmd.Dir = resolved
		}
	} else if workingDir != "" {
		cmd.Dir = workingDir
	}

	// Process hardening: new process group + death signal so children
	// (and grandchildren) die if the agent crashes. Linux-only fields.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}

	return cmd, nil
}

// ScrubEnv returns env with entries whose key starts with any of the
// sensitive prefixes removed. Used when the sandbox is active so secret
// material does not leak into sandboxed subprocesses.
func ScrubEnv(env []string) []string {
	scrubbed := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "HAKASE_") ||
			strings.HasPrefix(key, "AWS_") ||
			strings.HasPrefix(key, "GITHUB_") ||
			strings.HasPrefix(key, "OPENAI_") {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	return scrubbed
}

// trustedExecDirs are host paths a system_exec command may reference without
// requiring an explicit read root. They mirror the read-only system bindings
// bubblewrap provides (sandboxexec.go: systemROBindDirs) plus the minimal
// virtual/scratch filesystems bwrap mounts (/proc, /dev, /tmp, /run) and
// /sys, which common diagnostic commands read. Everything else must live
// under a sandbox read root.
var trustedExecDirs = []string{
	"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc", "/nix",
	"/proc", "/dev", "/sys", "/tmp", "/run",
}

// AuditSystemCommandPaths is the confinement guard for system_exec. When the
// sandbox is active (mode != off), every absolute path token in the command
// line must resolve under a sandbox read root or a trusted system directory;
// deny roots are always rejected. Relative path-like operands are resolved
// against the same working directory the spawned process receives and are
// checked against deny roots and denied sensitive basenames, so omitting the
// leading "/" cannot reach a denied file (e.g. "cat services/api/.env").
//
// It is best-effort: the shell command line is tokenized, not parsed, so
// variable/command-substituted paths (e.g. "$HOME/secret") are not caught.
// Phase 2 (bubblewrap) provides the kernel-enforced guarantee; this guard is
// defense-in-depth and gives an immediate, actionable error in the common
// case.
func AuditSystemCommandPaths(sb *SandboxConfig, command string, args []string, workingDir string) error {
	if sb == nil || sb.Mode == SandboxModeOff {
		return nil
	}
	// Relative operands resolve against the same working directory the
	// executed process gets: an approved override when supplied, otherwise
	// the workspace root (BuildExecCommand pins cmd.Dir to exactly this).
	// The override is resolved through ResolveScopedPath so a raw relative
	// value like "sub" can never leave the audit comparing an absolute deny
	// root against a relative candidate path.
	base := ""
	if workingDir != "" {
		resolved, err := sb.ResolveScopedPath(workingDir, false)
		if err != nil {
			// BuildExecCommand rejects the same override right after; fail
			// closed here so the audit never runs against an unusable base.
			return fmt.Errorf("working_dir %q rejected by sandbox: %w", workingDir, err)
		}
		base = resolved
	} else if len(sb.WorkspaceRoots) > 0 {
		base = sb.WorkspaceRoots[0]
	}
	var tokens []string
	if len(args) == 0 {
		tokens = SplitCommandTokens(command)
	} else {
		tokens = append(tokens, command)
		tokens = append(tokens, args...)
	}
	for _, tok := range tokens {
		if err := auditPathToken(sb, tok, base); err != nil {
			util.DebugWarn("system_exec_path_rejected", "command", command, "args", args, "error", err.Error())
			return err
		}
	}
	return nil
}

// auditPathToken checks a single command-line token. Absolute paths must be
// under a read root or a trusted system dir, and never under a deny root.
// Relative path-like operands are resolved against relativeBase and audited
// against deny rules only; everything else passes, since bare words cannot
// leave the confined working directory. Glob metacharacters are pre-expanded
// because sh resolves them only after this audit runs.
func auditPathToken(sb *SandboxConfig, tok, relativeBase string) error {
	p := strings.TrimSpace(tok)
	if p == "" {
		return nil
	}
	expanded := expandHome(p)
	if !filepath.IsAbs(expanded) {
		if relativeBase == "" || !relativeOperandNeedsAudit(expanded) {
			return nil
		}
		resolved := filepath.Clean(filepath.Join(relativeBase, expanded))
		if err := auditDenyCandidates(sb, resolveWithSymlinks(resolved), tok); err != nil {
			return err
		}
		// A pattern like "*/key" or "secrets/*.env" expands in the shell
		// after this audit; check every concrete match now so metacharacters
		// cannot mask denied targets behind their literal form.
		if strings.ContainsAny(expanded, "*?[") {
			return auditGlobExpansions(sb, resolved, tok)
		}
		return nil
	}
	for _, d := range sb.DenyRoots {
		if within(d, expanded) {
			return fmt.Errorf("command references %q which is in a denied sandbox root", tok)
		}
	}
	if sb.deniedBasename(expanded) {
		return fmt.Errorf("command references %q which is a denied sensitive file", tok)
	}
	// Same expansion concern for absolute patterns: "/proj/*/key" passes the
	// literal checks but may expand into a denied root.
	if strings.ContainsAny(expanded, "*?[") {
		if err := auditGlobExpansions(sb, expanded, tok); err != nil {
			return err
		}
	}
	if pathInAny(expanded, sb.ReadRoots) || pathInAny(expanded, trustedExecDirs) {
		return nil
	}
	return fmt.Errorf("command references path %q outside the sandbox (not under a read root %v nor a trusted system dir); add it to sandbox.read_roots in config.json or narrow the command", tok, sb.ReadRoots)
}

// maxGlobMatches bounds how many shell-glob expansions are audited for a
// single operand so pathological patterns cannot stall the guard.
const maxGlobMatches = 1000

// auditGlobExpansions expands a shell-glob path pattern the way sh will
// (bounded) and applies the deny-root and denied-basename checks to every
// concrete match. No-match patterns pass through: the spawned shell handles
// them exactly as it would have without this audit.
func auditGlobExpansions(sb *SandboxConfig, pattern, tok string) error {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	if len(matches) > maxGlobMatches {
		matches = matches[:maxGlobMatches]
	}
	for _, m := range matches {
		if err := auditDenyCandidates(sb, resolveWithSymlinks(m), tok); err != nil {
			return fmt.Errorf("%w (glob expansion)", err)
		}
	}
	return nil
}

// auditDenyCandidates applies the deny-root and denied-basename checks to
// every candidate path (literal plus its symlink-resolved forms).
func auditDenyCandidates(sb *SandboxConfig, candidates []string, tok string) error {
	for _, candidate := range candidates {
		for _, d := range sb.DenyRoots {
			if within(d, candidate) {
				return fmt.Errorf("command references %q which resolves into a denied sandbox root", tok)
			}
		}
		if sb.deniedBasename(candidate) {
			return fmt.Errorf("command references %q which is a denied sensitive file", tok)
		}
	}
	return nil
}

// relativeOperandNeedsAudit reports whether a relative token deserves deny
// auditing. Flags (-l, --output) and URLs (https://...) are skipped; every
// other relative operand resolves against the effective working directory,
// so even bare words like "config.json" cannot smuggle a read of denied
// content by omitting the leading path.
func relativeOperandNeedsAudit(tok string) bool {
	return !strings.HasPrefix(tok, "-") && !strings.Contains(tok, "://")
}

// resolveWithSymlinks returns the cleaned path plus its symlink-resolved form
// when it exists on disk, so a workspace symlink pointing into a denied root
// is caught too.
func resolveWithSymlinks(path string) []string {
	out := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		out = append(out, resolved)
	}
	return out
}

// pathInAny reports whether path is contained under any of roots.
func pathInAny(path string, roots []string) bool {
	for _, r := range roots {
		if within(r, path) {
			return true
		}
	}
	return false
}

// SplitCommandTokens splits a shell command line into whitespace-delimited
// tokens, honoring single and double quotes so quoted paths (e.g.
// '~/My Documents') stay intact. It is deliberately simple - enough for the
// path audit, not a full shell parser.
func SplitCommandTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

// EffectiveExecTimeout returns the timeout to apply to a synchronous
// system_exec run. A caller-supplied timeout_seconds wins; an absent or
// non-positive value falls back to DefaultSystemExecTimeout so no command
// blocks the agent forever.
func EffectiveExecTimeout(seconds float64) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return DefaultSystemExecTimeout
}

// SystemExecInput is the input schema for the synchronous system_exec tool.
type SystemExecInput struct {
	Command        string            `json:"command"            doc:"Full command line (e.g. 'find /home -name \"*.pdf\" 2>/dev/null') when args is empty; executable name only when args is provided"`
	Args           []string          `json:"args,omitempty"     doc:"Optional list of arguments passed to the command"`
	WorkingDir     string            `json:"working_dir,omitempty" doc:"Optional working directory for the command; defaults to the agent process working directory"`
	Env            map[string]string `json:"env,omitempty"      doc:"Optional environment variables merged over the agent process environment; these override inherited values"`
	TimeoutSeconds float64           `json:"timeout_seconds,omitempty" doc:"Optional timeout in seconds; the command is killed if it exceeds this duration. Defaults to 120s when omitted (set 0 or negative to keep the default)."`
	MergeOutput    *bool             `json:"merge_output,omitempty" doc:"Combine stdout and stderr into a single output string (defaults to true when omitted)"`
}

// SystemExecOutput is the output schema for the synchronous system_exec tool.
type SystemExecOutput struct {
	ProcessID  int64  `json:"process_id"  doc:"The unique process ID assigned to this invocation"`
	ExitCode   int    `json:"exit_code"   doc:"The exit code of the command, or -1 if it was killed, timed out, or failed to start"`
	Stdout     string `json:"stdout"      doc:"Standard output captured from the command"`
	Stderr     string `json:"stderr"      doc:"Standard error captured from the command"`
	Output     string `json:"output"      doc:"Combined stdout and stderr output when merge_output is enabled"`
	TimedOut   bool   `json:"timed_out"   doc:"True if the command was killed because it exceeded the timeout"`
	DurationMs int64  `json:"duration_ms" doc:"Wall-clock duration of the command in milliseconds"`
}

// SystemExecStartInput is the input schema for the asynchronous system_exec_start tool.
type SystemExecStartInput struct {
	Command        string            `json:"command"        doc:"Full command line (e.g. 'find /home -name \"*.pdf\" 2>/dev/null') when args is empty; executable name only when args is provided"`
	Args           []string          `json:"args,omitempty" doc:"Optional list of arguments passed to the command"`
	WorkingDir     string            `json:"working_dir,omitempty" doc:"Optional working directory for the command; defaults to the agent process working directory"`
	Env            map[string]string `json:"env,omitempty"  doc:"Optional environment variables merged over the agent process environment; these override inherited values"`
	TimeoutSeconds float64           `json:"timeout_seconds,omitempty" doc:"Optional timeout in seconds; the background process group is killed if it exceeds this duration. Defaults to 0 (no timeout)."`
}

// SystemExecStartOutput is the output schema for the asynchronous system_exec_start tool.
type SystemExecStartOutput struct {
	ProcessID int64  `json:"process_id" doc:"The registered process ID used to poll, inspect, or kill the background process"`
	Started   bool   `json:"started"    doc:"True if the background process was successfully started"`
	Message   string `json:"message"    doc:"Human-readable status message describing the result"`
}

// SystemExecStatusInput is the input schema for the system_exec_status tool.
type SystemExecStatusInput struct {
	ProcessID int64 `json:"process_id" doc:"The process ID of the background process to inspect"`
}

// SystemExecStatusOutput is the output schema for the system_exec_status tool.
type SystemExecStatusOutput struct {
	ProcessID  int64  `json:"process_id"  doc:"The process ID that was inspected"`
	Running    bool   `json:"running"     doc:"True if the background process is still running"`
	Finished   bool   `json:"finished"    doc:"True if the background process has exited"`
	ExitCode   int    `json:"exit_code"   doc:"The exit code of the process, or -1 if it is still running or was killed"`
	Output     string `json:"output"      doc:"Combined stdout and stderr output collected so far"`
	DurationMs int64  `json:"duration_ms" doc:"Wall-clock duration since the process started, in milliseconds"`
}

// SystemExecKillInput is the input schema for the system_exec_kill tool.
type SystemExecKillInput struct {
	ProcessID int64 `json:"process_id" doc:"The process ID of the background process to terminate"`
}

// SystemExecKillOutput is the output schema for the system_exec_kill tool.
type SystemExecKillOutput struct {
	ProcessID int64  `json:"process_id" doc:"The process ID that was targeted"`
	Killed    bool   `json:"killed"     doc:"True if the process was terminated by this call"`
	Message   string `json:"message"    doc:"Human-readable status message describing the result"`
}

// SystemExecListInput is the input schema for the system_exec_list tool.
type SystemExecListInput struct{}

// SystemExecProcessInfo describes the live state of a registered background process.
type SystemExecProcessInfo struct {
	ProcessID  int64    `json:"process_id"  doc:"The registered process ID"`
	Command    string   `json:"command"     doc:"The command or executable that was started"`
	Args       []string `json:"args"        doc:"The arguments the command was started with"`
	Running    bool     `json:"running"     doc:"True if the process is still running"`
	ExitCode   int      `json:"exit_code"   doc:"The exit code of the process, or -1 if it is still running or was killed"`
	DurationMs int64    `json:"duration_ms" doc:"Wall-clock duration since the process started, in milliseconds"`
}

// SystemExecListOutput is the output schema for the system_exec_list tool.
type SystemExecListOutput struct {
	Processes []SystemExecProcessInfo `json:"processes" doc:"All registered background processes with their live state"`
}

// createSystemExecTools builds the five-tool host system execution toolset.
// All tools share a single systemExecManager instance so background processes
// started by system_exec_start can be polled, listed, and killed.
// The sessionManager and taskID parameters enable task-scoped isolation:
// terminal sessions are keyed by taskID and CWD is resolved per-task.
// ExecSessionProvider is a minimal interface for session-scoped CWD/FileOps
// resolution. The root SessionManager implements this; after SessionManager
// migrates to internal/session (task 5), that package will provide the real type.
type ExecSessionProvider interface {
	GetCWD(taskID string, fallback string) string
	GetFileOps(taskID string, fallback string) any
}

// CreateSystemExecTools builds the system_exec toolset (system_exec,
// system_exec_start, system_exec_ps, system_exec_kill) bound to the given
// session manager for per-task CWD resolution. A nil sessionManager falls
// back to the agent process working directory.
func CreateSystemExecTools(log interfaces.LogFunc, sessionManager ExecSessionProvider, taskID string) ([]tool.Tool, error) {
	m := &systemExecManager{
		procs: make(map[int64]*runningProcess),
	}

	// Resolve per-task CWD from session manager if available,
	// otherwise fall back to the agent process working directory.
	taskCWD := ""
	if sessionManager != nil && taskID != "" {
		taskCWD = sessionManager.GetCWD(taskID, "")
	}
	if taskCWD == "" {
		taskCWD, _ = os.Getwd()
	}

	// system_exec: synchronous fire-and-wait execution.
	execTool, err := util.NewDocTool(functiontool.Config{
		Name:        "system_exec",
		Description: "Runs a system command or executable directly on the host machine synchronously and waits for it to finish or time out (default timeout 120s; pass timeout_seconds to override, or use system_exec_start for long-running work). Commands are checked against a harmful-command policy and may require approval. When the sandbox is active, commands that reference absolute paths outside the sandbox read roots or trusted system dirs are rejected. Not routed through the Python interpreter.",
	}, func(ctx agent.Context, input SystemExecInput) (SystemExecOutput, error) {
		start := time.Now()
		procID := m.allocateID()
		workingDir := input.WorkingDir
		if workingDir == "" {
			workingDir = taskCWD
		}
		cmd, err := BuildExecCommand(input.Command, input.Args, workingDir, input.Env)
		if err != nil {
			return SystemExecOutput{ProcessID: procID}, err
		}

		rp := newRunningProcess(procID, cmd, input.Command, input.Args)
		outWriter := processOutputWriter{rp: rp}
		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = io.MultiWriter(&stdoutBuf, outWriter)
		cmd.Stderr = io.MultiWriter(&stderrBuf, outWriter)

		if log != nil {
			log(fmt.Sprintf("⚡ [system_exec] Running: %s", strings.Join(cmd.Args, " ")))
		}

		// Pdeathsig fires on the OS thread that called Start; lock it
		// so the runtime does not recycle it before Wait reaps the child
		// (golang/go#27505).
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := cmd.Start(); err != nil {
			util.DebugError("system_exec_start_failed", "process_id", procID, "command", input.Command, "error", err.Error())
			return SystemExecOutput{
				ProcessID:  procID,
				ExitCode:   -1,
				TimedOut:   false,
				DurationMs: time.Since(start).Milliseconds(),
			}, fmt.Errorf("failed to start command %q: %w", input.Command, err)
		}

		var timeoutTimer *time.Timer
		if timeout := EffectiveExecTimeout(input.TimeoutSeconds); timeout > 0 {
			timeoutTimer = time.AfterFunc(
				timeout,
				func() {
					rp.markTimedOut()
					// Group-kill so grandchildren die too (Setpgid).
					if cmd.Process != nil {
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
				},
			)
			defer timeoutTimer.Stop()
		}

		runErr := cmd.Wait()
		rp.setFinished(-1)

		out := SystemExecOutput{
			ProcessID:  procID,
			ExitCode:   -1,
			TimedOut:   rp.isTimedOut(),
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String(),
		}

		if out.TimedOut {
			util.DebugWarn("system_exec_timeout", "process_id", procID, "command", input.Command, "duration_ms", out.DurationMs)
		}

		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
			out.ExitCode = 0
		case errors.As(runErr, &exitErr):
			out.ExitCode = exitErr.ExitCode()
		case !out.TimedOut:
			return out, fmt.Errorf("failed to run command %q: %w", input.Command, runErr)
		}

		mergeOutput := true
		if input.MergeOutput != nil {
			mergeOutput = *input.MergeOutput
		}
		if mergeOutput {
			out.Output = rp.outputString()
		}

		if log != nil {
			log(fmt.Sprintf("✅ [system_exec] Process #%d finished (exit %d, %d ms)", procID, out.ExitCode, out.DurationMs))
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_start: detached background execution registered in the registry.
	startTool, err := util.NewDocTool(functiontool.Config{
		Name:        "system_exec_start",
		Description: "Starts a system command or executable on the host machine in the background, registers it in the process registry, and returns immediately with a process ID for later polling with system_exec_status, killing with system_exec_kill, or listing with system_exec_list. Commands are checked against a harmful-command policy and may require approval.",
	}, func(ctx agent.Context, input SystemExecStartInput) (SystemExecStartOutput, error) {
		workingDir := input.WorkingDir
		if workingDir == "" {
			workingDir = taskCWD
		}
		cmd, err := BuildExecCommand(input.Command, input.Args, workingDir, input.Env)
		if err != nil {
			return SystemExecStartOutput{Started: false, Message: err.Error()}, err
		}

		rp := m.register(cmd, input.Command, input.Args)
		outWriter := processOutputWriter{rp: rp}
		cmd.Stdout = outWriter
		cmd.Stderr = outWriter

		if log != nil {
			log(fmt.Sprintf("🚀 [system_exec] Starting background process #%d: %s", rp.id, strings.Join(cmd.Args, " ")))
		}

		// Start+Wait must run on the same locked OS thread so Pdeathsig
		// does not fire prematurely (golang/go#27505). We launch a
		// goroutine that owns the thread for the child's lifetime; the
		// handler blocks only until Start reports success/failure.
		type startResult struct{ err error }
		startCh := make(chan startResult, 1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			if err := cmd.Start(); err != nil {
				startCh <- startResult{err: err}
				return
			}
			startCh <- startResult{err: nil}

			// Optional timeout: kill the process group after
			// TimeoutSeconds (same group-kill pattern as the sync
			// handler). Default 0 = no timeout.
			var timeoutTimer *time.Timer
			if input.TimeoutSeconds > 0 {
				timeoutTimer = time.AfterFunc(
					time.Duration(input.TimeoutSeconds*float64(time.Second)),
					func() {
						rp.markTimedOut()
						if cmd.Process != nil {
							_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						}
					},
				)
				defer timeoutTimer.Stop()
			}

			exitCode := 0
			if err := cmd.Wait(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}
			rp.setFinished(exitCode)
			if log != nil {
				log(fmt.Sprintf("✅ [system_exec] Background process #%d exited with code %d", rp.id, exitCode))
			}
		}()

		res := <-startCh
		if res.err != nil {
			m.remove(rp.id)
			rp.setFinished(-1)
			util.DebugError("system_exec_start_failed", "process_id", rp.id, "command", input.Command, "error", res.err.Error())
			return SystemExecStartOutput{
				ProcessID: rp.id,
				Started:   false,
				Message:   fmt.Sprintf("failed to start command: %v", res.err),
			}, res.err
		}

		return SystemExecStartOutput{
			ProcessID: rp.id,
			Started:   true,
			Message:   fmt.Sprintf("started background process #%d: %s", rp.id, input.Command),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_status: poll the live state of a background process.
	statusTool, err := util.NewDocTool(functiontool.Config{
		Name:        "system_exec_status",
		Description: "Returns the current live state (running, exit code, combined output collected so far, duration) of a background process previously started with system_exec_start.",
	}, func(ctx agent.Context, input SystemExecStatusInput) (SystemExecStatusOutput, error) {
		rp, ok := m.get(input.ProcessID)
		if !ok {
			return SystemExecStatusOutput{ProcessID: input.ProcessID}, fmt.Errorf("no such process: %d", input.ProcessID)
		}
		exitCode, _, _, finished := rp.stateSnapshot()
		return SystemExecStatusOutput{
			ProcessID:  rp.id,
			Running:    !finished,
			Finished:   finished,
			ExitCode:   exitCode,
			Output:     rp.outputString(),
			DurationMs: time.Since(rp.startTime).Milliseconds(),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_kill: terminate and reap a background process.
	killTool, err := util.NewDocTool(functiontool.Config{
		Name:        "system_exec_kill",
		Description: "Terminates a background process previously started with system_exec_start (SIGKILL), reaps it, and removes it from the process registry.",
	}, func(ctx agent.Context, input SystemExecKillInput) (SystemExecKillOutput, error) {
		rp, ok := m.get(input.ProcessID)
		if !ok {
			return SystemExecKillOutput{ProcessID: input.ProcessID, Killed: false, Message: "no such process"}, fmt.Errorf("no such process: %d", input.ProcessID)
		}

		_, _, _, alreadyFinished := rp.stateSnapshot()
		rp.markCanceled()
		// Group-kill (negative pid) so grandchildren die too (Setpgid).
		if !alreadyFinished && rp.cmd.Process != nil {
			_ = syscall.Kill(-rp.cmd.Process.Pid, syscall.SIGKILL)
		}
		rp.waitFinished()
		m.remove(input.ProcessID)

		if log != nil {
			log(fmt.Sprintf("🛑 [system_exec] Killed background process #%d", input.ProcessID))
		}
		if alreadyFinished {
			return SystemExecKillOutput{
				ProcessID: input.ProcessID,
				Killed:    false,
				Message:   "process had already finished",
			}, nil
		}
		return SystemExecKillOutput{
			ProcessID: input.ProcessID,
			Killed:    true,
			Message:   "process terminated",
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_list: list all registered background processes.
	listTool, err := util.NewDocTool(functiontool.Config{
		Name:        "system_exec_list",
		Description: "Lists all registered background system processes started by system_exec_start and their live state (running, exit code, duration).",
	}, func(ctx agent.Context, _ SystemExecListInput) (SystemExecListOutput, error) {
		procs := m.snapshot()
		infos := make([]SystemExecProcessInfo, 0, len(procs))
		for _, rp := range procs {
			exitCode, _, _, finished := rp.stateSnapshot()
			infos = append(infos, SystemExecProcessInfo{
				ProcessID:  rp.id,
				Command:    rp.cmdName,
				Args:       rp.args,
				Running:    !finished,
				ExitCode:   exitCode,
				DurationMs: time.Since(rp.startTime).Milliseconds(),
			})
		}
		return SystemExecListOutput{Processes: infos}, nil
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{execTool, startTool, statusTool, killTool, listTool}, nil
}
