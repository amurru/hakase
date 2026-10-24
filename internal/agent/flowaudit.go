// flowaudit.go - Flow Guard audit decisions (FG-006).
//
// Four decisions ride the always-on audit trail:
//
//   - flow_blocked: a pre-dispatch denial (requires failure, unknown
//     sink on a narrowed label, or a hard deny).
//   - flow_sanitized: a payload or result was redacted before it left
//     the guard.
//   - flow_error_redacted: the tool failed, its result and error text
//     were redacted, and the label was conservatively folded.
//   - flow_recomputed: a pre-v2 session resumed under a conservative
//     label.
//
// Every entry is metadata only: the label snapshot, the args hash and a
// redacted reason. Never raw args, never payload content, never the
// resource paths a policy matched on (M2). The CSV export appends the
// flow columns at the end of the row so old rows stay readable (see
// audit_chain.go).
package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"amurru/hakase/internal/permissions"
)

// flowLabelSnapshot renders the label for audit lines and denial text:
// "trusted/internal v2", or "uninitialized" for a nil (fail-closed)
// label.
func flowLabelSnapshot(l *permissions.SessionLabel) string {
	if l == nil {
		return "uninitialized"
	}
	trust := "trusted"
	if l.Trust == permissions.TrustSuspicious {
		trust = "suspicious"
	}
	aud := "public"
	switch l.Audience {
	case permissions.AudienceSelf:
		aud = "self"
	case permissions.AudienceInternal:
		aud = "internal"
	}
	return fmt.Sprintf("%s/%s v%d", trust, aud, l.Version)
}

// flowAuditEntry builds the shared metadata of a flow audit entry: the
// rule citation (when there is one), the label snapshot and the args
// hash. The reason is set by the caller and redacted on write.
func flowAuditEntry(toolName, action, argsHash, sessionID string, label *permissions.SessionLabel, rule *permissions.Rule) CommandAuditEntry {
	var pr permissions.PolicyRule
	if rule != nil {
		pr = rule.Citation()
	}
	pr.Flow = fmt.Sprintf("label %s argsHash %s", flowLabelSnapshot(label), argsHash)
	return CommandAuditEntry{
		Timestamp:    time.Now(),
		Tool:         toolName,
		Command:      "flow:" + action,
		SessionID:    sessionID,
		PolicyRule:   pr,
		FlowLabel:    flowLabelSnapshot(label),
		FlowArgsHash: argsHash,
	}
}

// AuditFlowBlock records a pre-dispatch flow denial (decision
// "flow_blocked", metadata only: hashes + label snapshot, never raw
// args). A flow ask that a human later releases shows this block plus
// the approval answer on the trail.
func AuditFlowBlock(toolName, action, argsHash, sessionID string, rule *permissions.Rule, label *permissions.SessionLabel) {
	e := flowAuditEntry(toolName, action, argsHash, sessionID, label, rule)
	e.Decision = "flow_blocked"
	e.Reason = "flow guard blocked " + action
	AuditCommandExec(e)
}

// AuditFlowSanitized records a redaction (decision "flow_sanitized",
// metadata only).
func AuditFlowSanitized(toolName, action, argsHash, sessionID string, label *permissions.SessionLabel) {
	e := flowAuditEntry(toolName, action, argsHash, sessionID, label, nil)
	e.Decision = "flow_sanitized"
	e.Reason = "flow guard redacted secret-shaped content"
	AuditCommandExec(e)
}

// AuditFlowErrorRedacted records the error path (decision
// "flow_error_redacted"): the tool failed, its result and error text
// were redacted, and the label was conservatively folded.
func AuditFlowErrorRedacted(toolName, action, argsHash, sessionID string, label *permissions.SessionLabel) {
	e := flowAuditEntry(toolName, action, argsHash, sessionID, label, nil)
	e.Decision = "flow_error_redacted"
	e.Reason = "flow guard redacted a failed tool result"
	AuditCommandExec(e)
}

// AuditFlowRecomputed records a pre-v2 session resumed under a
// conservative label (decision "flow_recomputed"). Old session files
// never resume widest (M1).
func AuditFlowRecomputed(sessionID string, label *permissions.SessionLabel) {
	e := flowAuditEntry("session", "flow", "", sessionID, label, nil)
	e.Decision = "flow_recomputed"
	e.Reason = "pre-v2 session resumed with a conservative flow label"
	AuditCommandExec(e)
}

// flowDebugEnabled reports whether the Flow Guard debug log is on
// (HAKASE_FLOW_DEBUG=1). The debug log goes to stderr only: never to
// the audit file, never to the prod logs (M5).
func flowDebugEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HAKASE_FLOW_DEBUG")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// flowDebugf writes one Flow Guard debug line to stderr. Callers pass
// hashes, label snapshots and verdicts only - never raw args, never
// payload content.
func flowDebugf(format string, args ...any) {
	if !flowDebugEnabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "flow: "+format+"\n", args...)
}
