package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

func installTestPolicyV2(t *testing.T, p permissions.Policy) {
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

func startLabel() *permissions.SessionLabel {
	l := permissions.StartLabel()
	return &l
}

func narrowedLabel() *permissions.SessionLabel {
	l := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 1}
	return &l
}

// TestGateFlowEscalatesAllowToAsk pins FG-003: a requires failure moves
// an allowed shell command to Ask with a flow reason, start unaffected.
func TestGateFlowEscalatesAllowToAsk(t *testing.T) {
	installTestPolicyV2(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	d := EvaluateCommandWithLabel(nil, "git status", nil, startLabel())
	if d.Action != ActionAllow {
		t.Errorf("start Action = %q, want allow", d.Action)
	}
	d = EvaluateCommandWithLabel(nil, "git status", nil, narrowedLabel())
	if d.Action != ActionAsk {
		t.Errorf("narrowed Action = %q, want ask", d.Action)
	}
	if !strings.Contains(d.Reason, "flow:") || !strings.Contains(d.Reason, "permissions policy") {
		t.Errorf("Reason = %q, want flow detail by permissions policy", d.Reason)
	}
}

// TestGateFlowUnknownSink pins C2 at the gate: narrowed + unmapped shell
// fails closed, start keeps risk behavior.
func TestGateFlowUnknownSink(t *testing.T) {
	installTestPolicyV2(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	d := EvaluateCommandWithLabel(nil, "git status", nil, narrowedLabel())
	if d.Action != ActionAsk {
		t.Errorf("narrowed unmapped Action = %q, want ask (fail-closed)", d.Action)
	}
	if !strings.Contains(d.Reason, "flow: unknown action") {
		t.Errorf("Reason = %q, want unknown-action flow detail", d.Reason)
	}
	d = EvaluateCommandWithLabel(nil, "git status", nil, startLabel())
	if d.Action != ActionAllow {
		t.Errorf("start unmapped Action = %q, want allow (v1 behavior)", d.Action)
	}
}

// TestGateFlowAtFile pins the best-effort @-file operand rule (M4).
func TestGateFlowAtFile(t *testing.T) {
	installTestPolicyV2(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
		{Action: "read", Resource: "docs/secret/*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	d := EvaluateCommandWithLabel(nil, "cat @docs/secret/key", nil, startLabel())
	if d.Action != ActionAllow {
		t.Errorf("start Action = %q, want allow", d.Action)
	}
	d = EvaluateCommandWithLabel(nil, "cat @docs/secret/key", nil, narrowedLabel())
	if d.Action != ActionAsk {
		t.Errorf("narrowed Action = %q, want ask (@-file escalation)", d.Action)
	}
	// Non-file @-mentions (no path shape) never escalate.
	d = EvaluateCommandWithLabel(nil, "echo @teammate hello", nil, narrowedLabel())
	if d.Action == ActionDeny {
		t.Errorf("mention Action = %q, want not-deny (best-effort only)", d.Action)
	}
}

// TestGateFlowV1Parity pins EvaluateCommand == WithLabel(start) on v1.
func TestGateFlowV1Parity(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "git *", Effect: permissions.EffectAllow},
	}})
	for _, cmd := range []string{"git status", "git push origin", "date", "ls /tmp"} {
		a := EvaluateCommand(nil, cmd, nil)
		b := EvaluateCommandWithLabel(nil, cmd, nil, startLabel())
		if a.Action != b.Action || a.Reason != b.Reason {
			t.Errorf("%q: plain=%q/%q withlabel=%q/%q, want identical", cmd, a.Action, a.Reason, b.Action, b.Reason)
		}
	}
	// Nil label is fail-closed, never widest.
	installTestPolicyV2(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	d := EvaluateCommandWithLabel(nil, "git status", nil, nil)
	if d.Action == ActionAllow {
		t.Errorf("nil-label Action = allow, want escalated (fail-closed)")
	}
}
