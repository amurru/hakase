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
	// Flow Guard (S7): a one-shot pre-grant minted by ResolveApprovalPause
	// for a (tool, command) re-drive must never satisfy a flow ask. The
	// pre-grant carries no (argsHash, labelVersion) binding, so it cannot
	// prove it evaluated the flow Requires that produced this FlowReason;
	// skip it and fall through to the human. S17: the grant is session
	// scoped - only the asking session's grant satisfies it.
	if req.FlowReason == "" && consumePreGrant(req.Tool, req.Command, req.SessionID) {
		return true, nil
	}
	// approval.mode (PM-002): "deny" auto-denies and "allow"
	// auto-approves without blocking on a gate. Enterprise
	// disable_bypass neutralizes mode=allow back to interactive.
	// Either way this only skips the prompt for a turn that already
	// decided to execute: replay policy is untouched, so mode=allow
	// never replays side effects on resume (see ReplayPolicyFor -
	// system_exec stays ReplayNever).
	//
	// Flow Guard (docs/flow-guard FG-005, H5): a flow ask is never
	// auto-approved by mode=allow - the human decides, because the flow
	// ask exists exactly because the label says this call is not
	// admissible. The approval covers one exact call (tool, argsHash,
	// labelVersion) and never resets the session label.
	switch approvalMode() {
	case "deny":
		return false, fmt.Errorf("approval denied by approval.mode=deny")
	case "allow":
		if req.FlowReason == "" && !permissions.BypassDisabled() {
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
		"approval: "+req.Tool+" "+util.RedactTruncate(req.Command, util.MaxLogField),
		approvalPauseDetail(req))
	if pauseID != "" {
		defer unrecordGatePause(pauseID)
	}
	return g.AskApproval(ctx, req)
}

// approvalPauseDetail builds the pause detail for an approval gate. Flow
// asks additionally carry their binding (FlowReason/FlowArgsHash/
// FlowLabelVersion) so a restart can tell a flow-scoped pause from a
// legacy record and scope the resume grant accordingly (S7). The keys
// are written whenever a v2 policy is installed (even for non-flow
// asks, with empty flow values) so legacy records - which lack the keys
// entirely - fail closed under v2 instead of silently mapping onto the
// new key. No v2 policy keeps the record shape byte-identical (v1).
func approvalPauseDetail(req ApprovalRequest) map[string]any {
	// S13 audit-only redaction: the pause record is persisted (resume
	// replays it), so the command is redacted here; the live approval
	// prompt (req.Command) stays full truncated for the human.
	detail := map[string]any{
		"tool": req.Tool, "command": util.RedactTruncate(req.Command, util.MaxLogField),
		"risk": req.Risk, "reason": req.Reason,
		// S10/M3: the unredacted grant hash rides alongside the
		// redacted command so resume can mint the pre-grant without
		// persisting (or re-deriving from) cleartext. Hash only.
		"grantHash": permissions.ArgsHash(req.Tool, req.Command),
	}
	if permissions.FlowEnabled() {
		detail["flowReason"] = req.FlowReason
		detail["flowArgsHash"] = req.FlowArgsHash
		detail["flowLabelVersion"] = req.FlowLabelVersion
	}
	return detail
}
