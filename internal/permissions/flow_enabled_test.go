// flow_enabled_test.go - Flow Guard fix-up loop 3/2 (epic #124, M2).
//
// Pins the installed-policy FlowEnabled matrix: only a v2 policy enables
// the flow path, however it is installed. The production path installs via
// InstallLayered (agent/permissions_init.go), so both Install and
// InstallLayered are covered for v1 and v2.
package permissions

import "testing"

// TestFlowEnabledInstallMatrix pins M2: FlowEnabled is false with no
// policy, with a v1 policy (either install spelling, preserving v1
// behavior byte-identical), and true with a v2 policy (either spelling).
func TestFlowEnabledInstallMatrix(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	v1, err := Compile(Policy{Version: 1, Default: "ask"})
	if err != nil {
		t.Fatalf("Compile v1: %v", err)
	}
	if v1.FlowEnabled() {
		t.Fatal("v1 CompiledPolicy FlowEnabled = true, want false")
	}
	v2, err := Compile(Policy{Version: 2, Default: "ask", Rules: []Rule{
		{Action: "webfetch", Resource: "*", Effect: EffectAllow, Delta: &FlowDelta{Trust: "suspicious"}},
	}})
	if err != nil {
		t.Fatalf("Compile v2: %v", err)
	}
	if !v2.FlowEnabled() {
		t.Fatal("v2 CompiledPolicy FlowEnabled = false, want true")
	}
	t.Cleanup(func() { Install(nil) })

	Install(nil)
	if FlowEnabled() {
		t.Error("Install(nil): FlowEnabled = true, want false")
	}
	Install(v1)
	if FlowEnabled() {
		t.Error("Install(v1): FlowEnabled = true, want false")
	}
	InstallLayered(&LayeredPolicy{Policy: v1})
	if FlowEnabled() {
		t.Error("InstallLayered(v1): FlowEnabled = true, want false")
	}
	Install(v2)
	if !FlowEnabled() {
		t.Error("Install(v2): FlowEnabled = false, want true")
	}
	InstallLayered(&LayeredPolicy{Policy: v2})
	if !FlowEnabled() {
		t.Error("InstallLayered(v2): FlowEnabled = false, want true")
	}
}
