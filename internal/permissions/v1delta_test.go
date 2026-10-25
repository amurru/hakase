// v1delta_test.go - V1-DELTA register tests (S25 guardrail exception).
package permissions

import (
	"testing"
)

// TestV1DeltaRegisterUnregisteredInert pins no-v1-break: a v1 policy with
// flow keys is rejected, and an unregistered v1 delta never folds.
func TestV1DeltaRegisterUnregisteredInert(t *testing.T) {
	if _, err := Compile(Policy{Version: 1, Default: "allow", Rules: []Rule{
		{Action: "webfetch", Resource: "*", Effect: EffectAllow, Delta: &FlowDelta{Trust: "suspicious"}},
	}}); err == nil {
		t.Fatal("v1 policy with flow keys and no registration: want rejection")
	}
	cp, err := Compile(Policy{Version: 1, Default: "allow", Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow},
	}})
	if err != nil {
		t.Fatalf("plain v1 policy must compile: %v", err)
	}
	// Install-free check: DeltaFor on a v1 compiled policy folds nothing.
	if _, ok := cp.DeltaFor("read", "docs/a"); ok {
		t.Fatal("unregistered v1 action must not fold")
	}
}

// TestV1DeltaRegisteredHonored pins the sanctioned exception: a
// registered action compiles with flow keys under v1 and folds.
func TestV1DeltaRegisteredHonored(t *testing.T) {
	RegisterV1Delta("webfetch")
	t.Cleanup(func() { UnregisterV1Delta("webfetch") })
	cp, err := Compile(Policy{Version: 1, Default: "allow", Rules: []Rule{
		{Action: "webfetch", Resource: "*", Effect: EffectAllow, Delta: &FlowDelta{Trust: "suspicious"}},
	}})
	if err != nil {
		t.Fatalf("registered v1 action must compile: %v", err)
	}
	d, ok := cp.DeltaFor("webfetch", "https://example.com")
	if !ok || d.Trust != "suspicious" {
		t.Fatalf("registered v1 delta must fold, got %+v ok=%v", d, ok)
	}
	if _, ok := cp.DeltaFor("read", "docs/a"); ok {
		t.Fatal("unregistered sibling action must stay inert")
	}
}
