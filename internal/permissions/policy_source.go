package permissions

import "sync"

// Installed-policy source (Phase 1 wiring).
//
// The rule loader and config plumbing land in Phase 2 (T2.1/T2.2). Until
// then, the policy is installed programmatically (or by tests) via
// Install. A nil installed policy means "no permissions.json configured":
// Lookup reports ok=false and every call site keeps its existing gate
// behavior unchanged. Enforcement is therefore strictly additive -
// installing no policy can never change a verdict.
var (
	installedMu     sync.RWMutex
	installedPolicy *CompiledPolicy
	installedLayers *LayeredPolicy
)

// Install sets the active policy used by Lookup. Install(nil) removes it.
func Install(cp *CompiledPolicy) {
	installedMu.Lock()
	defer installedMu.Unlock()
	installedPolicy = cp
	installedLayers = nil
}

// InstallLayered sets the active layered policy (loader output).
// InstallLayered(nil) removes it.
func InstallLayered(lp *LayeredPolicy) {
	installedMu.Lock()
	defer installedMu.Unlock()
	installedLayers = lp
	if lp != nil {
		installedPolicy = lp.Policy
	} else {
		installedPolicy = nil
	}
}

// Lookup evaluates an action against resources using the installed
// policy. ok is false when no policy is installed (caller must fall back
// to its existing logic); otherwise it mirrors CompiledPolicy.Evaluate.
// Additive call sites (gate, fileops, path audit) act only when rule is
// non-nil, so a policy default alone never changes a verdict - closed
// world is expressed with an explicit catch-all rule instead.
func Lookup(action string, resources ...string) (eff Effect, rule *Rule, ok bool) {
	return LookupForAgent("", action, resources...)
}

// LookupForAgent evaluates with the named agent's overlay merged in
// ("" = base rules only).
func LookupForAgent(agentName, action string, resources ...string) (eff Effect, rule *Rule, ok bool) {
	installedMu.RLock()
	defer installedMu.RUnlock()
	if installedPolicy == nil {
		return "", nil, false
	}
	if agentName == "" {
		eff, rule = installedPolicy.Evaluate(action, resources...)
	} else {
		eff, rule = installedPolicy.EvaluateForAgent(agentName, action, resources...)
	}
	return eff, rule, true
}

// Installed returns the active layered policy, or nil when none is
// installed.
func Installed() *LayeredPolicy {
	installedMu.RLock()
	defer installedMu.RUnlock()
	return installedLayers
}

// BypassDisabled reports whether the installed enterprise layer sets
// disable_bypass (neutralizes approval.mode=allow). False when no
// layered policy is installed.
func BypassDisabled() bool {
	installedMu.RLock()
	defer installedMu.RUnlock()
	return installedLayers != nil && installedLayers.Enterprise.DisableBypass
}
