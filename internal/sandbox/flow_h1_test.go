// flow_h1_test.go - S9/H1 approve-then-run e2e (highs fix-up lane).
package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
)

// flowH1Ctx is a minimal invocation context carrying an ADK task id,
// resolved to the hakase session via interfaces.RegisterTaskSession
// (grants are session-scoped since the empty-session hardening: a
// versioned binding with no session never installs).
type flowH1Ctx struct {
	context.Context
	taskID string
}

func (c *flowH1Ctx) SessionID() string { return c.taskID }

// approve-then-run for read_file: an Ask without approval denies; the
// pre-dispatch grant (threaded via the shared store) admits the same
// call once, and only once.
func TestTaskResolveApprovedAskAdmits(t *testing.T) {
	dir := t.TempDir()
	installV2Policy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 2}
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel {
		cp := narrowed
		return &cp
	})
	sess := "sess-h1-read"
	taskID := "task-h1-read"
	interfaces.RegisterTaskSession(taskID, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	ctx := &flowH1Ctx{Context: context.Background(), taskID: taskID}
	raw := filepath.Join(dir, "notes.txt")
	if _, err := taskResolve(ctx, raw, false, ""); err == nil || !strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("unapproved Ask = %v, want flow-guard denial", err)
	}
	// Simulate the ADK pre-dispatch approval threading its grant
	// (exact sessioned tool binding, as grantFlowCall mirrors). S25: a
	// fresh approval mints twin v1+v2 records; the gate drains both.
	resources := permissions.PathResources(raw)
	permissions.GrantFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file",
		ArgsHash:     permissions.FlowArgsHash("read_file", "read", resources),
		LabelVersion: narrowed.Version,
	})
	permissions.GrantFlowBindingV2(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file",
		ArgsHash:     permissions.FlowArgsHash("read_file", "read", resources),
		LabelVersion: narrowed.Version,
	})
	if _, err := taskResolve(ctx, raw, false, ""); err != nil {
		t.Fatalf("approved Ask denied: %v", err)
	}
	// One-shot: the same call without a fresh grant denies again.
	if _, err := taskResolve(ctx, raw, false, ""); err == nil || !strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("reused grant = %v, want flow-guard denial (one-shot)", err)
	}
}

// High loop 5 single identity (sandbox side): the search root resolves
// with action "grep" so an approved grep grant admits the same call the
// agent pre-dispatch bound - and only once.
func TestSearchFilesApprovedAskAdmits(t *testing.T) {
	dir := t.TempDir()
	installV2Policy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "grep", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 2}
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel {
		cp := narrowed
		return &cp
	})
	sess := "sess-h1-grep"
	taskID := "task-h1-grep"
	interfaces.RegisterTaskSession(taskID, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	ctx := &flowH1Ctx{Context: context.Background(), taskID: taskID}
	if _, err := taskResolveWithAction(ctx, dir, "grep", false, ""); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("unapproved grep Ask = %v, want flow-guard denial", err)
	}
	// The agent pre-dispatch binds ("search_files","grep",PathResources):
	// the sandbox must consume that exact triple. S25: fresh approval
	// mints twin v1+v2 records; the gate drains both.
	resources := permissions.PathResources(dir)
	permissions.GrantFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "search_files",
		ArgsHash:     permissions.FlowArgsHash("search_files", "grep", resources),
		LabelVersion: narrowed.Version,
	})
	permissions.GrantFlowBindingV2(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "search_files",
		ArgsHash:     permissions.FlowArgsHash("search_files", "grep", resources),
		LabelVersion: narrowed.Version,
	})
	if _, err := taskResolveWithAction(ctx, dir, "grep", false, ""); err != nil {
		t.Fatalf("approved grep Ask denied: %v", err)
	}
	if _, err := taskResolveWithAction(ctx, dir, "grep", false, ""); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("reused grep grant = %v, want flow-guard denial (one-shot)", err)
	}
}

// approve-then-run for system_exec: the path audit denies an Ask without
// a local decision and admits with one.
func TestShellAuditApprovedAskAdmits(t *testing.T) {
	sb := &SandboxConfig{Mode: SandboxModePaths}
	installV2Policy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 2}
	if err := AuditSystemCommandPathsWithLabel(sb, "echo hi", nil, "", &narrowed); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("unapproved shell Ask = %v, want flow-guard denial", err)
	}
	// Gate local decision admits (same-stack approval, no second prompt).
	if err := auditSystemCommandPaths(sb, "echo hi", nil, "", &narrowed, true); err != nil {
		t.Fatalf("approved shell Ask denied: %v", err)
	}
	// Deny stays deny even with approval.
	installV2Policy(t, permissions.Policy{Default: "deny"})
	if err := auditSystemCommandPaths(sb, "echo hi", nil, "", &narrowed, true); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Fatalf("deny with approval = %v, want denial (deny stays deny)", err)
	}
}
