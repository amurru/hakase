package agent

import (
	"amurru/hakase/internal/interfaces"
	"fmt"
	"time"
)

// ApprovalRequest describes a tool invocation needing user approval.
// It is a type alias for the shared interface contract.
type ApprovalRequest = interfaces.ApprovalRequest

// ApprovalExpiry returns the configured approval expiry duration from deps.
// Defaults to 60 seconds when not explicitly configured (ExpirySeconds <= 0).
func ApprovalExpiry() time.Duration {
	if deps == nil || deps.ApprovalCfg.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(deps.ApprovalCfg.ExpirySeconds) * time.Second
}

// ApproveExec wraps the interactive approval gate. When the gate is nil
// (headless mode / not yet wired), fails closed.
func ApproveExec(req ApprovalRequest) (bool, error) {
	if rt == nil {
		return false, fmt.Errorf("no approval mechanism available (headless mode)")
	}
	g := rt.ApprovalGate()
	if g == nil {
		return false, fmt.Errorf("no approval mechanism available (headless mode)")
	}
	return g.AskApproval(req)
}
