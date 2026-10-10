package agent

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
	"context"
	"fmt"
	"strings"
	"time"
)

// ApprovalRequest describes a tool invocation needing user approval.
// It is a type alias for the shared interface contract.
type ApprovalRequest = interfaces.ApprovalRequest

// approvalMode returns the configured approval mode ("interactive"
// default, "deny", "allow"), lowercased. Unknown values fall back to
// interactive at runtime; LoadConfig rejects them at startup.
func approvalMode() string {
	if deps == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(deps.ApprovalCfg.Mode))
}

// ApprovalDenyAll reports whether approval.mode=deny is configured.
// The sandbox package consults it (via ApprovalDenyAllFunc) to refuse
// everything up front, including paths that never reach ApproveExec.
func ApprovalDenyAll() bool {
	return approvalMode() == "deny"
}

// ApprovalExpiry returns the configured approval expiry duration from deps.
// Defaults to 60 seconds when not explicitly configured (ExpirySeconds <= 0).
func ApprovalExpiry() time.Duration {
	if deps == nil || deps.ApprovalCfg.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(deps.ApprovalCfg.ExpirySeconds) * time.Second
}

// ApproveExec wraps the interactive approval gate. When the gate is nil
// (headless mode / not yet wired), fails closed. A one-shot pre-grant
// installed by ResolveApprovalPause auto-approves its exact
// tool+command without blocking (durable-resume approval re-drive).
// While blocked, the pause is recorded for durable resume
// (best-effort; never fails the gate) and removed on resolve.
func ApproveExec(ctx context.Context, req ApprovalRequest) (bool, error) {
	if consumePreGrant(req.Tool, req.Command) {
		return true, nil
	}
	// approval.mode (PM-002): "deny" auto-denies and "allow"
	// auto-approves without blocking on a gate. Enterprise
	// disable_bypass neutralizes mode=allow back to interactive.
	// Either way this only skips the prompt for a turn that already
	// decided to execute: replay policy is untouched, so mode=allow
	// never replays side effects on resume (see ReplayPolicyFor -
	// system_exec stays ReplayNever).
	switch approvalMode() {
	case "deny":
		return false, fmt.Errorf("approval denied by approval.mode=deny")
	case "allow":
		if !permissions.BypassDisabled() {
			return true, nil
		}
	}
	if rt == nil {
		return false, fmt.Errorf("no approval mechanism available (headless mode)")
	}
	g := rt.ApprovalGate()
	if g == nil {
		return false, fmt.Errorf("no approval mechanism available (headless mode)")
	}
	pauseID := recordGatePause(ctx, hakasesession.PauseGateApproval, req.SessionID,
		"approval: "+req.Tool+" "+util.TruncateStr(req.Command),
		map[string]any{
			"tool": req.Tool, "command": req.Command,
			"risk": req.Risk, "reason": req.Reason,
		})
	if pauseID != "" {
		defer unrecordGatePause(pauseID)
	}
	return g.AskApproval(ctx, req)
}
