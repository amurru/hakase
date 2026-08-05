package main

import (
	"fmt"
	"time"
)

// ApprovalRequest describes a tool invocation needing user approval.
type ApprovalRequest struct {
	Tool      string // "system_exec" | "python_interpreter"
	Command   string // full command line (or code preview for python, truncated to 2000 runes)
	Args      []string
	Risk      string // Risk* name
	Reason    string // why approval is required
	Source    string // "direct" | "delegated"
	ExpiresAt time.Time
}

// askApproval is the interactive approval gate. The TUI installs it at
// startup (main.go). When nil (headless mode), approval is DENIED - fail
// closed. Returns true only when the human explicitly approved.
var askApproval func(req ApprovalRequest) (bool, error)

// currentApproval holds the runtime approval configuration, set in setupRunner
// like currentSandbox. Zero value is safe: defaults to deny mode with 60s expiry.
var currentApproval ApprovalConfig

// approvalExpiry returns the configured approval expiry duration. Defaults to
// 60 seconds when not explicitly configured (ExpirySeconds <= 0).
func approvalExpiry() time.Duration {
	if currentApproval.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(currentApproval.ExpirySeconds) * time.Second
}

// approveExec is a small wrapper: nil askApproval -> (false, error); otherwise
// delegates to the installed askApproval function. Always fails closed.
func approveExec(req ApprovalRequest) (bool, error) {
	if askApproval == nil {
		return false, fmt.Errorf("no approval mechanism available (headless mode)")
	}
	return askApproval(req)
}
