package agent

import (
	"context"
	"testing"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/permissions"
)

// TestApproveExecModeDeny pins approval.mode=deny auto-denying without
// touching a gate (works headless: rt is nil here).
func TestApproveExecModeDeny(t *testing.T) {
	savedDeps, savedRt := deps, rt
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: "deny"}}
	rt = nil
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })

	approved, err := ApproveExec(context.Background(), ApprovalRequest{
		Tool: "system_exec", Command: "ls", ExpiresAt: time.Now().Add(time.Minute),
	})
	if approved {
		t.Error("ApproveExec = true under mode=deny, want false")
	}
	if err == nil {
		t.Error("ApproveExec err = nil under mode=deny, want error")
	}
}

// TestApproveExecModeAllow pins approval.mode=allow auto-approving without
// blocking on a gate (works headless: rt is nil here).
func TestApproveExecModeAllow(t *testing.T) {
	savedDeps, savedRt := deps, rt
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: "allow"}}
	rt = nil
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })

	approved, err := ApproveExec(context.Background(), ApprovalRequest{
		Tool: "system_exec", Command: "ls", ExpiresAt: time.Now().Add(time.Minute),
	})
	if !approved || err != nil {
		t.Errorf("ApproveExec = %v/%v under mode=allow, want true/nil", approved, err)
	}
}

// TestApproveExecModeAllowRespectsDisableBypass pins enterprise
// disable_bypass neutralizing mode=allow back to interactive (fail-closed
// headless here: no gate wired, so it must deny, not auto-approve).
func TestApproveExecModeAllowRespectsDisableBypass(t *testing.T) {
	savedDeps, savedRt := deps, rt
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: "allow"}}
	rt = nil
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })

	lp, err := permissions.Compile(permissions.Policy{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.InstallLayered(&permissions.LayeredPolicy{
		Policy:     lp,
		Enterprise: permissions.EnterprisePolicy{DisableBypass: true},
	})
	t.Cleanup(func() { permissions.InstallLayered(nil) })

	approved, err := ApproveExec(context.Background(), ApprovalRequest{
		Tool: "system_exec", Command: "ls", ExpiresAt: time.Now().Add(time.Minute),
	})
	if approved || err == nil {
		t.Errorf("ApproveExec = %v/%v under disable_bypass, want false/error", approved, err)
	}
}

// TestApprovalModeAllowKeepsReplayNever pins the ReplayNever interaction:
// auto-approve only skips the prompt for a turn that already decided to
// execute - resume must still never replay side effects.
func TestApprovalModeAllowKeepsReplayNever(t *testing.T) {
	savedDeps := deps
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: "allow"}}
	t.Cleanup(func() { deps = savedDeps })

	for _, tool := range []string{"system_exec", "git_push", "git_commit", "unknown_tool_xyz"} {
		if got := ReplayPolicyFor(tool); got != ReplayNever {
			t.Errorf("ReplayPolicyFor(%q) = %q under mode=allow, want never", tool, got)
		}
	}
}
