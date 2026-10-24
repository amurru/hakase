package sandbox

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

func installV2Policy(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	p.Version = 2
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile v2: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

func stubFlowLabel(t *testing.T, l *permissions.SessionLabel) {
	t.Helper()
	orig := FlowLabelFunc
	if l == nil {
		FlowLabelFunc = nil
	} else {
		cp := *l
		FlowLabelFunc = func(context.Context) *permissions.SessionLabel {
			out := cp
			return &out
		}
	}
	t.Cleanup(func() { FlowLabelFunc = orig })
}

func flowNarrowed() *permissions.SessionLabel {
	return &permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 1}
}

// TestTaskResolveFlowDeny pins FG-003 file pre-dispatch: a flow-deny on
// the resolved path fails. Reads/edits are usually sources (delta-only
// rules): an allow+requires Ask does NOT fail here - the read proceeds,
// taints the label (Phase 2 post-admission), and the later send is gated.
// Only Deny escalates in taskResolve, mirroring the base PM-002 wiring
// (Ask stays on the tool-risk approval path).
func TestTaskResolveFlowDeny(t *testing.T) {
	dir := t.TempDir()
	// Unknown-sink fail-closed (C2) under a default-deny v2 policy: the
	// edit action maps no rule, the narrowed label denies explicitly.
	installV2Policy(t, permissions.Policy{Default: permissions.EffectDeny, Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	stubFlowLabel(t, flowNarrowed())
	if _, err := taskResolve(context.Background(), dir+"/notes.txt", true, ""); err == nil {
		t.Error("narrowed unmapped edit passed, want flow guard denial")
	} else if !strings.Contains(err.Error(), "flow guard") {
		t.Errorf("error = %q, want flow guard citation", err)
	}
	// Start label: additive design (default alone enforces nothing), so
	// the unmapped edit passes with no flow detail.
	start := permissions.StartLabel()
	stubFlowLabel(t, &start)
	if _, err := taskResolve(context.Background(), dir+"/notes.txt", true, ""); err != nil {
		t.Errorf("start unmapped edit failed: %v (additive: default alone is not a rule)", err)
	}
}

// TestTaskResolveFlowSourceProceeds pins S9 fail-closed: a requires Ask
// on read denies in taskResolve (the sandbox has no approval round-trip
// of its own). The pre-dispatch callback already routed the Ask to the
// human; this layer denies so a direct tool path can never bypass it.
// Only an allow (no flow detail) proceeds to the filesystem.
func TestTaskResolveFlowSourceProceeds(t *testing.T) {
	dir := t.TempDir()
	installV2Policy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	stubFlowLabel(t, flowNarrowed())
	if _, err := taskResolve(context.Background(), dir+"/notes.txt", false, ""); err == nil {
		t.Error("narrowed source read passed, want flow guard denial (S9 fail-closed)")
	} else if !strings.Contains(err.Error(), "flow guard") {
		t.Errorf("error = %q, want flow guard citation", err)
	}
}

// TestAuditFlowDeny pins the WithLabel exec-path audit: narrowed
// unknown-sink under default-deny fails with flow detail, the StartLabel
// wrapper keeps v1 behavior.
func TestAuditFlowDeny(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
	}
	installV2Policy(t, permissions.Policy{Default: permissions.EffectDeny, Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	if err := AuditSystemCommandPathsWithLabel(sb, "git status", nil, "", flowNarrowed()); err == nil {
		t.Error("narrowed audit passed, want flow guard denial")
	} else if !strings.Contains(err.Error(), "flow guard") {
		t.Errorf("error = %q, want flow guard citation", err)
	}
	// Start wrapper: additive design keeps v1 behavior (default alone
	// enforces nothing), bare words pass the audit.
	if err := AuditSystemCommandPaths(sb, "git status", nil, ""); err != nil {
		t.Errorf("start wrapper audit failed: %v", err)
	}
}
