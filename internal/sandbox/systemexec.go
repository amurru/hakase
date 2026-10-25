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
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
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
// execOptions carries the optional build behaviors for BuildExecCommand.
type execOptions struct {
	// operatorAuthorized records that the human operator issued the command
	// directly (e.g. the `hakase projects` registry CLI), so the interactive
	// approval gate is bypassed - there is no agent/UI to answer it, and the
	// human typing the command IS the authorization. Hard denies and sandbox
	// confinement still apply.
	operatorAuthorized bool
	// preApproved records that a narrower gate already collected human
	// approval for this exact call (S18 pip: allowlist + name-shape +
	// approval), so the shared choke must not prompt a second time. Only
	// the allowlist leg and the known-risk threshold ask are satisfied by
	// it: the approval.mode=deny check, hard denies, deny patterns,
	// permissions-policy and flow verdicts, unknown-risk asks, and the
	// path audit all still apply (deny-wins, unknown-sink fail-closed).
	// Set only by choke builders reached after their own approval gate.
	preApproved bool
	// preApproveReason names the narrower gate behind preApproved for the
	// audit trail (never raw args).
	preApproveReason string
	// pipInstall marks a pip package-index install (S22): the bwrap
	// profile keeps the workspace read-only except .venv/ writable, with
	// network explicitly OPEN for index egress. The compensating controls
	// are the S18 gates (name shape + allowlist + human approval), which
	// run before this build - not the net jail.
	pipInstall bool
	// replaceEnv, when useReplaceEnv is set (ExecReplaceEnv), replaces
	// the ambient process environment instead of merging over it: the
	// child env is built from replaceEnv plus the per-call env overlay
	// only, then scrubbed. Pip uses this with the scrubbed S20 child
	// env (pipEnvMap) so no ambient PYTHONPATH-family key survives.
	replaceEnv    map[string]string
	useReplaceEnv bool
}

// ExecOption customizes how BuildExecCommand constructs a command.
type ExecOption func(*execOptions)

// ExecOperatorAuthorized marks the command as issued directly by the operator
// rather than by an agent. Approvals that would normally be asked are recorded
// as operator-authorized instead of consulting ApproveFunc.
func ExecOperatorAuthorized() ExecOption {
	return func(o *execOptions) { o.operatorAuthorized = true }
}

// ExecPipInstall marks the command as a pip package-index install (S22):
// the bwrap profile keeps the workspace read-only except .venv/ writable,
// with network explicitly OPEN for index egress (the S18 allowlist +
// approval + name-shape gates above are the compensating controls, not
// the net jail). Documented as an explicit exception, never silent.
func ExecPipInstall() ExecOption {
	return func(o *execOptions) { o.pipInstall = true }
}

// ExecReplaceEnv replaces the ambient process environment for the child:
// instead of merging the per-call env over os.Environ, the child env is
// built from replaceEnv plus the per-call env overlay only, then scrubbed
// (ScrubEnv, including the S20 PYTHONPATH-family keys). Honored by both
// the bwrap and plain build branches.
func ExecReplaceEnv(env map[string]string) ExecOption {
	return func(o *execOptions) { o.replaceEnv = env; o.useReplaceEnv = true }
}

// ExecPreApproved marks the build as already human-approved by a narrower
// gate (S18 pip: name-shape + allowlist + approval). The shared choke
// consumes it instead of prompting again, but only for the allowlist leg
// and a known-risk threshold ask: hard denies, deny patterns,
// permissions-policy and flow verdicts, unknown-risk asks, and the path
// audit still apply. The reason names the gate for the audit trail.
func ExecPreApproved(reason string) ExecOption {
	return func(o *execOptions) { o.preApproved = true; o.preApproveReason = reason }
}

// buildEnvList renders the child env for one build branch: ambient plus
// per-call overlay by default, or replaceEnv plus overlay when
// ExecReplaceEnv is set. Keys are deduplicated with the overlay winning:
// a replace/ambient entry shadowed by an overlay key is dropped so no key
// appears twice (exec passes duplicates through and the child reads the
// first or last depending on libc). ScrubEnv always runs after the merge
// so secrets and PYTHONPATH-family keys never survive regardless of source.
func buildEnvList(env map[string]string, bo *execOptions) []string {
	shadowed := make(map[string]struct{}, len(env))
	for k := range env {
		shadowed[k] = struct{}{}
	}
	var out []string
	if !bo.useReplaceEnv {
		for _, kv := range os.Environ() {
			if k, _, ok := strings.Cut(kv, "="); ok {
				if _, dup := shadowed[k]; dup {
					continue
				}
			}
			out = append(out, kv)
		}
	} else {
		for k, v := range bo.replaceEnv {
			if _, dup := shadowed[k]; dup {
				continue
			}
			out = append(out, k+"="+v)
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return ScrubEnv(out)
}

// BuildExecCommand builds the hardened exec.Cmd for a system command: harmful-
// command policy evaluation (+ interactive approval), path confinement,
// env scrubbing, and bubblewrap wrapping. `env` overlays the process env;
// `opts` customize the build (see ExecOperatorAuthorized).
func BuildExecCommand(command string, args []string, workingDir string, env map[string]string, opts ...ExecOption) (*exec.Cmd, error) {
	return buildExecCommand(context.Background(), CurrentSandbox, "", command, args, workingDir, env, opts...)
}

// BuildExecCommandFor is BuildExecCommand, except the effective sandbox comes
// from ctx (sandbox.ConfigFrom: a context-scoped override for project-bound
// runs, else the process CurrentSandbox). The exec tool handlers and the git
// engine call this so a per-session pinned sandbox actually constrains the
// subprocess. When ctx is an ADK invocation context, the hakase session it
// serves is resolved and attached to any approval prompt raised while
// building the command (gate prompt routing).
func BuildExecCommandFor(ctx context.Context, command string, args []string, workingDir string, env map[string]string, opts ...ExecOption) (*exec.Cmd, error) {
	return buildExecCommand(ctx, ConfigFrom(ctx), interfaces.SessionIDFromCtx(ctx), command, args, workingDir, env, opts...)
}

// buildExecCommand applies the policy/sandbox pipeline to one command using
// the supplied sandbox configuration instead of reading CurrentSandbox.
// sessionID (possibly empty) is attached to approval prompts.
func buildExecCommand(ctx context.Context, sb *SandboxConfig, sessionID string, command string, args []string, workingDir string, env map[string]string, opts ...ExecOption) (*exec.Cmd, error) {
	bo := &execOptions{}
	for _, o := range opts {
		o(bo)
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command must not be empty")
	}

	// Issue #14: landlock is unimplemented on every platform - refuse loudly
	// instead of silently degrading to path-auditing-only exec. This check
	// runs before everything (including the Windows coercion below) so a
	// landlock config fails closed no matter how it was constructed
	// (config file, direct struct, pinned override).
	if sb != nil && sb.Mode == SandboxModeLandlock {
		return nil, ErrLandlockNotImplemented
	}

	// H1: approval.mode=deny denies everything, including commands the
	// risk gate would allow (ApproveExec only sees the ask path).
	if ApprovalDenyAllFunc != nil && ApprovalDenyAllFunc() {
		mode := "off"
		if sb != nil {
			mode = string(sb.Mode)
		}
		if AuditCommandFunc != nil {
			AuditCommandFunc(CommandAuditEntry{
				Timestamp: time.Now(), Tool: "system_exec",
				Command: command, Args: args, SessionID: sessionID,
				SandboxMode: mode, Decision: "denied",
				Risk: "unknown", Reason: "denied by approval.mode=deny",
			})
		}
		return nil, fmt.Errorf("command denied by approval.mode=deny")
	}

	// WIN-005 defensive mode coercion: bubblewrap does not exist on Windows
	// and tests construct sb directly (bypassing LoadSandboxConfig), so the
	// coercion also happens here. Mutating the mode in place is idempotent
	// and makes every audit entry below record the effective mode (paths).
	if sb != nil && runtime.GOOS == "windows" && sb.Mode == SandboxModeBubblewrap {
		util.DebugWarn("sandbox_mode_coerced",
			"from", string(sb.Mode),
			"to", string(SandboxModePaths),
			"reason", "bubblewrap is unsupported on windows")
		sb.Mode = SandboxModePaths
	}

	// Harmful-command protection gate: policy decision + approval.
	// Runs BEFORE AuditSystemCommandPaths so denied commands never reach
	// path auditing. The audit entries record the decision at the gate
	// level (DurationMs=0, ExitCode=0) - the post-execution audit is in
	// the sync/start handlers. When the flow-aware evaluator is wired,
	// the label resolves from ctx (FG-003); otherwise the legacy path.
	// S9/S14: the resolved label (possibly nil = fail-closed under v2)
	// flows into both the evaluator and the ask binding below, so an
	// unregistered session can never pass as widest.
	var decision GateDecision
	flowLabel := flowLabelFromCtx(ctx)
	// Pre-approved narrow-gate builds (S22 pip via ExecPreApproved)
	// evaluate the policy gate with the command allowlist cleared: the
	// binary is fixed by the choke (not model-chosen) and the S18
	// allowlist + name-shape + approval above are the compensating
	// controls, so the example allowed_commands list (ls, cat, grep,
	// head, tail, echo, git) must not deny it. Every other leg -
	// approval.mode=deny above, hard denies, deny patterns,
	// permissions-policy and flow verdicts, risk - evaluates against
	// the real config via gateSB, and the path audit below uses the
	// real sb too.
	gateSB := sb
	if bo.preApproved && sb != nil && len(sb.AllowedCommands) > 0 {
		cp := *sb
		cp.AllowedCommands = nil
		gateSB = &cp
	}
	if EvaluateCommandWithLabelFunc != nil {
		decision = EvaluateCommandWithLabelFunc(gateSB, command, args, flowLabel)
	} else {
		decision = EvaluateCommandFunc(gateSB, command, args)
	}
	var sandboxMode string
	if sb != nil {
		sandboxMode = string(sb.Mode)
	} else {
		sandboxMode = "off"
	}
	cd := workingDir
	if cd == "" {
		cd, _ = os.Getwd()
	}
	// S9/H1 local decision: the Ask branch below sets gateApproved when
	// the human approved (consumed pre-dispatch grant or in-stack
	// approval); the path audit consumes it instead of hard-denying.
	gateApproved := false
	switch decision.Action {
	case ActionDeny:
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SessionID:   sessionID,
			SandboxMode: sandboxMode,
			Decision:    "denied",
			PolicyRule:  decision.PolicyRule.Citation(),
			Risk:        decision.Risk.String(),
			Reason:      decision.Reason,
		})
		return nil, fmt.Errorf("command denied by protection policy: %s", decision.Reason)
	case ActionAsk:
		if bo.operatorAuthorized {
			AuditCommandFunc(CommandAuditEntry{
				Timestamp:   time.Now(),
				Tool:        "system_exec",
				Command:     command,
				Args:        args,
				CWD:         cd,
				SessionID:   sessionID,
				SandboxMode: sandboxMode,
				Decision:    "operator_approved",
				PolicyRule:  decision.PolicyRule.Citation(),
				Risk:        decision.Risk.String(),
				Reason:      decision.Reason,
			})
			gateApproved = true
			break
		}
		// S9/H1: a flow Ask already approved at the ADK pre-dispatch
		// carries its one-shot grant - consume it and skip a second
		// prompt (the approval covers exactly this call).
		flowApproved := false
		{
			var lv uint64
			if flowLabel != nil {
				lv = flowLabel.Version
			}
			if tryConsumeShellApproval(sessionID, command, args, lv) {
				flowApproved = true
				gateApproved = true
				AuditCommandFunc(CommandAuditEntry{
					Timestamp:   time.Now(),
					Tool:        "system_exec",
					Command:     command,
					Args:        args,
					CWD:         cd,
					SessionID:   sessionID,
					SandboxMode: sandboxMode,
					Decision:    "approved",
					PolicyRule:  decision.PolicyRule.Citation(),
					Risk:        decision.Risk.String(),
					Reason:      "flow grant consumed: " + decision.Reason,
				})
			}
		}
		if !flowApproved && bo.preApproved && decision.Risk != RiskUnknown &&
			decision.PolicyRule == nil && !strings.Contains(decision.Reason, "by permissions policy") {
			// S22: a narrower gate already collected human approval for
			// this exact call (S18 pip: name-shape + allowlist +
			// approval), so satisfy a known-risk threshold ask with the
			// carried approval instead of prompting twice. Unknown-risk
			// asks still prompt (unknown-sink fail-closed), and
			// permissions-policy / flow asks fall through to the prompt
			// below - a pip approval never overrides those verdicts.
			flowApproved = true
			gateApproved = true
			AuditCommandFunc(CommandAuditEntry{
				Timestamp:   time.Now(),
				Tool:        "system_exec",
				Command:     command,
				Args:        args,
				CWD:         cd,
				SessionID:   sessionID,
				SandboxMode: sandboxMode,
				Decision:    "approved",
				PolicyRule:  decision.PolicyRule.Citation(),
				Risk:        decision.Risk.String(),
				Reason:      "pre-approved narrow gate (" + bo.preApproveReason + "): " + decision.Reason,
			})
		}
		if !flowApproved {
			// S14: bind a flow-caused ask to its exact call (shared pure
			// helper, no API change); base asks keep empty Flow fields.
			flowReason, flowHash, flowVersion := flowShellAskBinding(flowLabel, command, args, decision)
			approved, aerr := ApproveFunc(ctx, interfaces.ApprovalRequest{
				Tool:      "system_exec",
				Command:   command,
				Args:      args,
				Risk:      decision.Risk.String(),
				Reason:    decision.Reason,
				Source:    "direct",
				ExpiresAt: time.Now().Add(ApprovalExpiryFunc()),
				SessionID: sessionID,
				// S14: a flow-caused ask carries its binding (shared pure
				// helper, no API change) so approval.mode=allow never
				// auto-approves it; base asks keep empty Flow fields (v1).
				FlowReason:       flowReason,
				FlowArgsHash:     flowHash,
				FlowLabelVersion: flowVersion,
			})
			if aerr != nil || !approved {
				AuditCommandFunc(CommandAuditEntry{
					Timestamp:   time.Now(),
					Tool:        "system_exec",
					Command:     command,
					Args:        args,
					CWD:         cd,
					SessionID:   sessionID,
					SandboxMode: sandboxMode,
					Decision:    "not_approved",
					PolicyRule:  decision.PolicyRule.Citation(),
					Risk:        decision.Risk.String(),
					Reason:      decision.Reason,
				})
				if aerr != nil {
					return nil, fmt.Errorf("command approval failed: %w", aerr)
				}
				return nil, fmt.Errorf("command not approved by user: %s", command)
			}
			// In-stack approval is the audit's local decision (S9/H1):
			// the audit below admits on gateApproved without a second
			// prompt or a lingering grant.
			flowApproved = true
			gateApproved = true
			AuditCommandFunc(CommandAuditEntry{
				Timestamp:   time.Now(),
				Tool:        "system_exec",
				Command:     command,
				Args:        args,
				CWD:         cd,
				SessionID:   sessionID,
				SandboxMode: sandboxMode,
				Decision:    "approved",
				PolicyRule:  decision.PolicyRule.Citation(),
				Risk:        decision.Risk.String(),
				Reason:      decision.Reason,
			})
		}
	case ActionAllow:
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SessionID:   sessionID,
			SandboxMode: sandboxMode,
			Decision:    "allowed",
			PolicyRule:  decision.PolicyRule.Citation(),
			Risk:        decision.Risk.String(),
			Reason:      decision.Reason,
		})
	}

	// Sandbox confinement: reject commands that reference paths outside the
	// sandbox's trusted folders (read roots + system dirs). Applies to both
	// the sync and background tools since both go through here. workingDir
	// threads through so relative operands resolve the way the process will.
	// S9/H1: the gate's local decision rides along - an approved Ask
	// admits here instead of hard-denying.
	if err := auditSystemCommandPaths(sb, command, args, workingDir, flowLabel, gateApproved); err != nil {
		return nil, err
	}

	// P0-1: route through sh -c when no args are provided so the model's
	// natural whole-command-line input (pipes, redirects, globs) works.
	// When args are provided, use the explicit executable+args form.
	// Named cmdCtx (not ctx) to avoid colliding with the run-context
	// parameter: command construction stays on Background as before; only
	// the approval gate above observes run cancellation.
	cmdCtx := context.Background()

	// Phase 2: when bubblewrap mode is active, wrap the inner command in
	// bwrap for kernel-enforced filesystem + network isolation. The inner
	// argv (sh -c or direct) becomes the command bwrap executes.
	if sb != nil && sb.Mode == SandboxModeBubblewrap {
		var innerArgv []string
		if len(args) == 0 {
			innerArgv = []string{"sh", "-c", command}
		} else {
			innerArgv = append([]string{command}, args...)
		}
		wd := workingDir
		if wd == "" {
			wd = sb.WorkspaceRoot()
		}
		// S22: pip installs keep the workspace read-only except .venv/
		// writable (pipProfile), with network explicitly OPEN for index
		// egress (sb.AllowNetwork OR the pip exception).
		var extraBinds []string
		if bo.pipInstall {
			venvDir := filepath.Join(effectiveChildDir(sb, workingDir), ".venv")
			extraBinds = []string{venvDir}
		}
		bwCmd, err := wrapBwrapCmd(sb, innerArgv, wd, sb.AllowNetwork || bo.pipInstall, extraBinds, bo.pipInstall)
		if err != nil {
			if sb.AllowFallback {
				// Explicitly configured to allow fallback: fail LOUD and fall
				// through to the plain exec path below. Issue #14: a
				// sandboxed-to-unsandboxed transition must never be
				// debug-only. The audit entry is the normative record
				// (always on, unlike DebugWarn which needs debug mode); the
				// UI hook makes it visible in the web UI / TUI for this run.
				fallbackCWD := wd
				if fallbackCWD == "" {
					fallbackCWD, _ = os.Getwd()
				}
				reason := fmt.Sprintf("bubblewrap unavailable (%v); running with path-confinement only (no kernel isolation)", err)
				if AuditCommandFunc != nil {
					AuditCommandFunc(CommandAuditEntry{
						Timestamp:   time.Now(),
						Tool:        "system_exec",
						Command:     command,
						Args:        args,
						CWD:         fallbackCWD,
						SessionID:   sessionID,
						SandboxMode: string(sb.Mode),
						Decision:    "sandbox_fallback",
						Risk:        decision.Risk.String(),
						Reason:      reason,
					})
				}
				util.DebugWarn("sandbox_bwrap_fallback", "error", err.Error(), "session_id", sessionID)
				notifySandboxFallback(sessionID,
					fmt.Sprintf("Sandbox fallback: bwrap unavailable, command %q runs with path-confinement only (no kernel isolation)", command))
			} else {
				return nil, fmt.Errorf("bubblewrap sandbox unavailable (bwrap not installed?) and sandbox.allow_fallback is false: %w", err)
			}
		} else {
			bwCmd.Env = buildEnvList(env, bo)
			if wd != "" {
				bwCmd.Dir = wd
			}
			configureProcess(bwCmd)
			return bwCmd, nil
		}
	}

	var cmd *exec.Cmd
	if len(args) == 0 {
		// P0-1: route through the platform shell when no args are
		// provided (sh -c on Unix, cmd /D /C on Windows) so the model's
		// natural whole-command-line input (pipes, redirects) works.
		shellCmd, err := buildShellCommand(cmdCtx, command, effectiveChildDir(sb, workingDir))
		if err != nil {
			return nil, err
		}
		cmd = shellCmd
	} else {
		// Explicit executable+args form; on Windows bare names are
		// resolved against PATH only (never the working directory) and
		// rewritten to absolute paths before exec.
		directCmd, err := buildDirectCommand(cmdCtx, command, args, effectiveChildDir(sb, workingDir))
		if err != nil {
			return nil, err
		}
		cmd = directCmd
	}

	// Env: ambient merged with caller overrides by default, or the
	// ExecReplaceEnv set plus overrides when set (S20 scrubbed pip env).
	// Scrub runs after the merge in buildEnvList.
	cmd.Env = buildEnvList(env, bo)
	// Platform hardening (Windows: NoDefaultCurrentDirectoryInExePath=1).
	cmd.Env = hardenChildEnv(cmd.Env)

	// Working directory: sandbox-aware resolution.
	if sb != nil && sb.Mode != SandboxModeOff {
		if workingDir == "" {
			if root := sb.WorkspaceRoot(); root != "" {
				cmd.Dir = root
			}
		} else {
			resolved, err := sb.ResolveScopedPath(workingDir, false)
			if err != nil {
				return nil, fmt.Errorf("working_dir %q rejected by sandbox: %w", workingDir, err)
			}
			cmd.Dir = resolved
		}
	} else if workingDir != "" {
		cmd.Dir = workingDir
	}

	// Process hardening: new process group + death signal so children
	// (and grandchildren) die if the agent crashes (Linux); Job Object
	// tracking on Windows (assigned after Start).
	configureProcess(cmd)

	return cmd, nil
}

// ScrubEnv returns env with entries whose key starts with any of the
// sensitive prefixes removed. Used when the sandbox is active so secret
// material does not leak into sandboxed subprocesses. On Windows the
// prefix match is case-insensitive (Windows environment names are
// case-insensitive, so "Aws_secret_access_key" is still readable by
// children and must not survive the scrub); the Linux match stays
// byte-exact.
//
// S20: the exact PYTHONPATH-family keys (PYTHONPATH, PYTHONHOME,
// PYTHONSTARTUP, __PYVENV_LAUNCHER__) are always removed too (absent,
// not empty), case-insensitively on Windows. The scrub runs after the
// env merge in buildEnvList so no ambient or overlay value survives.
func ScrubEnv(env []string) []string {
	prefixes := [...]string{"HAKASE_", "AWS_", "GITHUB_", "OPENAI_"}
	caseInsensitive := runtime.GOOS == "windows"
	scrubbed := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if envKeyIsSensitive(key, prefixes[:], caseInsensitive) {
			continue
		}
		if isPythonScrubbedKey(key, caseInsensitive) {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	return scrubbed
}

// pythonScrubbedKeys is the exact S20 scrub list: these keys are removed
// from the child env always (absent, not empty).
var pythonScrubbedKeys = []string{
	"PYTHONPATH",
	"PYTHONHOME",
	"PYTHONSTARTUP",
	"__PYVENV_LAUNCHER__",
}

// isPythonScrubbedKey reports whether key is an S20-scrubbed key.
// Windows matches case-insensitively; Linux matches byte-exact.
func isPythonScrubbedKey(key string, caseInsensitive bool) bool {
	for _, k := range pythonScrubbedKeys {
		if caseInsensitive {
			if strings.EqualFold(key, k) {
				return true
			}
			continue
		}
		if key == k {
			return true
		}
	}
	return false
}

// envKeyIsSensitive reports whether key starts with any sensitive prefix.
func envKeyIsSensitive(key string, prefixes []string, caseInsensitive bool) bool {
	for _, p := range prefixes {
		if caseInsensitive {
			if len(key) >= len(p) && strings.EqualFold(key[:len(p)], p) {
				return true
			}
			continue
		}
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// effectiveChildDir returns the directory the spawned process will actually
// run in: the caller-supplied workingDir when set, the effective sandbox's
// workspace root when the sandbox pins it, or the agent process working
// directory. It derives the root from the sb argument (the context-scoped
// override for pinned project-bound runs), never from CurrentSandbox, so a
// per-run sandbox override is honored here exactly like everywhere else.
func effectiveChildDir(sb *SandboxConfig, workingDir string) string {
	if workingDir != "" {
		return workingDir
	}
	if sb != nil && sb.Mode != SandboxModeOff {
		if root := sb.WorkspaceRoot(); root != "" {
			return root
		}
	}
	wd, _ := os.Getwd()
	return wd
}

// trustedExecDirs is declared per-OS in pathdirs_unix.go / pathdirs_windows.go.

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
	l := permissions.StartLabel()
	return AuditSystemCommandPathsWithLabel(sb, command, args, workingDir, &l)
}

// AuditSystemCommandPathsWithLabel is AuditSystemCommandPaths under a flow
// label (FG-003). A nil label is fail-closed. Callers with a request
// context (buildExecCommand) pass flowLabelFromCtx(ctx); direct callers
// keep the StartLabel wrapper above.
func AuditSystemCommandPathsWithLabel(sb *SandboxConfig, command string, args []string, workingDir string, label *permissions.SessionLabel) error {
	return auditSystemCommandPaths(sb, command, args, workingDir, label, false)
}

// auditSystemCommandPaths is the approval-aware audit (S9/H1): approved
// carries the gate's local decision (a consumed one-shot grant or an
// in-stack approval), so an approved Ask admits instead of hard-denying.
// Deny always denies; Ask without approval denies.
func auditSystemCommandPaths(sb *SandboxConfig, command string, args []string, workingDir string, label *permissions.SessionLabel, approved bool) error {
	if sb == nil || sb.Mode == SandboxModeOff {
		return nil
	}
	// Permissions policy (PM-002): deny enforcement against the dual
	// shell identity (S9/M4: ShellLine primary + bare command fallback -
	// the same strings the gate matches). Ask/allow (and no installed
	// policy) leave the audit unchanged; the gate owns prompting.
	shellRes := permissions.ShellResources(command, args)
	if eff, rule, ok := permissions.Lookup("shell", shellRes...); ok && rule != nil && eff == permissions.EffectDeny {
		suffix := ""
		if rule != nil {
			suffix = fmt.Sprintf(" (rule: %s %q)", rule.Action, rule.Resource)
		}
		return fmt.Errorf("command denied by permissions policy%s", suffix)
	}
	// Flow Guard pre-dispatch (FG-003): Deny fails closed here (S9/M4
	// dual identity on shellRes). Ask admits only with the gate's local
	// decision (approved): the gate runs before this audit and already
	// prompted on flow Ask. A nil label (present-but-unregistered
	// session under v2) is fail-closed inside LookupWithLabel
	// (evaluated as narrowest), so skipping on nil would fail open.
	// Without a v2 policy the lookup reports ok=false and the audit
	// keeps exact v1 behavior.
	if eff, _, detail, ok := permissions.LookupWithLabel(label, "shell", shellRes...); ok && detail != "" && eff != permissions.EffectAllow {
		if eff == permissions.EffectDeny {
			return fmt.Errorf("command denied by flow guard: %s", detail)
		}
		if !approved {
			return fmt.Errorf("command denied by flow guard: %s", detail)
		}
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
	// WIN-005: expansion tokens whose variable name contains a space
	// ("%X Y%") split across tokenizer words, so the per-token check below
	// never sees the joined form; run it on the whole command line too.
	if err := checkShellExpansionAlias(command); err != nil {
		return err
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
// against deny rules AND read-root confinement, so "../"-style escapes
// cannot leave the sandbox; everything else passes, since bare words cannot
// leave the confined working directory. Glob metacharacters are
// pre-expanded because sh resolves them only after this audit runs.
func auditPathToken(sb *SandboxConfig, tok, relativeBase string) error {
	p := strings.TrimSpace(tok)
	if p == "" {
		return nil
	}
	expanded := expandHome(p)
	// WIN-005: reject Win32 path aliases (trailing dots/spaces, ADS,
	// device namespaces, drive-relative forms) before any containment
	// math - the OS would open the base path the string checks approve.
	// The resolution-relevant classes (device namespaces, drive-relative)
	// apply to EVERY token: quoting does not change how the OS resolves
	// them, so a quoted "C:secret file.txt" must not slip through just
	// because it carries spaces.
	if err := checkPathAliasGlobal(expanded); err != nil {
		return fmt.Errorf("command references %q which is rejected: %w", tok, err)
	}
	// A token carrying shell syntax or whitespace is a command clause
	// (e.g. "echo hi > /dev/null") or a quoted free-text argument (e.g. a
	// commit message), not a plain path operand; only then are the
	// per-component text classes (ADS colon+dot, trailing dots/spaces)
	// skipped, since legitimate prose contains those characters.
	if !strings.ContainsAny(p, " \t><|&;()^\r\n") {
		if err := checkPathAlias(expanded); err != nil {
			return fmt.Errorf("command references %q which is rejected: %w", tok, err)
		}
	}
	// WIN-005 (Windows): reject cmd.exe expansion tokens - the audit sees
	// the literal while cmd expands %VAR% to the OS path just before exec.
	if err := checkShellExpansionAlias(expanded); err != nil {
		return fmt.Errorf("command references %q which is rejected: %w", tok, err)
	}
	if !filepath.IsAbs(expanded) {
		if relativeBase == "" || !relativeOperandNeedsAudit(expanded) {
			return nil
		}
		resolved := filepath.Clean(filepath.Join(relativeBase, expanded))
		// Canonicalize the joined absolute form (handle-based on Windows)
		// so deny checks run against what the OS would open.
		if canon, cerr := canonicalizePath(resolved, false); cerr == nil {
			resolved = canon
		}
		if err := auditCandidatePaths(sb, resolveWithSymlinks(resolved), tok, true); err != nil {
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
	// WIN-005: canonicalize absolute tokens (folds junctions, 8.3 short
	// names, and case on Windows) before the deny/scope checks.
	if canon, cerr := canonicalizePath(expanded, false); cerr == nil {
		expanded = canon
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
		// sh expands every match; auditing only a prefix would let denied or
		// unconfined paths past the cap reach the command unaudited, so
		// reject instead of truncating.
		return fmt.Errorf("command references %q which expands to %d paths, exceeding the %d-match audit limit; narrow the pattern", tok, len(matches), maxGlobMatches)
	}
	for _, m := range matches {
		if err := auditCandidatePaths(sb, resolveWithSymlinks(m), tok, true); err != nil {
			return fmt.Errorf("%w (glob expansion)", err)
		}
	}
	return nil
}

// auditCandidatePaths applies the deny-root and denied-basename checks to
// every candidate path (literal plus its symlink-resolved forms). When
// requireScope is true - always the case for relative operands and their
// glob expansions - each candidate must also sit under a read root or a
// trusted system directory, so "../"-style escapes cannot leave the sandbox.
func auditCandidatePaths(sb *SandboxConfig, candidates []string, tok string, requireScope bool) error {
	for _, candidate := range candidates {
		// WIN-005: canonicalize every candidate (handle-based on Windows)
		// so a symlink/8.3/case alias cannot string-match past the deny
		// rules.
		if canon, cerr := canonicalizePath(candidate, false); cerr == nil {
			candidate = canon
		}
		for _, d := range sb.DenyRoots {
			if within(d, candidate) {
				return fmt.Errorf("command references %q which resolves into a denied sandbox root", tok)
			}
		}
		if sb.deniedBasename(candidate) {
			return fmt.Errorf("command references %q which is a denied sensitive file", tok)
		}
		if requireScope && !pathInAny(candidate, sb.ReadRoots) && !pathInAny(candidate, trustedExecDirs) {
			return fmt.Errorf("command references %q which resolves outside the sandbox (not under a read root %v nor a trusted system dir); add it to sandbox.read_roots in config.json or narrow the command", tok, sb.ReadRoots)
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
		Description: "Runs a system command or executable directly on the host machine synchronously and waits for it to finish or time out (default timeout 120s; pass timeout_seconds to override, or use system_exec_start for long-running work). Commands are checked against a harmful-command policy and may require approval. When the sandbox is active, commands that reference absolute paths outside the sandbox read roots or trusted system dirs are rejected. Not routed through the Python interpreter." + shellRoutingNote,
		// Durable resume (Phase 4): marks the call event with
		// LongRunningToolIDs so a restart mid-approval can resume.
		// Synchronous behavior is unchanged.
		IsLongRunning: true,
	}, func(ctx agent.Context, input SystemExecInput) (SystemExecOutput, error) {
		start := time.Now()
		procID := m.allocateID()
		workingDir := input.WorkingDir
		if workingDir == "" {
			workingDir = taskCWD
		}
		cmd, err := BuildExecCommandFor(ctx, input.Command, input.Args, workingDir, input.Env)
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
		// Windows: assign the started process to its kill-on-close Job
		// Object (no-op on Unix, where Setpgid already grouped the tree).
		if err := attachProcessTree(cmd); err != nil {
			util.DebugWarn("system_exec_job_attach_failed", "process_id", procID, "error", err.Error())
		}

		var timeoutTimer *time.Timer
		if timeout := EffectiveExecTimeout(input.TimeoutSeconds); timeout > 0 {
			timeoutTimer = time.AfterFunc(
				timeout,
				func() {
					rp.markTimedOut()
					// Tree-kill: process group (Unix) or Job Object (Windows).
					_ = killProcessTree(cmd)
				},
			)
			defer timeoutTimer.Stop()
		}

		runErr := cmd.Wait()
		releaseProcessTree(cmd)
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
		// Durable resume (Phase 4): marks the call event with
		// LongRunningToolIDs so a restart mid-approval can resume.
		// Synchronous behavior is unchanged.
		IsLongRunning: true,
	}, func(ctx agent.Context, input SystemExecStartInput) (SystemExecStartOutput, error) {
		workingDir := input.WorkingDir
		if workingDir == "" {
			workingDir = taskCWD
		}
		cmd, err := BuildExecCommandFor(ctx, input.Command, input.Args, workingDir, input.Env)
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
			if err := attachProcessTree(cmd); err != nil {
				util.DebugWarn("system_exec_job_attach_failed", "process_id", rp.id, "error", err.Error())
			}
			startCh <- startResult{err: nil}

			// Optional timeout: kill the process tree after
			// TimeoutSeconds (same tree-kill pattern as the sync
			// handler). Default 0 = no timeout.
			var timeoutTimer *time.Timer
			if input.TimeoutSeconds > 0 {
				timeoutTimer = time.AfterFunc(
					time.Duration(input.TimeoutSeconds*float64(time.Second)),
					func() {
						rp.markTimedOut()
						_ = killProcessTree(cmd)
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
			releaseProcessTree(cmd)
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
		// Tree-kill: process group (Unix) or Job Object (Windows) so
		// grandchildren die too.
		if !alreadyFinished {
			_ = killProcessTree(rp.cmd)
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
