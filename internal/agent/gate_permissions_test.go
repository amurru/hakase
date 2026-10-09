package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

func installTestPolicy(t *testing.T, p permissions.Policy) {
	t.Helper()
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

// TestGatePermissionsDeny pins policy deny overriding an otherwise-allowed
// command, with the rule cited in the reason.
func TestGatePermissionsDeny(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "git push *", Effect: permissions.EffectDeny},
	}})
	d := EvaluateCommand(nil, "git push origin main", nil)
	if d.Action != ActionDeny {
		t.Errorf("Action = %q, want deny", d.Action)
	}
	if !strings.Contains(d.Reason, "permissions policy") {
		t.Errorf("Reason = %q, want permissions policy citation", d.Reason)
	}
	// Untouched commands keep gate behavior (git status is low-risk allow).
	d = EvaluateCommand(nil, "git status", nil)
	if d.Action != ActionAllow {
		t.Errorf("unmatched Action = %q, want allow", d.Action)
	}
}

// TestGatePermissionsAsk pins policy ask escalating an allowed command.
func TestGatePermissionsAsk(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "git *", Effect: permissions.EffectAsk},
	}})
	d := EvaluateCommand(nil, "git status", nil)
	if d.Action != ActionAsk {
		t.Errorf("Action = %q, want ask", d.Action)
	}
}

// TestGatePermissionsAllow pins policy allow downgrading a threshold ask.
func TestGatePermissionsAllow(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
	}})
	// date is medium-risk (asks at default threshold); policy allow wins.
	d := EvaluateCommand(nil, "date", nil)
	if d.Action != ActionAllow {
		t.Errorf("Action = %q, want allow", d.Action)
	}
}

// TestGatePermissionsNeverWidensHardDeny pins hard-deny winning over a
// blanket policy allow.
func TestGatePermissionsNeverWidensHardDeny(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
	}})
	d := EvaluateCommand(nil, "rm -rf /", nil)
	if d.Action != ActionDeny {
		t.Errorf("Action = %q, want deny (hard-deny beats policy allow)", d.Action)
	}
}

// TestGatePermissionsAllowFailsClosedOnUnknown pins RiskUnknown staying
// ask even under a blanket policy allow.
func TestGatePermissionsAllowFailsClosedOnUnknown(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
	}})
	d := EvaluateCommand(nil, "definitelynotarealbinary12345 --frobnicate", nil)
	if d.Action != ActionAsk {
		t.Errorf("Action = %q, want ask (unknown binary stays fail-closed)", d.Action)
	}
	if d.Risk != RiskUnknown {
		t.Errorf("Risk = %v, want unknown", d.Risk)
	}
}

// TestGateNoPolicyPreservesBehavior pins zero behavior change when no
// policy is installed.
func TestGateNoPolicyPreservesBehavior(t *testing.T) {
	permissions.Install(nil)
	d := EvaluateCommand(nil, "git status", nil)
	if d.Action != ActionAllow {
		t.Errorf("Action = %q, want allow", d.Action)
	}
	if strings.Contains(d.Reason, "permissions policy") {
		t.Errorf("Reason = %q, want no policy citation", d.Reason)
	}
}
