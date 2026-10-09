package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
)

// TestExecAuditCarriesPolicyRule pins the deciding permissions rule
// flowing from the gate decision into the audit entry.
func TestExecAuditCarriesPolicyRule(t *testing.T) {
	origGate, origAudit, origApprove, origExpiry := EvaluateCommandFunc, AuditCommandFunc, ApproveFunc, ApprovalExpiryFunc
	rule := &permissions.Rule{Action: "shell", Resource: "git push *", Effect: permissions.EffectDeny, Source: "enterprise"}
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionDeny, Risk: RiskLow, Reason: "denied by permissions policy", PolicyRule: rule}
	}
	var got *CommandAuditEntry
	AuditCommandFunc = func(entry CommandAuditEntry) {
		e := entry
		got = &e
	}
	ApproveFunc = func(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) { return false, nil }
	ApprovalExpiryFunc = func() time.Duration { return 60 * time.Second }
	t.Cleanup(func() {
		EvaluateCommandFunc, AuditCommandFunc, ApproveFunc, ApprovalExpiryFunc = origGate, origAudit, origApprove, origExpiry
	})

	_, err := buildExecCommand(context.Background(), nil, "sess", "git push origin", nil, "", nil)
	if err == nil {
		t.Fatal("denied command built, want error")
	}
	if got == nil {
		t.Fatal("no audit entry captured")
	}
	if got.PolicyRule.Source != "enterprise" || got.PolicyRule.Resource != "git push *" || got.PolicyRule.Effect != "deny" {
		t.Errorf("entry policy_rule = %+v, want the deciding citation", got.PolicyRule)
	}
	if !strings.Contains(got.Reason, "permissions policy") {
		t.Errorf("entry reason = %q, want policy citation", got.Reason)
	}
}
