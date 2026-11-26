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
	"sort"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/util"

	"github.com/google/shlex"
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
	// Git pre-gate enforcement: config/env influence the gate cannot see
	// (the per-call overlay, the ambient/replace environment, and the
	// working_dir repository config). An exec-hook env denies outright;
	// lesser overrides convert a gate Allow into an Ask so approval can
	// clear a benign case. Runs before the decision switch so Deny
	// short-circuits exactly like a gate deny and Ask flows through the
	// normal approval path below.
	if gitDeny, gitAsk := auditGitPreGate(command, args, workingDir, env, bo, sb); gitDeny != "" {
		AuditCommandFunc(CommandAuditEntry{
			Timestamp:   time.Now(),
			Tool:        "system_exec",
			Command:     command,
			Args:        args,
			CWD:         cd,
			SessionID:   sessionID,
			SandboxMode: sandboxMode,
			Decision:    "denied",
			Risk:        "unknown",
			Reason:      gitDeny,
		})
		return nil, fmt.Errorf("command denied by protection policy: %s", gitDeny)
	} else if gitAsk != "" {
		switch decision.Action {
		case ActionAllow:
			decision = GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: gitAsk}
		case ActionAsk:
			// The gate already asks, but its view hides the
			// workdir/config influence the pre-gate sees (env
			// overlay, repository hooks, directory overrides):
			// carry that reason into the approval prompt so the
			// human reviews the full influence. Deny precedence
			// is unchanged (deny never reaches this branch).
			if decision.Reason == "" {
				decision.Reason = gitAsk
			} else {
				decision.Reason = decision.Reason + "; " + gitAsk
			}
		}
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
//
// Git exec-hook keys (GIT_CONFIG_COUNT, GIT_CONFIG_PARAMETERS,
// GIT_CONFIG_KEY_*/GIT_CONFIG_VALUE_*, GIT_EXTERNAL_DIFF, GIT_PAGER,
// GIT_SSH, GIT_SSH_COMMAND, GIT_ASKPASS, and any non-sentinel
// GIT_EDITOR value) are always removed too: the gate classifies the
// argv it can see, so an env overlay that injects command execution
// into git must never reach the child (fail-closed: scrubbed absent,
// never passed through). Whole-file selectors (GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM) are NOT scrubbed: the engine and the test harness
// legitimately point them at owned identity configs, and the pre-gate
// Ask leg + the gate's own -c/env legs cover hostile content.
//
// Option-injection keys (TAR_OPTIONS, MAKEFLAGS, GNUMAKEFLAGS, MAKEFILES)
// are always removed: make/tar parse them as pre-options or extra makefiles
// before argv, so an ambient value could add a hidden -C, an exec flag
// (--to-command, -I), or an uninspected makefile that no argv scan can see.
// The scrub runs after the merge in buildEnvList, so the keys are removed
// from ambient and overlay values alike; the gate only audits assignments
// visible in argv, and a scrubbed overlay never reaches the child.
func ScrubEnv(env []string) []string {
	prefixes := [...]string{"HAKASE_", "AWS_", "GITHUB_", "OPENAI_"}
	caseInsensitive := runtime.GOOS == "windows"
	scrubbed := make([]string, 0, len(env))
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		if envKeyIsSensitive(key, prefixes[:], caseInsensitive) {
			continue
		}
		if isPythonScrubbedKey(key, caseInsensitive) {
			continue
		}
		if isGitScrubbedKey(key, caseInsensitive) {
			continue
		}
		if isGitEditorScrubbed(key, val, caseInsensitive) {
			continue
		}
		if isToolOptionScrubbedKey(key, caseInsensitive) {
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

// gitScrubbedKeys are the exact GIT_* exec-hook env names always removed
// from the child env (absent, not empty): arbitrary config injection
// (COUNT/PARAMETERS triples), the external diff driver, pagers, ssh
// commands, and askpass helpers all run commands. Whole-file selectors
// (GIT_CONFIG_GLOBAL/SYSTEM) are NOT scrubbed: the engine and the test
// harness legitimately point them at owned identity configs, and the
// pre-gate Ask leg + the gate's own -c/env legs cover hostile content.
var gitScrubbedKeys = []string{
	"GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS",
	"GIT_EXTERNAL_DIFF",
	"GIT_PAGER",
	"GIT_SSH",
	"GIT_SSH_COMMAND",
	"GIT_ASKPASS",
}

// gitScrubbedPrefixes are the GIT_* env name prefixes always removed:
// the numbered GIT_CONFIG_KEY_/GIT_CONFIG_VALUE_ pairs carry injected
// config values. Kept narrow (no blanket GIT_CONFIG_): file selectors
// stay out of the scrub list by design (see above).
var gitScrubbedPrefixes = []string{
	"GIT_CONFIG_KEY_",
	"GIT_CONFIG_VALUE_",
}

// toolOptionScrubbedKeys are the option-injection variables always removed
// from the child env (absent, not empty): make/tar parse them as pre-options
// (or extra makefiles) before argv, so an ambient or overlay value could add
// -C/--directory, --to-command, --use-compress-program, or an uninspected
// makefile without ever appearing in the argv the gate classifies. The gate
// only audits assignments visible in argv; the scrub runs after the merge in
// buildEnvList, so both the ambient and overlay values are removed and an
// overlay value never reaches the child.
var toolOptionScrubbedKeys = []string{
	"TAR_OPTIONS",
	"MAKEFLAGS",
	"GNUMAKEFLAGS",
	"MAKEFILES",
}

// isToolOptionScrubbedKey reports whether key is an option-injection key.
// Windows matches case-insensitively; Linux matches byte-exact.
func isToolOptionScrubbedKey(key string, caseInsensitive bool) bool {
	for _, k := range toolOptionScrubbedKeys {
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

// isGitScrubbedKey reports whether key is a git exec-hook scrubbed key.
// Windows matches case-insensitively; Linux matches byte-exact.
func isGitScrubbedKey(key string, caseInsensitive bool) bool {
	fold := func(a, b string) bool {
		if caseInsensitive {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	hasPrefix := func(s, p string) bool {
		if caseInsensitive {
			return len(s) >= len(p) && strings.EqualFold(s[:len(p)], p)
		}
		return strings.HasPrefix(s, p)
	}
	for _, k := range gitScrubbedKeys {
		if fold(key, k) {
			return true
		}
	}
	for _, p := range gitScrubbedPrefixes {
		if hasPrefix(key, p) {
			return true
		}
	}
	return false
}

// isGitEditorScrubbed reports whether a GIT_EDITOR entry must be removed
// from the child env: any value other than the engine's own
// non-interactive sentinel (true/false) names an executable editor hook.
// The sentinel passes through so the engine's non-interactive runs keep
// working; everything else is scrubbed absent, never passed through.
func isGitEditorScrubbed(key, val string, caseInsensitive bool) bool {
	match := key == "GIT_EDITOR"
	if caseInsensitive {
		match = strings.EqualFold(key, "GIT_EDITOR")
	}
	if !match {
		return false
	}
	return val != "true" && val != "false"
}

// stripSubstPrefix removes command/process-substitution openers from a
// token for trampoline comparison (gate parity): shlex keeps "$(git" and
// "<(git" as one token, hiding git/env from the base scan. Quoted payloads
// stay single multi-word tokens ("git ..." != git) so echo stays green.
func stripSubstPrefix(tok string) string {
	for {
		switch {
		case strings.HasPrefix(tok, "$("):
			tok = tok[2:]
		case strings.HasPrefix(tok, "<(") || strings.HasPrefix(tok, ">("):
			tok = tok[2:]
		case strings.HasPrefix(tok, "`") || strings.HasPrefix(tok, "("):
			tok = tok[1:]
		default:
			return tok
		}
	}
}

// gitLedBase resolves the comparison basename for git/env/cd trampoline
// detection: substitution openers stripped first (a `$(git -C ...)`
// substitution still executes git), then all shell-escape backslashes
// stripped (an interior escape like g\it executes as git, exactly like a
// leading \git), then the basename, lowercased. Comparison-only:
// tokenization elsewhere is unchanged, so quoted payloads stay single
// multi-word tokens ("git ..." != git) and echo stays green. A bare
// remainder never touches the filesystem beyond what filepath.Base does.
func gitLedBase(tok string) string {
	return strings.ToLower(filepath.Base(strings.ReplaceAll(stripSubstPrefix(tok), "\\", "")))
}

// isEnvAssignToken reports whether tok is a VAR=val environment
// assignment. Utility names never contain "=", so anything matching
// before the utility position is an assignment, not the command.
func isEnvAssignToken(tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if c == '=' {
			return i > 0
		}
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return false
}

// envShortSplitPos returns the index of -S in a short cluster when S is
// the first value-taking member (scanned left to right). A -C/-u/-a
// before S takes the remainder as its own value (e.g. -CS means -C with
// value "S", -uS means unset "S"), so it is not a split-string. Returns
// -1 when no split-string S is in option position.
func envShortSplitPos(a string) int {
	for k := 1; k < len(a); k++ {
		switch a[k] {
		case 'C', 'u', 'a':
			return -1
		case 'S':
			return k
		}
	}
	return -1
}

// SplitEnvSplitString tokenizes one env(1) -S/--split-string value with the
// same escape-aware shell lexer the gate uses (google/shlex). Sharing this
// entry point removes the previous divergence where the sandbox used a
// quote-strip-only tokenizer: both layers now decide utility and -C/--chdir
// positions from identical token streams. Returns ok=false for an
// unparseable value (unbalanced quote, trailing escape) or an empty token
// stream, which callers fail closed on.
func SplitEnvSplitString(val string) ([]string, bool) {
	toks, err := shlex.Split(val)
	if err != nil || len(toks) == 0 {
		return nil, false
	}
	return toks, true
}

// maxEnvSpliceDepth caps -S/--split-string expansions along one chain
// (budget 32, mirroring the gate's expandEnvSplitString fixpoint): each
// spliced value re-enters option scanning, so adversarial nesting
// (`env -S 'env -S ...'`) terminates fail-closed instead of blowing up
// CPU/stack. Bare `env env ...` nesting (no -S) is NOT capped: each layer
// only slices the remaining stream (rest[i+1:]) and appendEnvStream aliases
// the child result while the output is empty, so ONE unwrap of a 500-layer
// chain stays linear and still deny/allows on the real utility instead of
// failing closed at 32. That per-unwrap linearity does not make a
// per-position scan linear: re-unwrapping every suffix is quadratic or
// worse, so callers that walk tokens (gate interpreterTails/codeToolTails,
// the chdir/hard-deny env legs) bound their scan with coveredUntil and the
// settled stop rather than relying on this comment.
const maxEnvSpliceDepth = 32

// spliceEnvLayer consumes one leading env(1) option layer, preserving
// assignments in place: -S/--split-string values are spliced inline and
// re-scanned as an option stream (their assignments stay ahead of the
// utility so the deny scan sees them, and a smuggled -C/--chdir is
// collected like a literal one), VAR=val tokens pass through untouched,
// and value-taking flags (-u/-a, --unset/--argv0) consume their operand.
// -C/--chdir operands are collected in order (not consumed silently):
// each steers the utility's working directory, so the caller resolves
// them against the running directory and audits the result exactly like
// a git -C override. Short clusters evaluate left to right like getopt:
// the first value-taking member takes the attached remainder (or the
// next token when trailing). Returns unknown=true for a missing
// value operand, an unparseable -S value, an over-budget -S chain, or an
// unknown/ambiguous long option: the caller fails closed to approval
// instead of guessing the utility position.
func spliceEnvLayer(rest []string) (out []string, chdirs []string, unknown bool) {
	out, chdirs, _, unknown = spliceEnvLayerDepth(rest, 0)
	return out, chdirs, unknown
}

// splicedTailCoverage maps a spliced stream's coverage back to input
// coordinates: the synthetic -S words (spliced) are not input positions, so
// only tokens consumed past them count as additional input coverage. A chain
// that ends inside the spliced words covers none of the following input
// tokens, which may sit behind a wrapper the splice introduced.
func splicedTailCoverage(subCovered, spliced int) int {
	if subCovered > spliced {
		return subCovered - spliced
	}
	return 0
}

// appendEnvStream appends one spliced/recursed env stream to the output
// buffer without copying when the buffer is still empty. A bare nested-env
// chain otherwise copies the whole remaining stream once per layer
// (quadratic in the layer count) on the way back up the recursion; aliasing
// the child result keeps a deep chain linear. The child stream is read-only
// to every caller, so sharing its backing array is safe.
func appendEnvStream(out, sub []string) []string {
	if len(out) == 0 {
		return sub
	}
	return append(out, sub...)
}

// spliceEnvLayerDepth is spliceEnvLayer with the -S expansion depth.
// sDepth increments only on -S/--split-string splices (the only shape
// that re-grows the stream); nested-env unwraps keep the same depth
// because they strictly shorten the input.
//
// covered is the number of leading tokens of rest the option chain
// consumes, in rest's own coordinates: assignments, options and their
// operands, and nested env layers. A -S value's spliced words are not input
// positions, so they never add coverage; a chain that ends inside them
// covers only the flag and its value, never the following input tokens (a
// spliced wrapper can put those behind the utility). Callers use the span
// only to skip positions an earlier unwrap already scanned, so it must
// never over-report.
func spliceEnvLayerDepth(rest []string, sDepth int) (out []string, chdirs []string, covered int, unknown bool) {
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			tail := rest[i+1:]
			// SE-1: `env -- env -C /hook ls` executes the inner
			// env with its chdir, so unwrap the nested layer
			// instead of treating it as an opaque utility.
			// After --, assignments are operands, not overlays:
			// only a bare nested env unwraps here.
			if len(tail) > 0 && gitLedBase(tail[0]) == "env" {
				sub, subDirs, subCovered, subUnk := spliceEnvLayerDepth(tail[1:], sDepth)
				if subUnk {
					return nil, nil, 0, true
				}
				out = appendEnvStream(out, sub)
				chdirs = append(chdirs, subDirs...)
				return out, chdirs, i + 2 + subCovered, false
			}
			out = append(out, tail...)
			return out, chdirs, i + 1, false
		}
		if a == "-" {
			out = append(out, rest[i:]...)
			return out, chdirs, i, false
		}
		if isEnvAssignToken(a) {
			out = append(out, a)
			i++
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			val := ""
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				val = name[idx+1:]
				name = name[:idx]
				hasVal = true
			}
			switch resolveEnvLongName(name) {
			case envLongSplitString:
				tail := rest[i+1:]
				flagTokens := 1
				if !hasVal {
					if i+1 >= len(rest) {
						return nil, nil, 0, true
					}
					val = rest[i+1]
					tail = rest[i+2:]
					flagTokens = 2
				}
				toks, ok := SplitEnvSplitString(val)
				if !ok {
					return nil, nil, 0, true
				}
				// A -S value is itself an env option stream, spliced
				// where the flag stood: re-scan the spliced words so
				// a smuggled -C/--chdir steers the directory exactly
				// like a literal one (never silently dropped). An
				// unparseable splice fails closed. Budget 32: each
				// splice re-grows the stream, so adversarial nesting
				// fails closed instead of blowing up CPU.
				if sDepth+1 >= maxEnvSpliceDepth {
					return nil, nil, 0, true
				}
				sub, subDirs, subCovered, subUnk := spliceEnvLayerDepth(append(toks, tail...), sDepth+1)
				if subUnk {
					return nil, nil, 0, true
				}
				out = appendEnvStream(out, sub)
				chdirs = append(chdirs, subDirs...)
				return out, chdirs, i + flagTokens + splicedTailCoverage(subCovered, len(toks)), false
			case envLongTakesValue:
				// Only --chdir steers the working directory;
				// --unset/--argv0 operands are skipped.
				if envLongIsChdir(name) {
					if hasVal {
						chdirs = append(chdirs, val)
						i++
					} else {
						if i+1 >= len(rest) {
							return nil, nil, 0, true
						}
						chdirs = append(chdirs, rest[i+1])
						i += 2
					}
					continue
				}
				if hasVal {
					i++
				} else {
					if i+1 >= len(rest) {
						return nil, nil, 0, true
					}
					i += 2
				}
				continue
			case envLongBoolean:
				i++
				continue
			default:
				return nil, nil, 0, true
			}
		}
		if len(a) > 1 && a[0] == '-' {
			if k := envShortSplitPos(a); k >= 0 {
				var val string
				tail := rest[i+1:]
				flagTokens := 1
				if k+1 < len(a) {
					val = a[k+1:]
				} else {
					if i+1 >= len(rest) {
						return nil, nil, 0, true
					}
					val = rest[i+1]
					tail = rest[i+2:]
					flagTokens = 2
				}
				toks, ok := SplitEnvSplitString(val)
				if !ok {
					return nil, nil, 0, true
				}
				if prefix := a[1:k]; prefix != "" {
					out = append(out, "-"+prefix)
				}
				// Like the long branch: the spliced words re-enter
				// option scanning so a smuggled -C/--chdir steers
				// the directory exactly like a literal one. Budget
				// 32 like above.
				if sDepth+1 >= maxEnvSpliceDepth {
					return nil, nil, 0, true
				}
				sub, subDirs, subCovered, subUnk := spliceEnvLayerDepth(append(toks, tail...), sDepth+1)
				if subUnk {
					return nil, nil, 0, true
				}
				out = appendEnvStream(out, sub)
				chdirs = append(chdirs, subDirs...)
				return out, chdirs, i + flagTokens + splicedTailCoverage(subCovered, len(toks)), false
			}
			// Short cluster, evaluated left to right like getopt:
			// the first value-taking member (-u/-C/-a) takes the
			// attached remainder as its value, or the next token
			// when trailing; -C values are directory operands
			// collected for the caller, never consumed silently.
			// Anything else is a boolean cluster.
			consumedNext := false
			for k := 1; k < len(a); k++ {
				switch a[k] {
				case 'C':
					if k+1 < len(a) {
						chdirs = append(chdirs, a[k+1:])
					} else {
						if i+1 >= len(rest) {
							return nil, nil, 0, true
						}
						chdirs = append(chdirs, rest[i+1])
						consumedNext = true
					}
				case 'u', 'a':
					if k+1 >= len(a) {
						if i+1 >= len(rest) {
							return nil, nil, 0, true
						}
						consumedNext = true
					}
				default:
					continue
				}
				break
			}
			if consumedNext {
				i += 2
			} else {
				i++
			}
			continue
		}
		// SE-1: a VAR=val assignment before a nested env does not
		// stop option scanning the way real env(1) rescans: `env
		// -S 'X=1 env -C /evil git status'` splices to ["X=1"
		// "env" "-C" ...], and the inner env chdirs into /evil
		// exactly like a literal -C. Unwrap the nested layer,
		// keeping the collected assignments ahead of it.
		if gitLedBase(rest[i]) == "env" {
			sub, subDirs, subCovered, subUnk := spliceEnvLayerDepth(rest[i+1:], sDepth)
			if subUnk {
				return nil, nil, 0, true
			}
			out = appendEnvStream(out, sub)
			chdirs = append(chdirs, subDirs...)
			return out, chdirs, i + 1 + subCovered, false
		}
		out = append(out, rest[i:]...)
		return out, chdirs, i, false
	}
	return out, chdirs, i, false
}

// envLongKind classifies the env(1) long options the trampoline splice
// understands. Mirrors the gate's getopt_long set so both layers splice
// the same inputs the same way.
type envLongKind int

const (
	envLongUnknown envLongKind = iota
	envLongSplitString
	envLongTakesValue
	envLongBoolean
)

// envLongOptionNames is the canonical env(1) long-option set used for
// unique-prefix resolution (--s -> --split-string, --ch -> --chdir,
// --u -> --unset) on both security layers. The gate consumes it through
// EnvLongOptionNames so the two layers cannot drift: adding an option on one
// side changes both resolutions at once.
var envLongOptionNames = []string{
	"ignore-environment", "chdir", "null", "unset", "split-string",
	"argv0", "ignore-signal", "block-signal", "default-signal",
	"list-signal-handling", "debug", "help", "version",
}

// EnvLongOptionNames returns a copy of the canonical env(1) long-option set.
// Callers get an independent slice, so mutating the result cannot change the
// resolver or the other security layer.
func EnvLongOptionNames() []string {
	return append([]string(nil), envLongOptionNames...)
}

// EnvLongOptionKind resolves an env(1) long-option name (without leading
// dashes or "=value") by unique getopt_long prefix and reports the canonical
// kind: "split-string", "value", "boolean", or "" for an unknown or
// ambiguous name. It is the shared resolution contract the gate mirrors, so
// cross-layer parity tests can pin both classifiers to one table.
func EnvLongOptionKind(name string) string {
	kind, _ := matchEnvLongName(name)
	switch kind {
	case envLongSplitString:
		return "split-string"
	case envLongTakesValue:
		return "value"
	case envLongBoolean:
		return "boolean"
	}
	return ""
}

// resolveEnvLongName is the known env(1) long-option set for unique-prefix
// resolution (--s -> --split-string, --ch -> --chdir, --u -> --unset).
// Unknown or ambiguous names fail closed (envLongUnknown).
func resolveEnvLongName(name string) envLongKind {
	kind, _ := matchEnvLongName(name)
	return kind
}

// matchEnvLongName resolves an env(1) long-option name by unique prefix
// and also reports the matched full name so callers can distinguish the
// value-taking options (--chdir steers the working directory while
// --unset/--argv0 do not).
func matchEnvLongName(name string) (envLongKind, string) {
	var match string
	count := 0
	for _, full := range envLongOptionNames {
		if full == name {
			match = full
			count = 1
			break
		}
		if strings.HasPrefix(full, name) {
			match = full
			count++
		}
	}
	if count != 1 {
		return envLongUnknown, ""
	}
	switch match {
	case "split-string":
		return envLongSplitString, match
	case "unset", "chdir", "argv0":
		return envLongTakesValue, match
	default:
		return envLongBoolean, match
	}
}

// envLongIsChdir reports whether an env(1) long-option name (without
// leading dashes or =value) uniquely resolves to --chdir by the same
// unique-prefix rule as resolveEnvLongName. Only the chdir operand
// steers the utility's working directory; --unset/--argv0 values do
// not, so only chdir operands are collected for the workdir audit.
func envLongIsChdir(name string) bool {
	_, match := matchEnvLongName(name)
	return match == "chdir"
}

// unwrapEnvLayers fully unwraps nested env(1) layers, preserving
// assignments in place: `env -S 'GIT_EXTERNAL_DIFF=id git status'`
// splices to ["GIT_EXTERNAL_DIFF=id" "git" "status"] so the git-led scan
// sees both the utility and the assignments ahead of it (the assignments
// are evaluated as an env overlay before the strip, never silently
// dropped). Also collects the -C/--chdir directory operands of every
// layer in order: each steers the utility's working directory, so the
// caller resolves them against the running directory instead of auditing
// the wrong repository. Returns unknown=true when a layer cannot be
// parsed: the caller fails closed to approval. A bare env (no utility)
// yields an empty stream.
func unwrapEnvLayers(argv []string) (eff []string, chdirs []string, unknown bool) {
	eff = argv
	for len(eff) > 0 {
		if gitLedBase(eff[0]) == "env" {
			next, dirs, unk := spliceEnvLayer(eff[1:])
			if unk {
				return nil, nil, true
			}
			chdirs = append(chdirs, dirs...)
			eff = next
			continue
		}
		// SE-1: leading assignments do not stop a nested env
		// (`env -S 'X=1 env -C /evil git status'` splices to
		// ["X=1" "env" "-C" ...]): skip the overlay prefix and
		// unwrap the nested layer, keeping the assignments ahead
		// of the effective stream. spliceEnvLayer already recurses
		// this way; this loop covers streams built elsewhere.
		j := 0
		for j < len(eff) && isEnvAssignToken(eff[j]) {
			j++
		}
		if j > 0 && j < len(eff) && gitLedBase(eff[j]) == "env" {
			next, dirs, unk := spliceEnvLayer(eff[j+1:])
			if unk {
				return nil, nil, true
			}
			chdirs = append(chdirs, dirs...)
			eff = append(append([]string{}, eff[:j]...), next...)
			continue
		}
		break
	}
	return eff, chdirs, false
}

// envChainCovered reports how many leading tokens of rest belong to the
// env(1) option/utility chain that unwrapEnvLayers would consume from
// append([]string{"env"}, rest...): options and their operands, assignments,
// nested env layers, and a `--`-guarded nested env. It delegates to the
// splice itself, so the span is exactly what the unwrap consumed in rest's
// own coordinates: a -S value's spliced words are not input positions, so a
// chain that ends inside them covers only the flag and its value, never the
// following input tokens (which may sit behind a wrapper the splice
// introduced). A caller that scans every env token position can therefore
// skip positions an earlier unwrap already consumed without ever moving
// past one it did not. An unknown shape stops the count at zero; the caller
// fails closed anyway.
func envChainCovered(rest []string) int {
	_, _, covered, unknown := spliceEnvLayerDepth(rest, 0)
	if unknown {
		return 0
	}
	return covered
}

// envTrampolinePrefixes are the delegating binaries that keep a following
// env(1) in command position ("sudo env -C ...", "xargs env ...",
// "time env -C ..."). It is the single source of truth shared with the
// gate's interpreter-wrapper check (internal/agent imports this package; the
// reverse would cycle), so the two layers can never drift: an env layer
// behind a missing wrapper would end a scan early and hide its -C/-S payload.
// The map is unexported and read only through IsEnvTrampolinePrefix, so an
// importer can never mutate the authoritative set.
var envTrampolinePrefixes = map[string]bool{
	"sudo": true, "su": true, "doas": true,
	"nice": true, "nohup": true, "timeout": true,
	"xargs": true, "find": true, "env": true,
	"chroot": true, "runuser": true, "setsid": true, "stdbuf": true,
	"unshare": true, "nsenter": true, "gosu": true, "tini": true,
	"flock": true, "ionice": true,
	// time(1) delegates to the following command; without it the settled
	// stop treats "time env -C ..." as a plain utility and hides the
	// inner env's -C/-S payload.
	"time": true,
}

// IsEnvTrampolinePrefix reports whether base names a delegating binary that
// keeps a following env(1) in command position. The prefix table stays
// private so callers cannot widen or narrow the security decision in place.
func IsEnvTrampolinePrefix(base string) bool {
	return envTrampolinePrefixes[base]
}

// maxEnvPositionScans bounds the every-position env scan: each processed
// position unwraps its suffix, so an adversarial alternating wrapper/env
// chain ("env sudo env sudo ...") would otherwise stay quadratic even
// though it never reaches a plain utility. Past the bound the caller fails
// closed (unknown -> approval) rather than scanning forever.
const maxEnvPositionScans = 128

// maxSettledEnvScans bounds the extra env positions rescanned after the
// unwrapped stream settles on a plain utility. A settled word may delegate
// (time) or permute its operands (make/tar getopt), so the immediately
// trailing env -C must still be inspected; a long interleaved tail is
// operands of that settled utility and is not re-expanded without bound.
const maxSettledEnvScans = 32

// envChainSettled reports whether the effective stream produced by
// unwrapEnvLayers is led by a real utility rather than an assignment,
// wrapper prefix, or another env layer. A settled stream means later env
// tokens are that utility's operands; callers keep a bounded tail rescan
// (maxSettledEnvScans) because the settled word may delegate or permute its
// operands. A bare env (empty stream) is settled too: there is no utility
// left to unwrap.
func envChainSettled(eff []string) bool {
	for _, tok := range eff {
		if tok == "" || isEnvAssignToken(tok) {
			continue
		}
		return !IsEnvTrampolinePrefix(gitLedBase(tok))
	}
	return true
}

// envChdirInToks reports whether any env(1) layer in one segment's tokens
// carries a -C/--chdir directory operand, even when the segment is not
// git-led (`env -C /hook ls` steers ls into /hook exactly like it would
// steer git). Env positions in command position are tried (sudo env -C ...),
// with -S splices expanded by unwrapEnvLayers. A settled plain utility does
// not stop the scan: the settled word is attacker-chosen and may delegate
// (time) or permute its operands (make/tar getopt), leaving a later env -C
// live, so trailing env positions are rescanned. Positions already consumed
// by an earlier unwrap are skipped (envChainCovered), so deep nesting costs
// one unwrap per disjoint env invocation instead of one per env token. A
// work budget fails closed on pathological chains instead of scanning
// forever. Returns
// found when any layer yields a directory operand, unknown when any layer
// cannot be parsed or the budget is exhausted (fail closed to approval;
// reported alongside found so the caller never audits a guessed directory).
func envChdirInToks(toks []string) (found bool, unknown bool) {
	coveredUntil := 0
	scanned := 0
	settled := false
	trailing := 0
	for i, tok := range toks {
		if tok == "" || strings.HasPrefix(tok, "-") || isEnvAssignToken(tok) {
			continue
		}
		if gitLedBase(tok) != "env" {
			continue
		}
		if i < coveredUntil {
			continue
		}
		if settled {
			// A settled word may delegate (time) or permute its
			// operands (make/tar getopt), so a trailing env -C is
			// still live. Rescan a bounded tail instead of stopping
			// at settlement.
			if trailing >= maxSettledEnvScans {
				break
			}
			trailing++
		} else {
			if scanned >= maxEnvPositionScans {
				unknown = true
				break
			}
			scanned++
		}
		if end := i + 1 + envChainCovered(toks[i+1:]); end > coveredUntil {
			coveredUntil = end
		}
		eff, dirs, unk := unwrapEnvLayers(toks[i:])
		if unk {
			unknown = true
			continue
		}
		if len(dirs) > 0 {
			found = true
		}
		if len(eff) == 0 || envChainSettled(eff) {
			settled = true
		}
	}
	return found, unknown
}

// scanGitInToks finds the git utility in one segment's tokens, unwrapping
// env trampolines at env token positions in command position (leading or
// behind wrapper prefixes like "sudo env -S 'GIT_PAGER=x git status'"): -S
// splices inline with assignments preserved, so the deny scan sees the
// assignments ahead of git. A settled plain utility does not stop the scan:
// the settled word may delegate or permute its operands, so a later env -C
// can still steer the git utility; trailing env positions are rescanned.
// Positions already consumed by an earlier unwrap are skipped
// (envChainCovered), so deep nesting costs one unwrap per disjoint env
// invocation instead of one per env token. A work budget fails closed on
// pathological chains instead of scanning forever. Returns the assignments the spawned
// git reads as its env overlay (wrapper-prefix assigns plus spliced assigns),
// the ordered -C/--chdir directory operands from the outermost unwrapped
// layer (every nested layer's operand accumulates in order;
// resolveEnvChdirs chains them), ok when a git utility leads, and unknown
// when an env layer cannot be parsed or the budget is exhausted (fail closed
// to approval; reported even alongside a found utility so the caller never
// audits a guessed directory). With no env trampoline it falls back to the
// direct utility scan. Quoted payloads stay single multi-word tokens so
// `echo "git ..."` stays green.
func scanGitInToks(toks []string) (assigns []string, chdirs []string, ok bool, unknown bool) {
	collectAssigns := func(intoks []string) []string {
		var out []string
		for _, a := range intoks {
			if isEnvAssignToken(a) {
				out = append(out, a)
			}
		}
		return out
	}
	coveredUntil := 0
	scanned := 0
	foundAt := -1
	settled := false
	trailing := 0
	var foundTailAssigns []string
	var foundChdirs []string
	for i, tok := range toks {
		if tok == "" || strings.HasPrefix(tok, "-") || isEnvAssignToken(tok) {
			continue
		}
		if gitLedBase(tok) != "env" {
			continue
		}
		if i < coveredUntil {
			continue
		}
		if settled {
			if trailing >= maxSettledEnvScans {
				break
			}
			trailing++
		} else {
			if scanned >= maxEnvPositionScans {
				unknown = true
				break
			}
			scanned++
		}
		if end := i + 1 + envChainCovered(toks[i+1:]); end > coveredUntil {
			coveredUntil = end
		}
		eff, dirs, unk := unwrapEnvLayers(toks[i:])
		if unk {
			unknown = true
			continue
		}
		if _, a, ok := gitUtilityTail(eff); ok {
			foundAt = i
			foundTailAssigns = a
			foundChdirs = dirs
		}
		// Same settled-word rule as envChdirInToks: a later env token is
		// rescanned because the settled word may delegate or permute its
		// operands, and its -C still steers the git utility found here.
		if len(eff) == 0 || envChainSettled(eff) {
			settled = true
		}
	}
	if foundAt >= 0 {
		return append(collectAssigns(toks[:foundAt]), foundTailAssigns...), foundChdirs, true, unknown
	}
	// No env trampoline led to git: direct utility scan over wrapper
	// prefixes (sudo git, ...). Unquoted `echo git status` is also
	// scanned here fail-closed (it only asks when the audited
	// repository or config is itself influenced, staying silent in
	// clean repositories); quoted `echo "git ..."` stays one token
	// and never matches.
	if _, a, ok := gitUtilityTail(toks); ok {
		return a, nil, true, unknown
	}
	return nil, nil, false, unknown
}

// gitUtilityTail finds the git utility in an env-unwrapped argv, scanning
// every token position like the gate's H1 token scan: wrapper prefixes
// (sudo, nice, ...) and trampoline residues (xargs, find -exec, VAR=
// overlays) place git after argv[0], so an argv[0]-only check misses
// "sudo git -c ... status". Returns the tail from git onward plus the
// VAR=val assignments ahead of it (the env overlay the spawned git
// reads). ok=false when no git utility leads the stream. The scan is
// deliberately every-position, so an unquoted `echo git status` IS
// treated as git-led: fail-closed (it only asks when the audited
// repository or config is itself influenced, and stays silent in clean
// repositories). Quoted payloads stay single multi-word tokens
// ("git ..." != git) so `echo "git ..."` stays green.
func gitUtilityTail(eff []string) (tail []string, assigns []string, ok bool) {
	for i, tok := range eff {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") || isEnvAssignToken(tok) {
			continue
		}
		if gitLedBase(tok) != "git" {
			continue
		}
		for _, a := range eff[:i] {
			if isEnvAssignToken(a) {
				assigns = append(assigns, a)
			}
		}
		return eff[i:], assigns, true
	}
	return nil, nil, false
}

// isEnvLedArgv reports whether argv starts with an env(1) trampoline
// (after escape stripping), used to fail closed on unparseable layers.
func isEnvLedArgv(argv []string) bool {
	return len(argv) > 0 && gitLedBase(argv[0]) == "env"
}

// gitEnvHasGitPrefix reports whether key carries the GIT_ namespace.
// Windows matches case-insensitively (environment names are
// case-insensitive there, and git reads folded names like
// git_config_global); Linux stays byte-exact.
func gitEnvHasGitPrefix(key string, caseInsensitive bool) bool {
	if caseInsensitive {
		return len(key) >= 4 && strings.EqualFold(key[:4], "GIT_")
	}
	return strings.HasPrefix(key, "GIT_")
}

// gitPreGateDeniedKey reports whether a GIT_* env name denies a git build
// outright (arbitrary config injection or exec hooks). Mirrors the
// ScrubEnv list (gitScrubbedKeys plus the value-aware GIT_EDITOR rule):
// the child never receives these, and the build never runs while they
// are set. GIT_EDITOR denies unless it carries the engine's own
// non-interactive sentinel (true/false). Whole-file selectors
// (GIT_CONFIG_GLOBAL/SYSTEM) and other GIT_* names are Ask-level, not
// deny: approval can clear a benign owned config after inspection.
// Windows matches case-insensitively; Linux matches byte-exact.
func gitPreGateDeniedKey(key, val string, caseInsensitive bool) bool {
	eq := func(a, b string) bool {
		if caseInsensitive {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if eq(key, "GIT_EDITOR") {
		return val != "true" && val != "false"
	}
	for _, k := range gitScrubbedKeys {
		if eq(key, k) {
			return true
		}
	}
	hasPrefix := func(s, p string) bool {
		if caseInsensitive {
			return len(s) >= len(p) && strings.EqualFold(s[:len(p)], p)
		}
		return strings.HasPrefix(s, p)
	}
	if hasPrefix(key, "GIT_CONFIG_KEY_") || hasPrefix(key, "GIT_CONFIG_VALUE_") {
		return true
	}
	return false
}

// gitPreGateSkippedKey exempts the engine's own non-interactive env from
// the Ask leg: GIT_TERMINAL_PROMPT (fail-fast prompts, any value) and
// GIT_EDITOR carrying the non-interactive sentinel (true/false). Any
// other GIT_EDITOR value is an executable hook and is never skipped.
// Windows matches case-insensitively; Linux matches byte-exact.
func gitPreGateSkippedKey(key, val string, caseInsensitive bool) bool {
	if caseInsensitive {
		if strings.EqualFold(key, "GIT_TERMINAL_PROMPT") {
			return true
		}
		return strings.EqualFold(key, "GIT_EDITOR") && (val == "true" || val == "false")
	}
	if key == "GIT_TERMINAL_PROMPT" {
		return true
	}
	return key == "GIT_EDITOR" && (val == "true" || val == "false")
}

// shellSegment is one shell command segment plus the chaining operator
// that preceded it ("" for the first segment). The operator is reported
// because a || around a tracked cd leaves the directory uncertain: with
// `cd /hook || git status` the git runs in the pre-cd directory (the cd
// failed), and with `x || cd /hook` the cd itself runs conditionally.
// Either shape fails closed to approval rather than auditing a guessed
// directory.
type shellSegment struct {
	text string
	sep  string
}

// splitShellSegmentsWithSeps splits a shell command line on unquoted
// chaining and pipeline operators (&&, ||, ;, |, single &) and newlines
// for the pre-gate workdir audit, reporting the operator that preceded
// each segment. Quote-aware (single/double quotes and backslash
// escapes); empty segments are dropped. Deliberately small: only the
// segment boundaries, the joining operators, and cd operands matter
// here, not full shell semantics.
//
// Deliberate subset of the gate's splitShellSegments (internal/agent):
// this lexer has no &>/>& redirect handling (a `2>&1` FD-duplication
// splits here) and no escaped-operator tracking, because the pre-gate
// only needs boundaries and cd operands, never full shell semantics.
// The divergence is pinned by TestSplitShellSegmentsLiteOps. Do not
// "fix" one lexer without updating the other and its pin test.
func splitShellSegmentsWithSeps(command string) []shellSegment {
	var segs []shellSegment
	var cur strings.Builder
	inSingle, inDouble, escaped := false, false, false
	pending := ""
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, shellSegment{text: s, sep: pending})
		}
		cur.Reset()
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inSingle {
			cur.WriteRune(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if escaped {
			cur.WriteRune(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
			cur.WriteRune(c)
		case '\'':
			if !inDouble {
				inSingle = true
			}
			cur.WriteRune(c)
		case '"':
			inDouble = !inDouble
			cur.WriteRune(c)
		case '&':
			if inDouble {
				cur.WriteRune(c)
			} else if i+1 < len(runes) && runes[i+1] == '&' {
				flush()
				pending = "&&"
				i++
			} else {
				flush()
				pending = "&"
			}
		case '|':
			if !inDouble {
				flush()
				pending = "|"
				if i+1 < len(runes) && (runes[i+1] == '|' || runes[i+1] == '&') {
					if runes[i+1] == '|' {
						pending = "||"
					} else {
						pending = "|&"
					}
					i++
				}
			} else {
				cur.WriteRune(c)
			}
		case ';', '\n', '\r':
			if !inDouble {
				flush()
				pending = string(c)
			} else {
				cur.WriteRune(c)
			}
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return segs
}

// splitShellSegmentsLite splits a shell command line on unquoted chaining
// and pipeline operators for the pre-gate workdir audit. See
// splitShellSegmentsWithSeps for the subset contract.
func splitShellSegmentsLite(command string) []string {
	segs := splitShellSegmentsWithSeps(command)
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.text)
	}
	return out
}

// stripCdGroupOpeners removes subshell/group openers from the front of a
// shell segment's tokens before cd/pushd/popd classification: "(cd /hook
// && ...)" tokenizes with an attached opener ("(cd") and "{ cd /hook"
// with a standalone one ("{"), both of which change the directory
// exactly like a bare cd. Reports whether any opener was removed: a
// grouped segment's trailing closer attaches to the last token
// ("(cd /x)" is a single segment ending in "/x)"), so it is trimmed
// there; an ungrouped operand keeps its bytes (a literal trailing paren
// is a legal directory name outside grouping).
func stripCdGroupOpeners(toks []string) ([]string, bool) {
	i := 0
	for i < len(toks) && (toks[i] == "(" || toks[i] == "{") {
		i++
	}
	rest := toks[i:]
	grouped := i > 0
	if len(rest) == 0 {
		return toks, grouped
	}
	if stripped := strings.TrimLeft(rest[0], "({"); stripped != rest[0] {
		grouped = true
		out := make([]string, 0, len(rest))
		out = append(out, stripped)
		out = append(out, rest[1:]...)
		rest = out
	}
	if grouped {
		out := make([]string, len(rest))
		copy(out, rest)
		out[len(out)-1] = strings.TrimRight(out[len(out)-1], ")}")
		rest = out
	}
	return rest, grouped
}

// StripCdGroupOpeners is the exported accessor for stripCdGroupOpeners so the
// agent gate's command-position walk shares one subshell/group-opener rule
// with the sandbox cd/pushd/popd classifier (internal/agent imports the
// sandbox leaf; the reverse would create a cycle). See stripCdGroupOpeners
// for the exact contract.
func StripCdGroupOpeners(toks []string) ([]string, bool) {
	return stripCdGroupOpeners(toks)
}

// isCdFlagToken reports whether tok is a cd(1) option skipped while
// searching for the operand: an exact -L/-P/-e/-@ or a cluster of those
// letters ("-LP"). Anything else dash-shaped ("-", "-x") is an operand
// (or an unknown shape), never a skipped flag; "--" is handled by the
// caller because it ends option parsing.
func isCdFlagToken(tok string) bool {
	if len(tok) < 2 || tok[0] != '-' || tok == "--" {
		return false
	}
	for i := 1; i < len(tok); i++ {
		switch tok[i] {
		case 'L', 'P', 'e', '@':
		default:
			return false
		}
	}
	return true
}

// isCdVarStart reports whether c can start a shell variable name.
func isCdVarStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// isCdVarChar reports whether c can continue a shell variable name.
func isCdVarChar(c byte) bool {
	return isCdVarStart(c) || (c >= '0' && c <= '9')
}

// isCdVarName reports whether name is a bare shell variable name
// (VAR, not VAR:-default or an empty ${}).
func isCdVarName(name string) bool {
	if name == "" || !isCdVarStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isCdVarChar(name[i]) {
			return false
		}
	}
	return true
}

// expandCdOperand expands a cd/pushd operand the way the shell would see
// it: a leading ~ from $HOME ("~user" has no passwd lookup here and is
// unresolvable), then $VAR/${VAR} from the child environment (overlay
// over ambient/replace, scrubbed exactly like the spawned process
// receives it). Any other $ form ($?, $$, $1, ${VAR:-default}, a
// missing variable, a lone backslash escape) is unresolvable: the audit
// must not guess the directory. Single pass, no re-expansion of values.
func expandCdOperand(target string, lookupEnv func(string) (string, bool)) (string, bool) {
	if strings.Contains(target, "\\") {
		return "", false
	}
	rest := target
	if rest == "~" || strings.HasPrefix(rest, "~/") {
		home, ok := lookupEnv("HOME")
		if !ok || home == "" {
			return "", false
		}
		if rest == "~" {
			return home, true
		}
		rest = home + rest[1:]
	} else if strings.HasPrefix(rest, "~") {
		return "", false
	}
	if !strings.Contains(rest, "$") {
		return rest, true
	}
	var b strings.Builder
	for i := 0; i < len(rest); {
		c := rest[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(rest) {
			return "", false
		}
		n := rest[i+1]
		if n == '{' {
			end := strings.IndexByte(rest[i+2:], '}')
			if end < 0 {
				return "", false
			}
			name := rest[i+2 : i+2+end]
			if !isCdVarName(name) {
				return "", false
			}
			v, ok := lookupEnv(name)
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i += 2 + end + 1
			continue
		}
		if isCdVarStart(n) {
			j := i + 1
			for j < len(rest) && isCdVarChar(rest[j]) {
				j++
			}
			v, ok := lookupEnv(rest[i+1 : j])
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i = j
			continue
		}
		return "", false
	}
	return b.String(), true
}

// cdWrapperPrefixes are shell builtins that delegate to the following
// utility without changing the directory themselves: `command cd /hook`,
// `builtin cd /hook`, and `exec cd /hook` all change the directory
// exactly like a bare cd, so the audit skips them (plus VAR=
// assignments, which never execute) to reach the first operand. Any
// other wrapper (sudo, time, nice, ...) is left in place: a cd behind it
// cannot be modeled, and the beyond-first-operand net below fails it
// closed instead.
var cdWrapperPrefixes = map[string]bool{"command": true, "builtin": true, "exec": true}

// skipCdPrefixes skips leading VAR=val assignments and command/builtin/
// exec delegation wrappers to reach the first operand of a shell
// segment for cd/pushd/popd classification. Reuses isEnvAssignToken so
// `VAR=1 cd /hook` tracks exactly like `cd /hook`.
func skipCdPrefixes(toks []string) []string {
	i := 0
	for i < len(toks) {
		if isEnvAssignToken(toks[i]) {
			i++
			continue
		}
		if cdWrapperPrefixes[gitLedBase(toks[i])] {
			i++
			continue
		}
		break
	}
	return toks[i:]
}

// isCdCommand reports whether tok names the cd/pushd/popd family for the
// segment-aware workdir audit (escape/basename/case-folded like every
// other trampoline comparison).
func isCdCommand(tok string) bool {
	switch gitLedBase(tok) {
	case "cd", "pushd", "popd":
		return true
	}
	return false
}

// cdTokenBeyondFirst reports whether a cd/pushd/popd token appears past
// the first operand of a prefix-skipped segment (`sudo cd /hook`,
// `time cd /hook`). Such a shape cannot be modeled - the token is an
// operand of another command, not a directory change the audit can
// replay - so the caller fails closed to approval rather than auditing
// a guessed directory. Tokens containing a slash are excluded (a path
// operand like /bin/cd never executes as the cd builtin).
func cdTokenBeyondFirst(eff []string) bool {
	for _, tok := range eff[1:] {
		if strings.Contains(tok, "/") {
			continue
		}
		if isCdCommand(tok) {
			return true
		}
	}
	return false
}

// isCdRedirectOp reports whether tok is a shell output/input redirect
// operator (`>`, `>>`, `2>`, `>&`, `<<`, ...), so the operand-trailing
// scan can skip it (and its target) instead of mistaking a redirection
// (`cd /x 2>/dev/null`) for an unresolvable extra operand.
func isCdRedirectOp(tok string) bool {
	s := tok
	for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = s[1:]
	}
	switch s {
	case ">", ">>", ">|", ">&", "<", "<<", "<>", "<&", "&>", "&>>":
		return true
	}
	return false
}

// countSubshellParens counts unquoted ( and ) bytes in a shell segment
// for subshell-depth tracking. Quote-aware (single/double quotes and
// backslash escapes, mirroring the segment splitter): a quoted paren
// never opens a subshell, while a $(...) substitution is a real
// subshell whose cd cannot affect the parent either way. The counts
// drive a depth stack in the workdir audit: a cd inside (...) updates
// only the subshell-scoped directory, while a cd inside {...} (no
// parens) persists exactly like a bare cd.
func countSubshellParens(seg string) (opens, closes int) {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'':
			if !inDouble {
				inSingle = true
			}
		case '"':
			inDouble = !inDouble
		case '(':
			if !inDouble {
				opens++
			}
		case ')':
			if !inDouble {
				closes++
			}
		}
	}
	return opens, closes
}

// resolveEnvChdirs resolves env(1) -C/--chdir operands in order against
// cur for the segment-aware workdir audit: each operand steers the
// utility's working directory, so a relative operand joins onto the
// directory left by the previous one (or by a tracked cd), exactly like
// the spawned env process would see it. Operands expand like cd
// operands ($VAR/~/scrubbed-env); unlike cd flags they are literal, so
// a dash-led value resolves as a name. Returns ok=false when any
// operand is unresolvable: the caller fails closed to approval rather
// than auditing a guessed directory.
func resolveEnvChdirs(cur string, dirs []string, lookupEnv func(string) (string, bool)) ([]string, bool) {
	d := cur
	var out []string
	for _, raw := range dirs {
		exp, ok := expandCdOperand(raw, lookupEnv)
		if !ok || exp == "" {
			return nil, false
		}
		if filepath.IsAbs(exp) {
			d = filepath.Clean(exp)
		} else if d == "" {
			d = filepath.Clean(exp)
		} else {
			d = filepath.Clean(filepath.Join(d, exp))
		}
		out = append(out, d)
	}
	return out, true
}

// resolveCdTarget resolves a `cd`/`pushd` operand against cur for the
// segment-aware workdir audit, returning ok=false when the target is
// unresolvable: a missing operand with no HOME (bare `pushd` swaps the
// stack, `popd` rotates it - both unknown), "-" (previous directory), a
// flag-shaped operand, an unresolvable expansion, "~user", a lone "-",
// or any operand past the target (`cd /x extra` fails in the shell, so
// the directory is unknown - redirections and their targets are not
// operands and are skipped). The caller fails closed to approval rather
// than auditing a guessed directory. Flags (-L/-P/-e/-@ clusters) and
// "--" are skipped to find the operand; after "--" a dash-led operand
// is a literal name, "-" alone still means the previous directory.
// Absolute targets replace cur; relative targets join onto it.
func resolveCdTarget(cur string, toks []string, lookupEnv func(string) (string, bool)) (string, bool) {
	if len(toks) == 0 {
		return cur, false
	}
	cmd := gitLedBase(toks[0])
	if cmd == "popd" {
		return cur, false
	}
	if cmd != "cd" && cmd != "pushd" {
		return cur, false
	}
	i := 1
	if cmd == "cd" {
		for i < len(toks) && isCdFlagToken(toks[i]) {
			i++
		}
	}
	dashed := false
	if i < len(toks) && toks[i] == "--" {
		i++
		dashed = true
	}
	var target string
	if i < len(toks) {
		target = toks[i]
		if cmd == "pushd" && !dashed && (strings.HasPrefix(target, "-") || strings.HasPrefix(target, "+")) {
			return cur, false
		}
		if !dashed && strings.HasPrefix(target, "-") {
			return cur, false
		}
	} else {
		// Missing operand: bare `cd` enters $HOME; bare `pushd`
		// swaps the stack (unknown).
		if cmd == "pushd" {
			return cur, false
		}
		home, ok := lookupEnv("HOME")
		if !ok || home == "" {
			return cur, false
		}
		target = home
	}
	expanded, ok := expandCdOperand(target, lookupEnv)
	if !ok || expanded == "" || expanded == "-" {
		return cur, false
	}
	if !dashed && strings.HasPrefix(expanded, "-") {
		return cur, false
	}
	// Any operand past the target is unresolvable: `cd /x extra`
	// fails in the shell, so the directory is unknown. Redirections
	// (`cd /x 2>/dev/null`) are not operands: operators and their
	// targets are skipped, as are attached forms (`2>&1`).
	redirTarget := false
	for k := i + 1; k < len(toks); k++ {
		t := toks[k]
		if redirTarget {
			redirTarget = false
			continue
		}
		if isCdRedirectOp(t) {
			redirTarget = true
			continue
		}
		if strings.ContainsAny(t, "><") {
			continue
		}
		return cur, false
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), true
	}
	if cur == "" {
		return filepath.Clean(expanded), true
	}
	return filepath.Clean(filepath.Join(cur, expanded)), true
}

// childEnvMap renders the effective child environment (ambient or
// replace set plus the per-call overlay, scrubbed exactly like the
// spawned process receives it) as a lookup map for cd-operand
// expansion. Building it from buildEnvList keeps expansion honest: a
// variable the scrub removes resolves the same way for `cd $VAR` as it
// does for the child.
func childEnvMap(env map[string]string, bo *execOptions) map[string]string {
	m := make(map[string]string)
	for _, kv := range buildEnvList(env, bo) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if _, dup := m[k]; !dup {
				m[k] = v
			}
		}
	}
	return m
}

// auditGitPreGate enforces git config/env influence the gate cannot see:
// the per-call env overlay plus the ambient (or replace) environment, and
// the working_dir repository config. An exec-hook env in the overlay or
// the replace set denies outright; ambient keys the scrub removes stay
// silent (the child never receives them, so approval could not restore
// them). Lesser overrides ask: approval can clear a benign case. Empty
// when not git-led or clean. Reads only; the env scrub and the approval
// gate do the rest.
//
// Whole-file selectors, repository redirects (GIT_DIR/GIT_WORK_TREE/
// GIT_COMMON_DIR), and config injectors in the overlay or replace set always
// ask, even for non-git-led builds: a shell trampoline ("sh -c 'git
// ...'") hides the git-led shape from the token scan while the spawned
// git still reads the pointed file or repository. Ambient selectors stay
// on the normal leg below (the engine and the harness legitimately point
// them at owned identity configs); ambient keys the scrub removes stay
// silent there for the same no-restore reason as above.
//
// Shell-form lines are audited per segment: `cd <dir> && git status`
// audits the cd target's repository, not just the process working
// directory - the git segment runs there, so its config and hooks are
// the ones that execute. An unresolvable directory change (`cd -`,
// `pushd`, an unknown $VAR, `~user`) fails closed to approval rather
// than auditing a guessed directory.
func auditGitPreGate(command string, args []string, workingDir string, env map[string]string, bo *execOptions, sb *SandboxConfig) (string, string) {
	// Windows environment names are case-insensitive and git reads
	// folded names, so the pre-gate classification folds there exactly
	// like the ScrubEnv matcher does; Linux stays byte-exact.
	caseInsensitive := runtime.GOOS == "windows"
	seen := map[string]bool{}
	var denies []string
	var askKeys []string
	noteOverlay := func(key, val string) {
		// Deny always fires, even for a hoisted key: config
		// injectors and exec hooks deny on the git-led path while
		// the hoist leg only asks on the non-git-led path. Deny
		// wins downstream, so a double report is harmless.
		if gitPreGateDeniedKey(key, val, caseInsensitive) {
			denies = append(denies, fmt.Sprintf("git environment %s injects command execution", key))
			return
		}
		if seen[key] {
			return
		}
		seen[key] = true
		if gitEnvHasGitPrefix(key, caseInsensitive) && !gitPreGateSkippedKey(key, val, caseInsensitive) {
			askKeys = append(askKeys, key)
		}
	}
	// Ambient keys never deny: ScrubEnv strips exec hooks from the child
	// env, so an ambient GIT_PAGER=cat cannot reach git - denying on it
	// blocked even operator runs with no escape. Stripped keys stay
	// silent too: approval could not restore them (the child never
	// receives them), so asking would prompt on every call with no
	// effect. Only ambient keys that survive the scrub (whole-file
	// selectors, repository redirects, tracers like GIT_TRACE) fall to
	// Ask so a pointed-at file or repository stays visible; operator
	// authority auto-clears the Ask through the normal approval path
	// below.
	noteAmbient := func(key, val string) {
		if seen[key] {
			return
		}
		seen[key] = true
		if isGitScrubbedKey(key, caseInsensitive) || isGitEditorScrubbed(key, val, caseInsensitive) {
			return
		}
		if gitEnvHasGitPrefix(key, caseInsensitive) && !gitPreGateSkippedKey(key, val, caseInsensitive) {
			askKeys = append(askKeys, key)
		}
	}
	// Hoisted config-influence leg: selector, redirect, and injector
	// carriers (gitPreGateSelectorKey) in the overlay or replace set
	// always ask, even when the build is not git-led - a shell trampoline
	// ("sh -c 'git ...'") hides the git-led shape from the token scan
	// while the spawned git still reads the pointed file or repository.
	// Marked seen so the legs below never double-report the same key.
	for k := range env {
		if gitPreGateSelectorKey(k, caseInsensitive) && !seen[k] {
			seen[k] = true
			askKeys = append(askKeys, k)
		}
	}
	if bo.useReplaceEnv {
		for k := range bo.replaceEnv {
			if gitPreGateSelectorKey(k, caseInsensitive) && !seen[k] {
				seen[k] = true
				askKeys = append(askKeys, k)
			}
		}
	}
	// Working-directory base: resolve the override through the sandbox
	// exactly like BuildExecCommand does (ResolveScopedPath), so a raw
	// relative override cannot send the walk somewhere the process never
	// runs. An unresolvable override fails closed downstream; the workdir
	// audit below is skipped rather than walking an unscoped path.
	baseDir := effectiveChildDir(sb, workingDir)
	if sb != nil && sb.Mode != SandboxModeOff && workingDir != "" {
		if resolved, err := sb.ResolveScopedPath(workingDir, false); err == nil {
			baseDir = resolved
		} else {
			baseDir = ""
		}
	}
	// Child-env lookup for $VAR/${VAR}/~ cd-operand expansion: the child
	// environment (overlay over ambient/replace, scrubbed exactly like
	// the spawned process receives it), so a variable the scrub removes
	// resolves the same way for `cd $VAR` as it does for the child.
	// The lookup folds case on Windows, matching the scrub.
	var childEnv map[string]string
	lookupEnv := func(key string) (string, bool) {
		if childEnv == nil {
			childEnv = childEnvMap(env, bo)
		}
		if v, ok := childEnv[key]; ok {
			return v, true
		}
		if caseInsensitive {
			for k, v := range childEnv {
				if strings.EqualFold(k, key) {
					return v, true
				}
			}
		}
		return "", false
	}
	// Git-led segments with their effective (env-unwrapped) argv and the
	// directory each runs in, in segment order: the explicit form is one
	// segment; shell form resolves every segment, replaying
	// `cd <dir>`/`pushd <dir>` inline so `cd <evil> && git status`
	// audits the directory the git segment actually runs in. VAR=
	// assignments and command/builtin/exec wrappers are skipped to the
	// first operand before cd classification; a cd past the first
	// operand cannot be modeled and fails closed. Subshell depth is
	// tracked per segment: a cd inside (...) updates only the
	// subshell-scoped directory (so `(cd /clean) && git status` audits
	// the base directory), while a cd inside {...} persists exactly
	// like a bare cd. A || around a tracked cd leaves the directory
	// uncertain (the cd may have failed or run conditionally) and fails
	// closed too. env(1) -C/--chdir operands steer the utility's
	// directory and are resolved inline like a tracked cd. Assignments
	// preserved through -S splices ride along in gitAssigns for the deny
	// scan below. An unresolvable directory change (`cd -`,
	// `pushd`/`popd`, an unknown $VAR, `~user`, a missing/unresolvable
	// env -C operand) sets cdUnknown/envTrampolineUnknown and fails
	// closed to approval below rather than auditing a guessed directory.
	var gitAssigns [][]string
	var gitDirs []string
	cur := baseDir
	cdUnknown := false
	envTrampolineUnknown := false
	envChdirSeen := false
	if len(args) > 0 {
		argv := append([]string{command}, args...)
		a, dirs, found, unk := scanGitInToks(argv)
		if unk {
			envTrampolineUnknown = true
		}
		// env -C/--chdir steers the utility even when the segment is
		// not git-led (`env -C /hook ls` runs ls in /hook): Ask on
		// the override itself, and fail closed when the layer cannot
		// be parsed. scanGitInToks only reports chdirs alongside a
		// found git utility, so probe every env position here too.
		if cFound, cUnk := envChdirInToks(argv); cUnk {
			envTrampolineUnknown = true
		} else if cFound {
			envChdirSeen = true
		}
		if found {
			gitAssigns = append(gitAssigns, a)
			gitDirs = append(gitDirs, cur)
			if len(dirs) > 0 {
				resolved, ok := resolveEnvChdirs(cur, dirs, lookupEnv)
				if !ok {
					envTrampolineUnknown = true
				} else {
					gitDirs = append(gitDirs, resolved...)
					envChdirSeen = true
				}
			}
		}
	} else {
		depthStack := []string{cur}
		cdSeen := []bool{false}
		for _, sv := range splitShellSegmentsWithSeps(command) {
			seg := sv.text
			opens, closes := countSubshellParens(seg)
			for k := 0; k < opens; k++ {
				depthStack = append(depthStack, depthStack[len(depthStack)-1])
				cdSeen = append(cdSeen, false)
			}
			top := len(depthStack) - 1
			// An unbalanced closer (more ) than open depth) cannot
			// be replayed: the directory is unknown, fail closed
			// rather than auditing the base.
			unbalanced := closes >= len(depthStack)
			pop := func() {
				for k := 0; k < closes && len(depthStack) > 1; k++ {
					depthStack = depthStack[:len(depthStack)-1]
					cdSeen = cdSeen[:len(cdSeen)-1]
				}
			}
			if unbalanced {
				cdUnknown = true
			}
			toks := SplitCommandTokens(seg)
			if len(toks) == 0 {
				pop()
				continue
			}
			// env -C/--chdir on any segment steers that segment's
			// utility even without git: record it here so a
			// non-git-led override still asks below.
			if cFound, cUnk := envChdirInToks(toks); cUnk {
				envTrampolineUnknown = true
			} else if cFound {
				envChdirSeen = true
			}
			// Track `cd`/`pushd` segments (and fail-closed `popd`)
			// so the following git segments audit their real
			// directory. A cd itself never carries git influence.
			// Prefix skips reach the first operand (`VAR=1 cd`,
			// `command cd`); group openers are stripped before
			// classification so "(cd /hook" and "{ cd /hook"
			// track like a bare cd, and a wrapper behind an opener
			// ("(command cd /hook)") is skipped after the strip
			// so it tracks instead of falling through to the
			// beyond-first net. The depth stack scopes
			// subshell cds: a cd inside (...) updates only the
			// subshell-scoped directory (so `(cd /clean) && git`
			// audits the base), while a cd inside {...} (no
			// parens) persists exactly like a bare cd.
			eff := skipCdPrefixes(toks)
			stripped, _ := stripCdGroupOpeners(eff)
			stripped = skipCdPrefixes(stripped)
			if len(stripped) > 0 && isCdCommand(stripped[0]) {
				// A || around the cd leaves the directory
				// uncertain: `cd /hook || git` runs git in
				// the pre-cd directory, and `x || cd /hook`
				// runs the cd conditionally.
				if sv.sep == "||" {
					cdUnknown = true
				}
				next, ok := resolveCdTarget(depthStack[top], stripped, lookupEnv)
				if !ok {
					cdUnknown = true
				} else {
					depthStack[top] = next
					cdSeen[top] = true
				}
				pop()
				continue
			}
			if cdTokenBeyondFirst(eff) {
				cdUnknown = true
			}
			// A || after any tracked cd at this depth or above
			// leaves the directory uncertain: the cd may have
			// failed (`cd /hook || git`) or run conditionally
			// (`x || cd /hook` before this git). Only depths
			// that scope this segment count: a cd confined to
			// a popped subshell never affected the parent, so
			// it must not poison a base-depth git.
			if sv.sep == "||" {
				for d := 0; d <= top && d < len(cdSeen); d++ {
					if cdSeen[d] {
						cdUnknown = true
						break
					}
				}
			}
			a, dirs, found, unk := scanGitInToks(toks)
			if unk {
				envTrampolineUnknown = true
			}
			if !found {
				pop()
				continue
			}
			gitAssigns = append(gitAssigns, a)
			gitDirs = append(gitDirs, depthStack[top])
			if len(dirs) > 0 {
				resolved, ok := resolveEnvChdirs(depthStack[top], dirs, lookupEnv)
				if !ok {
					envTrampolineUnknown = true
				} else {
					gitDirs = append(gitDirs, resolved...)
					envChdirSeen = true
				}
			}
			pop()
		}
		// An unclosed subshell depth cannot be replayed: the shell
		// itself would reject the line, so fail closed rather than
		// auditing the base directory as if the cds applied.
		if len(depthStack) != 1 {
			cdUnknown = true
		}
	}
	// Trampoline-carried assignments inject like the overlay map: an
	// exec-hook assignment (GIT_EXTERNAL_DIFF, GIT_PAGER, GIT_CONFIG_*
	// triples, a non-sentinel GIT_EDITOR) denies outright; any other
	// GIT_* assignment asks. Runs before the strip: the assignments are
	// evaluated as the env overlay the spawned git reads, never silently
	// dropped with the env wrapper.
	for _, assigns := range gitAssigns {
		for _, a := range assigns {
			key, val, _ := strings.Cut(a, "=")
			if gitPreGateDeniedKey(key, val, caseInsensitive) {
				denies = append(denies, fmt.Sprintf("git environment %s injects command execution", key))
				continue
			}
			if gitEnvHasGitPrefix(key, caseInsensitive) && !gitPreGateSkippedKey(key, val, caseInsensitive) && !seen[key] {
				seen[key] = true
				askKeys = append(askKeys, key)
			}
		}
	}
	if len(gitDirs) == 0 {
		// An unparseable env trampoline fails closed to approval even
		// without a visible git utility: the layer the audit cannot
		// parse could hide one. The collected Ask keys and the -C
		// override ride along so no influence is dropped from the
		// prompt.
		if envTrampolineUnknown {
			base := joinGitAskWithDir(askKeys, envChdirSeen)
			if base == "" {
				return "", "env trampoline requires user approval"
			}
			return "", base + "; env trampoline requires user approval"
		}
		return "", joinGitAskWithDir(askKeys, envChdirSeen)
	}
	for k, v := range env {
		noteOverlay(k, v)
	}
	if bo.useReplaceEnv {
		for k, v := range bo.replaceEnv {
			noteOverlay(k, v)
		}
	} else {
		for _, kv := range os.Environ() {
			if k, v, ok := strings.Cut(kv, "="); ok {
				noteAmbient(k, v)
			}
		}
	}
	if len(denies) > 0 {
		sort.Strings(denies)
		return denies[0], ""
	}
	// Per-segment directories: each git segment audits the repository it
	// runs in (directory changes were replayed inline during the scan
	// above, starting from the sandbox-resolved base). An unresolvable
	// directory change (`cd -`, `pushd`/`popd`, an unknown variable)
	// fails closed to approval below rather than auditing a guess.
	seenDir := map[string]bool{}
	for _, dir := range gitDirs {
		if dir == "" || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		if reason := gitWorkdirConfigAskReason(dir); reason != "" {
			return "", reason
		}
		if reason := gitWorkdirHooksAskReason(dir); reason != "" {
			return "", reason
		}
	}
	if cdUnknown && len(gitDirs) > 0 {
		// Preserve the collected Ask keys and the -C override
		// alongside the directory-uncertainty Ask: the human
		// reviews the full influence, and an already-asking gate
		// keeps its reason.
		base := joinGitAskWithDir(askKeys, envChdirSeen)
		if base == "" {
			return "", "cd target requires user approval (unresolvable directory change)"
		}
		return "", base + "; cd target requires user approval (unresolvable directory change)"
	}
	if envTrampolineUnknown {
		base := joinGitAskWithDir(askKeys, envChdirSeen)
		if base == "" {
			return "", "env trampoline requires user approval"
		}
		return "", base + "; env trampoline requires user approval"
	}
	return "", joinGitAskWithDir(askKeys, envChdirSeen)
}

// gitPreGateSelectorKey reports whether key is a whole-file config
// selector (GIT_CONFIG_GLOBAL/SYSTEM, GIT_CONFIG), a repository
// redirect (GIT_DIR/WORK_TREE/COMMON_DIR), or an arbitrary config
// injector (any GIT_CONFIG_* name): the pointed file - or the pointed
// repository - re-enters git config parsing with full exec capability,
// so an overlay/replace carrier always asks - even for non-git-led
// builds, where a shell trampoline ("sh -c 'git ...'") hides the git-led
// shape. Windows matches case-insensitively; Linux matches byte-exact.
func gitPreGateSelectorKey(key string, caseInsensitive bool) bool {
	eq := func(a, b string) bool {
		if caseInsensitive {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if eq(key, "GIT_CONFIG_GLOBAL") || eq(key, "GIT_CONFIG_SYSTEM") || eq(key, "GIT_CONFIG") {
		return true
	}
	if eq(key, "GIT_DIR") || eq(key, "GIT_WORK_TREE") || eq(key, "GIT_COMMON_DIR") {
		return true
	}
	hasPrefix := func(s, p string) bool {
		if caseInsensitive {
			return len(s) >= len(p) && strings.EqualFold(s[:len(p)], p)
		}
		return strings.HasPrefix(s, p)
	}
	return hasPrefix(key, "GIT_CONFIG_")
}

// joinGitAskKeys renders the deterministic Ask reason for the collected
// offending keys: sorted and joined so map iteration order never leaks
// into the approval prompt or audit trail. Empty when clean.
func joinGitAskKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return fmt.Sprintf("git environment overrides require user approval: %s", strings.Join(sorted, ", "))
}

// joinGitAskWithDir renders the deterministic Ask reason for the
// collected offending keys (see joinGitAskKeys), plus the env -C
// directory-override Ask when an env layer steered the utility: like a
// git -C override, an env -C always asks, even into a clean repository,
// because the directory itself is config influence the gate cannot see.
func joinGitAskWithDir(keys []string, dirOverride bool) string {
	base := joinGitAskKeys(keys)
	if !dirOverride {
		return base
	}
	const dirAsk = "env -C directory override requires user approval"
	if base == "" {
		return dirAsk
	}
	return base + "; " + dirAsk
}

// gitWorkdirConfigAskReason audits the repository config visible from dir:
// walking up to 32 levels for .git/config (following a .git gitdir file
// for worktrees) plus its config.worktree sibling. When either config
// sets an execution-capable key (pager, editor, fsmonitor, sshCommand,
// askpass, external, difffilter, hooks path, tool cmd, filter/diff/merge
// drivers, textconv, a ! shell alias, a ! submodule update, an
// include/includeIf path, an ext:: URL or insteadOf rewrite, a
// protocol.ext.allow enablement, or an unsafe credential helper), the
// build asks: the gate classified a low-risk "git status" it cannot see
// is influenced. Presence alone (no exec keys) stays green so ordinary
// repositories keep working. No repository found within the walk cap
// stays green. A config that cannot be read stays green too: git itself
// errors on an unreadable repo, so there is nothing to execute silently.
// A config past the 64KiB audit cap asks: the audit cannot clear hooks
// it cannot read, so an oversize file fails closed to approval.
func gitWorkdirConfigAskReason(dir string) string {
	if dir == "" {
		return ""
	}
	cur := filepath.Clean(dir)
	for depth := 0; depth < 32; depth++ {
		dotGit := filepath.Join(cur, ".git")
		config := ""
		if fi, err := os.Lstat(dotGit); err == nil {
			target := fi
			if fi.Mode()&os.ModeSymlink != 0 {
				// A symlinked .git still resolves to a repo; follow
				// one level like git does. Unresolvable links
				// behave like a missing .git (walk up).
				if resolved, serr := os.Stat(dotGit); serr == nil {
					target = resolved
				} else {
					target = nil
				}
			}
			switch {
			case target == nil:
				// Unresolvable: treat as no .git here, keep walking.
			case target.IsDir():
				config = filepath.Join(dotGit, "config")
			case target.Mode().IsRegular():
				// Worktree gitdir pointer: ".git" file with
				// "gitdir: <path>" (tolerant of a missing space).
				// Capped: a FIFO or an oversize pointer must not
				// block the audit or escape it.
				data, tooLarge, err := readCappedAuditFile(dotGit, gitAuditCap)
				if tooLarge {
					return fmt.Sprintf("repository config pointer %s is too large to audit and requires user approval", dotGit)
				}
				if err != nil {
					return ""
				}
				line := strings.TrimSpace(string(data))
				rest, ok := strings.CutPrefix(line, "gitdir:")
				if !ok {
					return ""
				}
				gd := strings.TrimSpace(rest)
				if gd == "" {
					return ""
				}
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(cur, gd)
				}
				var bad bool
				if config, bad = worktreeGitConfigPath(gd); bad {
					return fmt.Sprintf("repository config %s is too large to audit and requires user approval", config)
				}
			default:
				// FIFO, socket, device, or anything else git
				// itself could not use as a .git entry: not a
				// repository config, keep walking without opening
				// (opening a FIFO would block the audit).
			}
		}
		if config != "" {
			if reason := auditGitConfigFile(config); reason != "" {
				return reason
			}
			// Worktree-scoped overrides live in the config.worktree
			// sibling (same directory as config): it re-enters git
			// config parsing with full exec capability, so it is
			// audited with the same capped read and hook set. A
			// missing sibling stays green (git runs without it).
			if reason := auditGitConfigWorktreeFile(filepath.Join(filepath.Dir(config), "config.worktree")); reason != "" {
				return reason
			}
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	// No .git found within the walk cap: no repository config in scope.
	return ""
}

// auditGitConfigFile audits one git config file with a capped read: an
// oversize file asks (too large to audit), an unreadable file stays
// green (git itself errors on it), and an exec-hook text asks naming
// the file.
func auditGitConfigFile(config string) string {
	data, tooLarge, err := readCappedAuditFile(config, gitAuditCap)
	if err != nil {
		return ""
	}
	if tooLarge {
		return fmt.Sprintf("repository config %s is too large to audit and requires user approval", config)
	}
	if gitConfigTextHasExecHook(string(data)) {
		return fmt.Sprintf("repository config %s sets command execution and requires user approval", config)
	}
	return ""
}

// auditGitConfigWorktreeFile audits the config.worktree sibling: same
// capped read and hook set as the main config, but a missing file stays
// green (git runs without it) while an oversize or exec-hook sibling
// asks exactly like the main file.
func auditGitConfigWorktreeFile(path string) string {
	data, tooLarge, err := readCappedAuditFile(path, gitAuditCap)
	if err != nil {
		return ""
	}
	if tooLarge {
		return fmt.Sprintf("repository config %s is too large to audit and requires user approval", path)
	}
	if gitConfigTextHasExecHook(string(data)) {
		return fmt.Sprintf("repository config %s sets command execution and requires user approval", path)
	}
	return ""
}

// gitWorkdirHooksAskReason audits the repository hooks visible from dir:
// walking up to 32 levels for a .git directory (following a .git gitdir
// file for worktrees, like the config audit). When the hooks directory
// holds an executable file that is not a stock ".sample" template, the
// build asks: git runs that hook on the matching event, so a plain
// "git status" (or commit, checkout, ...) the gate classified low-risk
// executes it. No hooks directory, no executable non-sample hook, or an
// unreadable hooks directory stays green. A hooks directory that cannot
// be listed stays green too: git itself cannot run hooks it cannot read.
func gitWorkdirHooksAskReason(dir string) string {
	if dir == "" {
		return ""
	}
	cur := filepath.Clean(dir)
	for depth := 0; depth < 32; depth++ {
		dotGit := filepath.Join(cur, ".git")
		gitDir := ""
		if fi, err := os.Lstat(dotGit); err == nil {
			target := fi
			if fi.Mode()&os.ModeSymlink != 0 {
				if resolved, serr := os.Stat(dotGit); serr == nil {
					target = resolved
				} else {
					target = nil
				}
			}
			switch {
			case target == nil:
				// Unresolvable: no hooks here, keep walking.
			case target.IsDir():
				gitDir = dotGit
			case target.Mode().IsRegular():
				data, tooLarge, err := readCappedAuditFile(dotGit, gitAuditCap)
				if tooLarge || err != nil {
					// The config leg already fails closed on an
					// unauditable pointer; the hooks leg cannot
					// resolve a gitdir from it, so keep walking
					// without blocking.
					break
				}
				line := strings.TrimSpace(string(data))
				rest, ok := strings.CutPrefix(line, "gitdir:")
				if !ok {
					break
				}
				gd := strings.TrimSpace(rest)
				if gd == "" {
					break
				}
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(cur, gd)
				}
				gitDir = gd
			default:
				// FIFO, socket, device: git itself could not use
				// this .git entry, keep walking without opening.
			}
		}
		if gitDir != "" {
			if reason := auditGitHooksDir(filepath.Join(gitDir, "hooks")); reason != "" {
				return reason
			}
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	return ""
}

// auditGitHooksDir reports an Ask reason when hooksDir holds an
// executable hook file: any regular-file entry (symlinks followed one
// level, like the config audit) whose name does not end in ".sample"
// and carries any execute bit. Stock sample templates, non-executable
// files, and directories never fire. An unreadable directory stays
// green: git cannot run hooks it cannot read either.
func auditGitHooksDir(hooksDir string) string {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".sample") {
			continue
		}
		full := filepath.Join(hooksDir, name)
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if resolved, serr := os.Stat(full); serr == nil {
				fi = resolved
			} else {
				continue
			}
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if fi.Mode().Perm()&0o111 != 0 {
			return fmt.Sprintf("repository hook %s is executable and requires user approval", full)
		}
	}
	return ""
}

// gitAuditCap bounds every on-disk read in the repository-config audit
// (the .git gitdir pointer, the worktree commondir file, and the config
// itself): at most cap+1 bytes are read, so an oversize file is reported
// without loading it and a FIFO can never block the audit (non-regular
// files are never opened).
const gitAuditCap = 64 * 1024

// readCappedAuditFile reads at most limit+1 bytes from path for the
// config audit. The path must be a regular file (symlinks are followed
// one level to their target, exactly like the .git discovery above);
// anything else (FIFO, socket, device) reports tooLarge so the caller
// fails closed to approval instead of blocking on open. A short read
// past the limit also reports tooLarge. A missing/unreadable path
// returns err and the caller stays green, matching git itself erroring
// on an unreadable repo.
func readCappedAuditFile(path string, limit int) (data []byte, tooLarge bool, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		fi, err = os.Stat(path)
		if err != nil {
			return nil, false, err
		}
	}
	if !fi.Mode().IsRegular() {
		return nil, true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

// worktreeGitConfigPath resolves the config file visible from a gitdir
// pointer target. A linked worktree gitdir
// (<main>/.git/worktrees/<name>) holds no config of its own; the shared
// config lives in the common dir named by its commondir file (usually
// "../.."). When commondir is missing or empty, fall back to stripping a
// trailing /worktrees/<name>, then to <gitdir>/config (covers a normal
// .git dir and the --separate-git-dir layout). The commondir read is
// capped like every other audit read: an unauditable commondir (FIFO,
// oversize) reports bad so the caller fails closed instead of blocking
// or guessing a config path it cannot verify.
func worktreeGitConfigPath(gd string) (string, bool) {
	gd = filepath.Clean(gd)
	if data, tooLarge, err := readCappedAuditFile(filepath.Join(gd, "commondir"), gitAuditCap); err == nil {
		if tooLarge {
			return filepath.Join(gd, "config"), true
		}
		if cd := strings.TrimSpace(string(data)); cd != "" {
			if !filepath.IsAbs(cd) {
				cd = filepath.Join(gd, cd)
			}
			return filepath.Join(filepath.Clean(cd), "config"), false
		}
	}
	if strings.EqualFold(filepath.Base(filepath.Dir(gd)), "worktrees") {
		return filepath.Join(filepath.Dir(filepath.Dir(gd)), "config"), false
	}
	return filepath.Join(gd, "config"), false
}

// gitConfigTextHasExecHook reports whether a git config text sets an
// execution-capable key: bare ini key names (any section) whose values
// run commands, a ! shell alias under [alias] (quote-tolerant: git
// strips one layer of quotes before checking for the ! shell marker),
// a merge driver under [merge], a ! update command under [submodule],
// any key under [pager] (per-command pager commands), the pack objects
// hook under [uploadpack] (uploadpack.packObjectsHook runs on
// clone/fetch; [pack] has no such key and stays green), remote
// uploadpack/receivepack/vcs commands under [remote "name"], the gpg
// ssh default key command under [gpg "ssh"], any include/includeIf
// re-enters git config parsing, so its content is untrusted on this
// on-disk leg), a credential helper outside the safe set (any ! shell
// helper, any absolute or relative path helper, and any bare helper
// name outside the stock set), gpg.program, the gpg ssh
// defaultKeyCommand, remote uploadpack/receivepack/vcs, core.gitProxy,
// core.alternateRefsCommand, gc.recentObjectsHook (git 2.55),
// any url value using the ext:: transport (clone/fetch executes it),
// any insteadOf rewrite pointing at ext:: (redirects fetches into
// command execution), and a protocol.ext.allow enablement
// (always/user: the repo opts into ext:: execution; never stays
// green). Section headers normalize to their base section
// ("[merge \"x\"]", "[merge.x]", and "[merge]" all compare as
// "merge") so legacy dotted headers cannot hide a driver; the protocol
// subsection ("ext" in `[protocol "ext"]`) is tracked separately so
// unrelated protocol allowments never fire. The argv/env deny leg is
// separate and unaffected.
//
// Scope decision (lfs): stock filter blocks ('[filter "lfs"]' with
// 'clean/smudge = git-lfs ...' and 'process = git-lfs
// filter-process') ask like any other filter driver - there is
// deliberately no trusted-filter-binary allowlist, because the filter
// path still executes a program on checkout/smudge and an allowlist
// would have to resolve and verify that binary on every build.
// Approval clears a benign stock-lfs repo after inspection.
func gitConfigTextHasExecHook(text string) bool {
	section := ""
	subsection := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inner := strings.TrimSpace(strings.Trim(trimmed, "[]"))
			// Subsection: a quoted name (`[protocol "ext"]`) or the
			// remainder of a legacy dotted header (`[protocol.ext]`).
			rest := ""
			if idx := strings.IndexAny(inner, " \t\""); idx >= 0 {
				rest = inner[idx:]
				inner = inner[:idx]
			}
			// Legacy dotted headers ("[merge.x]") carry the base
			// section before the dot; quoted subsections were
			// cut above, so only the base remains.
			baseSection := strings.ToLower(inner)
			if idx := strings.IndexByte(baseSection, '.'); idx >= 0 {
				rest = baseSection[idx+1:] + " " + rest
				baseSection = baseSection[:idx]
			}
			section = baseSection
			subsection = strings.ToLower(strings.Trim(strings.TrimSpace(rest), "\""))
			continue
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		// Any key under [pager] names a command run as a pager
		// ("[pager] log = diff-highlight | less").
		if section == "pager" {
			return true
		}
		switch key {
		case "pager", "editor", "fsmonitor", "sshcommand", "askpass",
			"external", "difffilter", "hookspath", "cmd",
			"clean", "smudge", "process", "command", "textconv":
			return true
		case "driver":
			// [merge "name"] driver runs a merge driver command.
			// No other section uses a bare "driver" exec key, so
			// the match stays narrow to [merge].
			if section == "merge" {
				return true
			}
		case "update":
			// [submodule "name"] update = !cmd runs a custom
			// command on update; plain strategies (checkout,
			// rebase, merge, none) stay green.
			if section == "submodule" {
				unquoted := strings.Trim(val, "\"'")
				if strings.HasPrefix(strings.TrimSpace(unquoted), "!") {
					return true
				}
			}
		case "path":
			// Any included file re-enters git config parsing with
			// full exec capability, so an include path alone asks.
			if section == "include" || section == "includeif" {
				return true
			}
		case "program":
			if section == "gpg" {
				return true
			}
		case "gitproxy":
			if section == "core" {
				return true
			}
		case "alternaterefscommand":
			// [core] alternateRefsCommand runs a command to list
			// alternate refs on fetch/clone (lowercased key compare).
			// No other section uses this key, so the match stays
			// narrow to [core].
			if section == "core" {
				return true
			}
		case "recentobjectshook":
			// [gc] recentObjectsHook runs a hook command during gc
			// (git 2.55; lowercased key compare). No other section
			// uses this key, so the match stays narrow to [gc].
			if section == "gc" {
				return true
			}
		case "packobjectshook":
			// [uploadpack] packObjectsHook runs a hook command during
			// pack-objects on clone/fetch (lowercased key compare).
			// [pack] has no such key, so it stays green.
			if section == "uploadpack" {
				return true
			}
		case "uploadpack", "receivepack", "vcs":
			// [remote "name"] uploadpack/receivepack/vcs run remote-side
			// commands; other sections with these keys stay green.
			if section == "remote" {
				return true
			}
		case "defaultkeycommand":
			// [gpg "ssh"] defaultKeyCommand runs a command to select
			// the signing key.
			if section == "gpg" {
				return true
			}
		case "helper":
			if section == "credential" && !isSafeCredentialHelper(val) {
				return true
			}
		case "url":
			// Any url using the ext:: transport executes a command
			// on clone/fetch (remote, submodule, or url-rewrite
			// base alike); ordinary https/ssh/file URLs stay green.
			if gitURLUsesExtTransport(val) {
				return true
			}
		case "insteadof":
			// An insteadOf rewrite pointing at ext:: redirects
			// fetches of the base into command execution.
			// Quote-tolerant like the alias/submodule legs.
			if gitURLUsesExtTransport(val) {
				return true
			}
		case "allow":
			// `[protocol "ext"] allow = always|user` opts the repo
			// into ext:: command execution; `never` stays green, as
			// do allowments for other protocols. The bare
			// `[protocol] allow = always|user` enables every
			// transport including ext, so it asks too.
			if section == "protocol" && (subsection == "" || subsection == "ext") {
				unquoted := strings.Trim(val, "\"'")
				switch strings.ToLower(strings.TrimSpace(unquoted)) {
				case "always", "user":
					return true
				}
			}
		}
		if section == "alias" {
			// Quote-tolerant like the submodule/credential legs:
			// git strips one layer of quotes, so '"!x"' still
			// runs a shell command.
			unquoted := strings.Trim(val, "\"'")
			if strings.HasPrefix(strings.TrimSpace(unquoted), "!") {
				return true
			}
		}
	}
	return false
}

// gitSafeCredentialHelpers are the stock credential-helper programs that
// stay green: bare names (no path, no shell escape) from the platform
// set. Everything else - a ! shell helper, an absolute or relative
// path, or a bare name outside this set - asks: the helper runs on
// credential fill, so an unknown program is command execution.
var gitSafeCredentialHelpers = map[string]bool{
	"store": true, "cache": true,
	"osxkeychain": true, "manager": true, "manager-core": true,
	"wincred": true, "libsecret": true,
}

// isSafeCredentialHelper reports whether a credential.helper value is a
// stock safe helper: quote-tolerant (git strips one layer of quotes),
// empty (clears helpers), or a bare safe name with trailing arguments
// ("cache --timeout=3600"). A ! shell marker, an absolute path, a
// relative path (./x, ../x, sub/dir), or a bare unknown name is unsafe.
func isSafeCredentialHelper(val string) bool {
	unquoted := strings.Trim(val, "\"'")
	trimmed := strings.TrimSpace(unquoted)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "!") {
		return false
	}
	if filepath.IsAbs(trimmed) {
		return false
	}
	first := trimmed
	if idx := strings.IndexAny(first, " \t"); idx >= 0 {
		first = first[:idx]
	}
	if first == "" {
		return true
	}
	if strings.ContainsAny(first, "/\\") || strings.HasPrefix(first, ".") || strings.HasPrefix(first, "~") {
		return false
	}
	prog := strings.ToLower(first)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		prog = strings.TrimSuffix(prog, ext)
	}
	return gitSafeCredentialHelpers[prog]
}

// gitURLUsesExtTransport reports whether a config url/insteadOf value
// selects the ext:: transport (quote-tolerant, scheme case-insensitive):
// git executes the remainder as a command on clone/fetch.
func gitURLUsesExtTransport(val string) bool {
	unquoted := strings.Trim(val, "\"'")
	trimmed := strings.TrimSpace(unquoted)
	return strings.HasPrefix(strings.ToLower(trimmed), "ext::")
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
// label (FG-003). A nil label is fail-closed when a v2 policy is installed
// (evaluated as narrowest); with no installed policy the flow lookup reports
// ok=false and the audit keeps v1 behavior, so nil is inert there. Callers
// with a request context (buildExecCommand) pass flowLabelFromCtx(ctx);
// direct callers keep the StartLabel wrapper above.
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
	// SE-2: a quoted -S payload hides absolute operands inside one
	// multi-word token (`env -S 'cat /home/u/.ssh/id_rsa'` tokenizes
	// the payload as a single "cat /home/..." token, which is neither
	// absolute nor a denied relative). The payload IS an env option
	// stream, so unwrap it with the same spliceEnvLayer/unwrapEnvLayers
	// the workdir audit uses and confine each spliced operand on its
	// own. An unparseable layer fails closed (deny): the audit cannot
	// clear a payload it cannot parse, so it never continues silent.
	// Quoted free-text operands elsewhere (commit messages,
	// grep patterns, echo text) are never re-split, so benign prose
	// with spaces or slashes stays green. Positions inside a chain an
	// earlier unwrap already consumed are skipped (envChainCovered), and
	// the scan stops once an unwrapped stream settles on a plain utility
	// (later env tokens are that utility's operands, not trampolines). A
	// work budget rejects adversarial alternating wrapper/env chains that
	// never settle.
	coveredUntil := 0
	scanned := 0
	for i, tok := range tokens {
		if tok == "" || strings.HasPrefix(tok, "-") || isEnvAssignToken(tok) {
			continue
		}
		if gitLedBase(tok) != "env" {
			continue
		}
		if i < coveredUntil {
			continue
		}
		if scanned >= maxEnvPositionScans {
			return fmt.Errorf("command nests env trampolines too deeply to audit and is rejected")
		}
		scanned++
		if end := i + 1 + envChainCovered(tokens[i+1:]); end > coveredUntil {
			coveredUntil = end
		}
		eff, _, unk := unwrapEnvLayers(tokens[i:])
		if unk {
			return fmt.Errorf("command references env -S payload which could not be parsed and is rejected")
		}
		for _, e := range eff {
			if err := auditPathToken(sb, e, base); err != nil {
				util.DebugWarn("system_exec_path_rejected", "command", command, "args", args, "error", err.Error())
				return err
			}
		}
		if envChainSettled(eff) {
			break
		}
	}
	for _, tok := range tokens {
		if err := auditPathToken(sb, tok, base); err != nil {
			util.DebugWarn("system_exec_path_rejected", "command", command, "args", args, "error", err.Error())
			return err
		}
	}
	return nil
}

// stripAuditRedirectToken removes leading shell redirect operators from a
// command-line token so an attached redirect target is audited as the path
// the shell actually opens: >, >>, <, <<, <>, &>, >&, and FD-prefixed
// forms (2>, 1>>, 0<, 2>&1). A bare FD number without an operator (e.g.
// "2fa") is a filename, never a redirect, and is left intact; a lone
// operator or FD-duplication residue (">", "2>&1" -> "1") is not a path
// and passes through to the normal relative-operand handling below.
// Only leading operators are stripped, so prose carrying ">" inside
// ("fix > bug") is unchanged and stays green.
func stripAuditRedirectToken(p string) string {
	for {
		orig := p
		if strings.HasPrefix(p, "&>") || strings.HasPrefix(p, ">&") {
			p = p[2:]
			continue
		}
		i := 0
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		rest := p[i:]
		switch {
		case strings.HasPrefix(rest, ">>") || strings.HasPrefix(rest, "<<") || strings.HasPrefix(rest, "<>") ||
			strings.HasPrefix(rest, ">&") || strings.HasPrefix(rest, "&>"):
			p = rest[2:]
		case strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "<"):
			p = rest[1:]
		}
		if p == orig {
			return p
		}
	}
}

// unescapeAuditToken interprets shell backslash escapes the way sh will
// before opening the path: `\X` is the literal character X, so `\/deny`
// opens /deny exactly like an unescaped spelling. A trailing lone
// backslash carries no character and is dropped. Applied before the
// redirect strip so an escaped operator (`\>/deny`) still resolves to
// its target; fail-closed (deny) when the unescaped form names a denied
// path, even where sh would have treated the escape as a literal.
func unescapeAuditToken(p string) string {
	if !strings.Contains(p, "\\") {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
			b.WriteByte(p[i])
			continue
		}
		if p[i] == '\\' {
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// splitAuditRedirectTargets splits a command-line token on unescaped shell
// redirect/pipe/separator operators so attached targets (`cat</deny`,
// `cat>/deny`, `cat&>/deny`, `cat0</deny`) are audited as the paths the
// shell actually opens. Escape-aware: a backslash escapes the next byte,
// so `\>` is literal and never splits. Returns the non-empty trimmed
// pieces; nil when no operator is present (fast path). Only `>`, `<`,
// `&`, `|`, `;` split - `(`/`)` are left intact because SplitCommandTokens
// already stripped quotes and substitution bodies are audited elsewhere.
// Over-approximate fail-closed: quoted prose (`fix>bug`) splits too, but
// its pieces are bare words that stay green, while a quoted deny target
// denies - the safe direction. Bare FD numbers (`2`), duplication
// residue (`1` from `2>&1`), and lone operators pass through the normal
// relative-operand handling below and stay green.
func splitAuditRedirectTargets(tok string) []string {
	hasOp := false
	for i := 0; i < len(tok); i++ {
		if tok[i] == '\\' {
			i++
			continue
		}
		if tok[i] == '>' || tok[i] == '<' || tok[i] == '&' || tok[i] == '|' || tok[i] == ';' {
			hasOp = true
			break
		}
	}
	if !hasOp {
		return nil
	}
	var out []string
	var cur strings.Builder
	for i := 0; i < len(tok); {
		c := tok[i]
		if c == '\\' && i+1 < len(tok) {
			cur.WriteByte(c)
			cur.WriteByte(tok[i+1])
			i += 2
			continue
		}
		if c == '>' || c == '<' || c == '&' || c == '|' || c == ';' {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			for i < len(tok) && (tok[i] == '>' || tok[i] == '<' || tok[i] == '&' || tok[i] == '|' || tok[i] == ';') {
				i++
			}
			continue
		}
		cur.WriteByte(c)
		i++
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	if len(out) <= 1 {
		return nil
	}
	return out
}

// stripAuditRedirectEscape resolves a token to the path the shell opens
// through attached redirects and backslash escapes: escapes first (so
// `\/deny` and `\>/deny` surface their target), then leading redirect/FD
// operators (so `</deny`, `0</deny`, `>/deny` surface theirs). Runs to a
// fixpoint (capped): stripping can expose an escape and unescaping can
// expose an operator. Interior operators are handled by
// splitAuditRedirectTargets in auditPathToken (so `cat</deny` surfaces
// `/deny`); only leading operators are stripped here so quoted prose
// ("fix > bug") and flag text ("--co=gzip") audit exactly as before.
func stripAuditRedirectEscape(p string) string {
	for i := 0; i < 4; i++ {
		next := stripAuditRedirectToken(unescapeAuditToken(p))
		if next == p {
			return p
		}
		p = next
	}
	return p
}

// auditPathToken checks a single command-line token. Absolute paths must be
// under a read root or a trusted system dir, and never under a deny root.
// Relative path-like operands are resolved against relativeBase and audited
// against deny rules AND read-root confinement, so "../"-style escapes
// cannot leave the sandbox; everything else passes, since bare words cannot
// leave the confined working directory. Glob metacharacters are
// pre-expanded because sh resolves them only after this audit runs.
func auditPathToken(sb *SandboxConfig, tok, relativeBase string) error {
	return auditPathTokenScoped(sb, tok, relativeBase, true)
}

// auditPathTokenScoped is auditPathToken with the read-root scope check made
// optional. requireScope is false only for prose pieces split out of a
// whitespace-carrying token (`git commit -m "fix a && /opt + echo"`): those
// pieces are free text, so they still get the deny-root/denied-basename
// checks (fail-closed) but are not required to resolve under a read root,
// which would deny an ordinary commit message that happens to contain a
// slash-prefixed word. A whitespace-free token is a real attached-redirect
// shape (`cat</deny`, `cat>/deny`) and keeps full path auditing.
func auditPathTokenScoped(sb *SandboxConfig, tok, relativeBase string, requireScope bool) error {
	// Attached redirect targets (`cat</deny`, `cat>/deny`, `cat&>/deny`):
	// the shell splits on the operator even without whitespace, so audit
	// each piece as the path the shell opens. A whitespace-carrying token
	// is quoted free text (or a command clause), so its pieces are prose:
	// deny-root/basename checks only, never the read-root confinement
	// (see auditPathTokenScoped). Recursion terminates: pieces contain no
	// operators, so the recursive call takes the single-path fast path
	// below. SE-2 payloads ride the same helper because the SE-2 leg
	// audits spliced operands through this function.
	if targets := splitAuditRedirectTargets(strings.TrimSpace(tok)); targets != nil {
		prose := strings.ContainsAny(tok, " \t\r\n")
		for _, t := range targets {
			if t == tok {
				continue
			}
			if err := auditPathTokenScoped(sb, t, relativeBase, requireScope && !prose); err != nil {
				return err
			}
		}
		return nil
	}
	p := stripAuditRedirectEscape(strings.TrimSpace(tok))
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
		if err := auditCandidatePaths(sb, resolveWithSymlinks(resolved), tok, requireScope); err != nil {
			return err
		}
		// A pattern like "*/key" or "secrets/*.env" expands in the shell
		// after this audit; check every concrete match now so metacharacters
		// cannot mask denied targets behind their literal form.
		if strings.ContainsAny(expanded, "*?[") {
			return auditGlobExpansions(sb, resolved, tok, requireScope)
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
		if err := auditGlobExpansions(sb, expanded, tok, requireScope); err != nil {
			return err
		}
	}
	if !requireScope {
		// Prose piece: deny-root/basename checks above are authoritative;
		// the read-root confinement does not apply to free text.
		return nil
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
// concrete match. requireScope keeps the read-root confinement for real
// path operands and drops it for prose pieces (see auditPathTokenScoped).
// No-match patterns pass through: the spawned shell handles them exactly as
// it would have without this audit.
func auditGlobExpansions(sb *SandboxConfig, pattern, tok string, requireScope bool) error {
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
		if err := auditCandidatePaths(sb, resolveWithSymlinks(m), tok, requireScope); err != nil {
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
