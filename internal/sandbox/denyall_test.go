package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestApprovalDenyAllBlocksExec pins approval.mode=deny refusing even
// allow-path commands (H1): the mode must cover everything, not just
// the ApproveExec ask path.
func TestApprovalDenyAllBlocksExec(t *testing.T) {
	origDeny, origAudit := ApprovalDenyAllFunc, AuditCommandFunc
	ApprovalDenyAllFunc = func() bool { return true }
	var audited *CommandAuditEntry
	AuditCommandFunc = func(entry CommandAuditEntry) {
		e := entry
		audited = &e
	}
	t.Cleanup(func() { ApprovalDenyAllFunc, AuditCommandFunc = origDeny, origAudit })

	_, err := buildExecCommand(context.Background(), nil, "sess", "git status", nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "approval.mode=deny") {
		t.Fatalf("buildExecCommand err = %v, want mode=deny refusal", err)
	}
	if audited == nil || audited.Decision != "denied" {
		t.Errorf("audit entry = %+v, want a denied decision", audited)
	}
}

// TestApprovalDenyAllBlocksFiles pins approval.mode=deny refusing file
// resolution too (H1): file tools never reach ApproveExec.
func TestApprovalDenyAllBlocksFiles(t *testing.T) {
	origDeny := ApprovalDenyAllFunc
	ApprovalDenyAllFunc = func() bool { return true }
	t.Cleanup(func() { ApprovalDenyAllFunc = origDeny })

	if _, err := taskResolve(context.Background(), filepath.Join(t.TempDir(), "a.txt"), false, ""); err == nil {
		t.Error("taskResolve read succeeded under mode=deny, want error")
	}
	if _, err := taskResolve(context.Background(), filepath.Join(t.TempDir(), "b.txt"), true, ""); err == nil {
		t.Error("taskResolve write succeeded under mode=deny, want error")
	}
}

// TestApprovalDenyAllOffPreservesBehavior pins the feature off by
// default (nil func): resolution works as before.
func TestApprovalDenyAllOffPreservesBehavior(t *testing.T) {
	origDeny := ApprovalDenyAllFunc
	ApprovalDenyAllFunc = nil
	t.Cleanup(func() { ApprovalDenyAllFunc = origDeny })

	if _, err := taskResolve(context.Background(), filepath.Join(t.TempDir(), "a.txt"), false, ""); err != nil {
		t.Errorf("taskResolve failed with feature off: %v", err)
	}
}
