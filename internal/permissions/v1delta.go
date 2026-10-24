// v1delta.go - V1-DELTA register (S25 guardrail exception).
//
// Guardrail: no v1 behavior break, except through the V1-DELTA register.
// A v0/v1 policy carrying flow keys (delta/requires/sanitizable) is
// rejected at Compile - unless the rule's action is explicitly
// registered here. Registration is code-only (no policy-file surface):
// the lane registers nothing by default, so stock v1 behavior is
// byte-identical. Tests register and unregister within the test.
//
// A registered action's delta is honored by DeltaFor even when the
// compiled policy is not flow-enabled; requires/sanitizable on a
// registered v1 action still evaluate through EvaluateWithLabel only
// when the label-aware path runs (FlowEnabled passthrough otherwise).
package permissions

import "sync"

var v1DeltaRegistry = struct {
	sync.RWMutex
	m map[string]bool
}{m: make(map[string]bool)}

// RegisterV1Delta sanctions one action for v1 delta folding (the only
// permitted v1 behavior change in this lane). Test-only helper in
// practice: production code registers nothing.
func RegisterV1Delta(action string) {
	if action == "" {
		return
	}
	v1DeltaRegistry.Lock()
	defer v1DeltaRegistry.Unlock()
	v1DeltaRegistry.m[action] = true
}

// UnregisterV1Delta removes a V1-DELTA sanction (test cleanup).
func UnregisterV1Delta(action string) {
	v1DeltaRegistry.Lock()
	defer v1DeltaRegistry.Unlock()
	delete(v1DeltaRegistry.m, action)
}

// IsV1DeltaRegistered reports whether action carries a V1-DELTA sanction.
func IsV1DeltaRegistered(action string) bool {
	v1DeltaRegistry.RLock()
	defer v1DeltaRegistry.RUnlock()
	return v1DeltaRegistry.m[action]
}
