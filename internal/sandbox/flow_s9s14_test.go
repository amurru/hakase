// flow_s9s14_test.go - S9 nil fail-closed at all sandbox gates + S14
// shell ask binding (highs fix-up lane, epic #124).
package sandbox

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

// installFlowV2 compiles and installs a v2 policy for the test (the
// flowExperimental gate is required for version 2 to load).
func installFlowV2(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

// v2DenyDefault is a v2 policy with no shell rule: an unmapped shell on
// a narrowed label fails closed (deny, unknown sink), while the start
// label keeps base behavior (no flow detail).
func v2DenyDefault() permissions.Policy {
	return permissions.Policy{Version: 2, Default: "deny", Rules: []permissions.Rule{
		{Action: "read", Resource: "/specific/*", Effect: "allow"},
	}}
}

// withFlowLabelFunc swaps the label resolver for one test.
func withFlowLabelFunc(t *testing.T, fn func(context.Context) *permissions.SessionLabel) {
	t.Helper()
	old := FlowLabelFunc
	FlowLabelFunc = fn
	t.Cleanup(func() { FlowLabelFunc = old })
}

// S9 matrix, fileops gate: nil (unregistered under v2) fails closed,
// start passes, v1 (no policy) passes with any label, and a mapped
// explicit allow passes even for nil.
func TestTaskResolveNilFailClosedMatrix(t *testing.T) {
	ctx := context.Background()
	start := permissions.StartLabel()

	installFlowV2(t, v2DenyDefault())

	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { return nil })
	if _, err := taskResolve(ctx, "unmapped/file.txt", false, ""); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Errorf("nil label unmapped read = %v, want flow-guard denial", err)
	}
	// A mapped explicit allow still passes for nil: the rule decides,
	// not the unknown-sink fallback.
	if _, err := taskResolve(ctx, "/specific/file.txt", false, ""); err != nil {
		t.Errorf("nil label mapped allow read = %v, want nil", err)
	}

	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel {
		cp := start
		return &cp
	})
	if _, err := taskResolve(ctx, "unmapped/file.txt", false, ""); err != nil {
		t.Errorf("start label unmapped read = %v, want nil (v1 parity)", err)
	}

	// v1: no policy installed, nil label passes through untouched.
	permissions.Install(nil)
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { return nil })
	if _, err := taskResolve(ctx, "unmapped/file.txt", false, ""); err != nil {
		t.Errorf("v1 nil label read = %v, want nil (FlowEnabled passthrough)", err)
	}
}

// S9 matrix, path-audit gate: same fail-closed shape as fileops.
func TestAuditPathsNilFailClosedMatrix(t *testing.T) {
	sb := &SandboxConfig{Mode: SandboxModePaths}
	start := permissions.StartLabel()

	installFlowV2(t, v2DenyDefault())

	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { return nil })
	// The audit helper takes the label directly: nil must deny, not skip.
	if err := AuditSystemCommandPathsWithLabel(sb, "echo hi", nil, "", nil); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Errorf("nil label unmapped shell audit = %v, want flow-guard denial", err)
	}
	if err := AuditSystemCommandPathsWithLabel(sb, "echo hi", nil, "", &start); err != nil {
		t.Errorf("start label unmapped shell audit = %v, want nil", err)
	}

	// v1: FlowEnabled false, nil label passes through untouched.
	permissions.Install(nil)
	if err := AuditSystemCommandPathsWithLabel(sb, "echo hi", nil, "", nil); err != nil {
		t.Errorf("v1 nil label shell audit = %v, want nil (passthrough)", err)
	}
}

// S14: the shell ask binding is empty unless the ask is flow-caused.
// Pure helper, no BuildExecCommand API change.
func TestFlowShellAskBinding(t *testing.T) {
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
	start := permissions.StartLabel()

	// v1: every ask binds nothing (mode=allow still applies to base asks).
	permissions.Install(nil)
	r, h, v := flowShellAskBinding(&narrowed, "echo hi", nil, GateDecision{Action: ActionAsk, Reason: "needs approval"})
	if r != "" || h != "" || v != 0 {
		t.Errorf("v1 ask binding = (%q,%q,%d), want empty", r, h, v)
	}

	// v2, base ask (start label, unmapped shell, no flow detail): empty.
	installFlowV2(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "/specific/*", Effect: "allow"},
	}})
	r, h, v = flowShellAskBinding(&start, "echo hi", nil, GateDecision{Action: ActionAsk, Reason: "needs approval"})
	if r != "" || h != "" || v != 0 {
		t.Errorf("v2 base ask binding = (%q,%q,%d), want empty", r, h, v)
	}

	// v2, flow ask (narrowed label, unmapped shell fails closed): bound.
	ask := GateDecision{Action: ActionAsk, Reason: "flow: shell blocked (suspicious/internal, v0) by permissions policy"}
	r, h, v = flowShellAskBinding(&narrowed, "echo hi", nil, ask)
	if r == "" || h == "" {
		t.Errorf("v2 flow ask binding = (%q,%q,%d), want reason+hash", r, h, v)
	}
	if v != narrowed.Version {
		t.Errorf("binding version = %d, want %d", v, narrowed.Version)
	}
	wantHash := permissions.FlowArgsHash("system_exec", "shell", permissions.ShellResources("echo hi", nil))
	if h != wantHash {
		t.Errorf("binding hash = %q, want %q (shared ShellResources identity)", h, wantHash)
	}

	// Deny decisions never bind (denials refuse outright, no approval).
	r, h, v = flowShellAskBinding(&narrowed, "echo hi", nil, GateDecision{Action: ActionDeny, Reason: "denied"})
	if r != "" || h != "" || v != 0 {
		t.Errorf("deny binding = (%q,%q,%d), want empty", r, h, v)
	}
}

// S14 two-rule identity regression: the gate, the audit, and the binding
// must all match the SAME ShellLine(command, args). A rule that matches
// the bare command must not fire when args extend the line, and vice
// versa; approval.mode=allow never auto-approves the flow ask because
// the binding is non-empty.
func TestShellIdentityTwoRules(t *testing.T) {
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
	narrowed.Version = 3
	installFlowV2(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "shell", Resource: "echo hi", Effect: "allow"},
		{Action: "shell", Resource: "echo hi --flag", Effect: "deny"},
	}})
	// Bare command hits the allow rule (no flow detail): no binding.
	lineBare := permissions.ShellLine("echo hi", nil)
	if eff, rule, detail, ok := permissions.LookupWithLabel(&narrowed, "shell", lineBare); !ok || rule == nil || detail != "" || eff != permissions.EffectAllow {
		t.Fatalf("bare line lookup = (%v,%v,%q,%v), want allow no-detail", eff, rule, detail, ok)
	}
	// Extended line hits the deny rule: gate denies, binding would be deny (no bind).
	lineFlag := permissions.ShellLine("echo hi", []string{"--flag"})
	eff, rule, detail, ok := permissions.LookupWithLabel(&narrowed, "shell", lineFlag)
	if !ok || eff != permissions.EffectDeny || rule == nil {
		t.Fatalf("flag line lookup = (%v,%v,%q,%v), want deny", eff, rule, detail, ok)
	}
	// Flow ask binding derives from the gate decision (same identity):
	// a flow ask carries its hash so mode=allow cannot auto-approve.
	ask := GateDecision{Action: ActionAsk, Reason: "flow: shell blocked (suspicious/internal, v3) by permissions policy"}
	r, h, v := flowShellAskBinding(&narrowed, "echo hi", []string{"--flag"}, ask)
	want := permissions.FlowArgsHash("system_exec", "shell", permissions.ShellResources("echo hi", []string{"--flag"}))
	if h != want || r == "" || v != 3 {
		t.Fatalf("flow ask binding = (%q,%q,%d), want reason+hash %q v3", r, h, v, want)
	}
	if h == permissions.FlowArgsHash("system_exec", "shell", permissions.ShellResources("echo hi", nil)) {
		t.Error("args must change the binding hash (bare vs flag collide)")
	}
	// Base ask (no flow:) binds nothing: mode=allow still applies there.
	r, h, v = flowShellAskBinding(&narrowed, "echo hi", nil, GateDecision{Action: ActionAsk, Reason: "needs approval"})
	if r != "" || h != "" || v != 0 {
		t.Errorf("base ask binding = (%q,%q,%d), want empty (mode=allow applies)", r, h, v)
	}
}

// S9 matrix: nil/start/narrowed x Default ask + unknown sinks. Unmapped
// file actions fail closed on narrowed/nil (Ask or Deny both deny here);
// start passes (no flow detail); v1 passes through untouched.
func TestTaskResolveS9Matrix(t *testing.T) {
	ctx := context.Background()
	start := permissions.StartLabel()
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
	installFlowV2(t, permissions.Policy{Version: 2, Default: "ask"})
	// narrowed unmapped read denies (unknown-sink Ask -> deny here).
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { cp := narrowed; return &cp })
	if _, err := taskResolve(ctx, "unmapped-s9/file.txt", false, ""); err == nil || !strings.Contains(err.Error(), "flow guard") {
		t.Errorf("narrowed unmapped read = %v, want flow-guard denial", err)
	}
	// nil (unregistered under v2) denies too.
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { return nil })
	if _, err := taskResolve(ctx, "unmapped-s9/file.txt", false, ""); err == nil || !strings.Contains(err.Error(), "flow guard") {
		t.Errorf("nil unmapped read = %v, want flow-guard denial", err)
	}
	// start passes (additive: default alone enforces nothing on widest).
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { cp := start; return &cp })
	if _, err := taskResolve(ctx, "unmapped-s9/file.txt", false, ""); err != nil {
		t.Errorf("start unmapped read = %v, want nil (v1 parity on widest)", err)
	}
	// v1: no policy, every label passes.
	permissions.Install(nil)
	withFlowLabelFunc(t, func(context.Context) *permissions.SessionLabel { return nil })
	if _, err := taskResolve(ctx, "unmapped-s9/file.txt", false, ""); err != nil {
		t.Errorf("v1 nil read = %v, want nil (passthrough)", err)
	}
}
