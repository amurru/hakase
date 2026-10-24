// pip_install.go - gated auto-pip installs (S18) via the shared choke (S22).
//
// A ModuleNotFoundError naming a missing package used to trigger a raw
// pip install straight from model-controlled stdout, with no allowlist,
// no approval, and progress echoed to the side pane. S18 makes pip a
// gated, allowlisted, quiet action:
//
//   - name shape: first .-segment of the captured module, validated by
//     pipNameRe; anything else (flags, URLs, whitespace) refuses, no exec.
//   - allowlist: PipAllowlist on the sandbox config; empty means deny-all.
//   - approval: every install raises a human approval Ask (medium risk),
//     even with AllowPipInstall=true. AllowPipInstall=false/nil denies
//     without asking (preserves pipAllowed fail-closed).
//   - quiet: no side-pane progress; audit lines stay, with the package
//     name hashed (never raw).
//   - single retry: at most one install per tool call (structural: no loop,
//     and runPythonWithAutoPip never re-enters the installer on the retry
//     output).
//
// S22: the install itself runs through runPipInstallViaChoke (the shared
// BuildExecCommand choke with the pip bwrap profile), never raw exec.
package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// pipModuleRe captures the missing module from interpreter stderr.
var pipModuleRe = regexp.MustCompile(`ModuleNotFoundError: No module named '([^']+)'`)

// pipNameRe is the package-name shape (S18 contract): alphanumerics plus
// inner . _ -, bounded length. Flags (--index-url=x), URLs, paths and
// whitespace never match.
var pipNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// extractMissingModule parses a missing package name from interpreter
// output. It takes the first .-segment (bs4.element -> bs4) and refuses
// malformed names with no exec.
func extractMissingModule(stdout string) (string, bool) {
	if !strings.Contains(stdout, "ModuleNotFoundError: No module named") {
		return "", false
	}
	m := pipModuleRe.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return "", false
	}
	name := strings.Split(m[1], ".")[0]
	if !pipNameRe.MatchString(name) {
		return "", false
	}
	return name, true
}

// pipPackageAllowed reports whether name is allowlisted (S18): the list
// lives on the sandbox config, empty denies all, matching is exact
// lowercase with no globs. S27: allowlist entries may carry an exact pin
// (name==version); matching compares base names so a pinned entry still
// authorizes its package (the pin itself is enforced by the strict gate).
func pipPackageAllowed(sb *sandbox.SandboxConfig, name string) bool {
	if sb == nil || len(sb.PipAllowlist) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, a := range sb.PipAllowlist {
		base := a
		if i := strings.Index(a, "=="); i >= 0 {
			base = a[:i]
		}
		if strings.ToLower(strings.TrimSpace(base)) == want {
			return true
		}
	}
	return false
}

// pipAuditCommand renders the audit command for a pip decision with the
// package name hashed (audit carries hashes only, never raw names).
func pipAuditCommand(name string) string {
	return "pip install hash:" + permissions.ShortHash(name)
}

// agentSandboxConfig returns the active sandbox config, or nil when unset
// (fail-closed downstream).
func agentSandboxConfig() *sandbox.SandboxConfig {
	if deps == nil {
		return nil
	}
	return deps.SandboxConfig
}

// agentSandboxModeName returns the sandbox mode name for audit lines.
func agentSandboxModeName(sb *sandbox.SandboxConfig) string {
	if sb == nil {
		return "off"
	}
	return string(sb.Mode)
}

// pipEnvMap renders the scrubbed S20 child env as the overlay map the
// choke builder takes.
func pipEnvMap(pyBin string) map[string]string {
	m := make(map[string]string)
	for _, kv := range buildPythonChildEnv(pyBin) {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// pipBinForVenv resolves the .venv pip binary for the platform.
//
// Deprecated: H1 single-root prefers pipBinForPyBin (pyBin-anchored,
// absolute). Kept for tests that stub the venv layout; production
// gateAndInstallPip threads the interpreter's own venv.
func pipBinForVenv() string {
	if runtime.GOOS == "windows" {
		return filepath.Join("./.venv", "Scripts", "pip.exe")
	}
	return filepath.Join("./.venv", "bin", "pip")
}

// countStrictHashes counts --hash legs in strict argv for the approval
// prompt (M6: the human approves the exact pinned + hashed install).
func countStrictHashes(args []string) int {
	n := 0
	for _, a := range args {
		if strings.HasPrefix(a, "--hash=") {
			n++
		}
	}
	return n
}

// gateAndInstallPip runs the S18 gate (config -> allowlist -> human
// approval) and, when every leg passes, the S22 choke install. Any denial
// or install failure returns an error and the caller keeps the original
// interpreter output unchanged (no retry).
func gateAndInstallPip(ctx context.Context, pkg, sessionID, pyBin, workDir string) error {
	sb := agentSandboxConfig()
	mode := agentSandboxModeName(sb)
	deny := func(reason string) error {
		AuditCommandExec(CommandAuditEntry{
			Timestamp: time.Now(), Tool: "pip",
			Command:  pipAuditCommand(pkg),
			Decision: "denied", Risk: "medium",
			Reason:      reason,
			SandboxMode: mode, SessionID: sessionID,
		})
		return fmt.Errorf("pip install refused: %s", reason)
	}
	// Config gate first (fail closed, no prompt): preserves the pipAllowed
	// contract - AllowPipInstall=false/nil denies without asking.
	if !pipAllowed(sb) {
		return deny("pip install not allowed (allow_pip_install)")
	}
	if !pipPackageAllowed(sb, pkg) {
		return deny("package not in pip allowlist (pip_allowlist)")
	}
	// H1 single absolute root: the pip binary is the interpreter's own venv
	// sibling (absolute), so integrity verify and the choke spawn agree
	// even when the process cwd differs from the run workspace.
	pipBinAbs := pipBinForPyBin(pyBin, workDir)
	if !filepath.IsAbs(pipBinAbs) {
		if abs, err := filepath.Abs(pipBinAbs); err == nil {
			pipBinAbs = abs
		}
	}
	// M6: the strict pin/hash/venv gate runs BEFORE the human prompt, so
	// the approval shows the exact install (pin + hash count) and denials
	// never prompt. The prompt carries human-readable text; the audit
	// below carries hashes only.
	if sb.PipStrictPins {
		strictPin, strictArgs, serr := checkPipStrict(sb, pkg, pipBinAbs)
		if serr != nil {
			return deny(serr.Error())
		}
		hashN := countStrictHashes(strictArgs)
		approved, aerr := ApproveExec(ctx, ApprovalRequest{
			Tool:      "pip",
			Command:   fmt.Sprintf("pip install %s (require-hashes:%d)", strictPin, hashN),
			Risk:      "medium",
			Reason:    "auto-install missing Python package into .venv",
			Source:    "direct",
			ExpiresAt: time.Now().Add(ApprovalExpiry()),
			SessionID: sessionID,
		})
		if aerr != nil || !approved {
			return deny("pip install not approved by user")
		}
		AuditCommandExec(CommandAuditEntry{
			Timestamp: time.Now(), Tool: "pip",
			Command:  pipAuditCommand(strictPin),
			Decision: "allowed", Risk: "medium",
			SandboxMode: mode, SessionID: sessionID,
		})
		if err := runPipInstallViaChokeArgs(ctx, pipBinAbs, strictArgs, workDir, pipEnvMap(pipBinAbs)); err != nil {
			return fmt.Errorf("pip install failed: %w", err)
		}
		return nil
	}
	// The human sees the real package name (this is the approval prompt,
	// not the audit trail); the audit below hashes it.
	approved, aerr := ApproveExec(ctx, ApprovalRequest{
		Tool:      "pip",
		Command:   "pip install " + pkg,
		Risk:      "medium",
		Reason:    "auto-install missing Python package into .venv",
		Source:    "direct",
		ExpiresAt: time.Now().Add(ApprovalExpiry()),
		SessionID: sessionID,
	})
	if aerr != nil || !approved {
		return deny("pip install not approved by user")
	}
	AuditCommandExec(CommandAuditEntry{
		Timestamp: time.Now(), Tool: "pip",
		Command:  pipAuditCommand(pkg),
		Decision: "allowed", Risk: "medium",
		SandboxMode: mode, SessionID: sessionID,
	})
	if err := runPipInstallViaChoke(ctx, pipBinAbs, pkg, workDir, pipEnvMap(pipBinAbs)); err != nil {
		return fmt.Errorf("pip install failed: %w", err)
	}
	return nil
}

// runPythonWithAutoPip runs the script, and on a missing-module failure
// runs the gated installer at most once, then retries the script exactly
// once. A second ModuleNotFoundError on the retry output never triggers
// another install in the same call (no loop). Install refusal or failure
// keeps the original output unchanged.
func runPythonWithAutoPip(ctx context.Context, run func() (string, string), install func(context.Context, string) error) (string, string) {
	stdout, stderr := run()
	pkg, ok := extractMissingModule(stdout)
	if !ok {
		return stdout, stderr
	}
	if err := install(ctx, pkg); err != nil {
		return stdout, stderr
	}
	return run()
}

// checkPipPreflight is the test seam for the pre-approval legs: config +
// allowlist without prompting. Production calls gateAndInstallPip.
func checkPipPreflight(sb *sandbox.SandboxConfig, pkg string) error {
	if !pipAllowed(sb) {
		return fmt.Errorf("pip install not allowed (allow_pip_install)")
	}
	if !pipPackageAllowed(sb, pkg) {
		return fmt.Errorf("package not in pip allowlist (pip_allowlist)")
	}
	return nil
}

// sessionIDFromAgentCtx resolves the hakase session for audit lines.
func sessionIDFromAgentCtx(ctx context.Context) string {
	return interfaces.SessionIDFromCtx(ctx)
}
