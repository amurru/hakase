// flow.go - Flow Guard session data-flow labels (FG-001).
//
// A per-session (trust x audience) label that only narrows. Trust combines
// by max (trusted < suspicious), audience ceiling combines by min
// (self < internal < public). Version increments only on a narrowing
// change. Stdlib only; URL/IDNA canonicalization lives in resources.go.
package permissions

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Trust is the integrity axis: trusted(0) < suspicious(1).
type Trust int

const (
	TrustTrusted Trust = iota
	TrustSuspicious
)

// Audience is the disclosure ceiling: self(0) < internal(1) < public(2).
type Audience int

const (
	AudienceSelf Audience = iota
	AudienceInternal
	AudiencePublic
)

// SessionLabel is the per-session flow label. Version counts narrowing
// folds; it only moves forward under the registry mutex (CAS).
type SessionLabel struct {
	Trust    Trust    `json:"trust"`
	Audience Audience `json:"audience"`
	Version  uint64   `json:"version"`
}

// StartLabel is the explicit initial label: trusted, public, 0. Never
// compare with == SessionLabel{}: the Go zero value is {trusted,self,0},
// not start. Use *SessionLabel nil-checks plus IsStart instead.
func StartLabel() SessionLabel {
	return SessionLabel{Trust: TrustTrusted, Audience: AudiencePublic}
}

// IsStart reports whether l is the widest (start) label. Version is
// ignored: a folded-then-unchanged label at version 0 and a never-folded
// label both count as start when trust/audience are widest.
func IsStart(l SessionLabel) bool {
	return l.Trust == TrustTrusted && l.Audience == AudiencePublic
}

// IsNilOrStart reports nil (uninitialized, fail-closed at call sites) as
// non-start: callers must fail closed on nil, never treat it as widest.
func IsNilOrStart(l *SessionLabel) bool {
	if l == nil {
		return false
	}
	return IsStart(*l)
}

// Meet is the senior-reviewed combine: trust by max, audience by min,
// version by max, bumped only when the merge actually narrows either
// dimension relative to a (with b's view folded in).
func Meet(a, b SessionLabel) SessionLabel {
	t := a.Trust
	if b.Trust > t {
		t = b.Trust
	}
	au := a.Audience
	if b.Audience < au {
		au = b.Audience
	}
	v := a.Version
	if b.Version > v {
		v = b.Version
	}
	if t != a.Trust || au != a.Audience {
		v++
	}
	return SessionLabel{Trust: t, Audience: au, Version: v}
}

// ParseTrust validates a trust enum.
func ParseTrust(s string) (Trust, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trusted":
		return TrustTrusted, nil
	case "suspicious":
		return TrustSuspicious, nil
	default:
		return 0, fmt.Errorf("permissions: invalid trust %q (want trusted|suspicious)", s)
	}
}

// ParseAudience validates an audience enum.
func ParseAudience(s string) (Audience, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "self":
		return AudienceSelf, nil
	case "internal":
		return AudienceInternal, nil
	case "public":
		return AudiencePublic, nil
	default:
		return 0, fmt.Errorf("permissions: invalid audience %q (want self|internal|public)", s)
	}
}

// FlowDelta is the JSON form of a rule delta: taints the future.
// Trust is a single enum, audience is a list folded to its minimum
// (most-narrowing) at compile time.
type FlowDelta struct {
	Trust    string   `json:"trust,omitempty"`
	Audience []string `json:"audience,omitempty"`
}

// FlowRequires is the JSON form of a rule gate: checked against the
// current label (meet call delta) before dispatch.
type FlowRequires struct {
	Trust            string   `json:"trust,omitempty"`
	AudienceContains []string `json:"audience_contains,omitempty"`
}

// compiledDelta is the validated delta: optional trust floor raise and
// optional audience ceiling lower.
type compiledDelta struct {
	hasTrust bool
	trust    Trust
	hasAud   bool
	aud      Audience
}

// compiledRequires is the validated gate.
type compiledRequires struct {
	hasTrust bool
	trust    Trust
	audMin   []Audience // each listed value; check passes when label >= min(list)
}

func compileDelta(d *FlowDelta, ctx string) (compiledDelta, error) {
	var out compiledDelta
	if d == nil {
		return out, nil
	}
	if d.Trust != "" {
		t, err := ParseTrust(d.Trust)
		if err != nil {
			return out, fmt.Errorf("permissions: %s: %w", ctx, err)
		}
		out.hasTrust, out.trust = true, t
	}
	if len(d.Audience) > 0 {
		min := AudiencePublic
		first := true
		for _, s := range d.Audience {
			a, err := ParseAudience(s)
			if err != nil {
				return out, fmt.Errorf("permissions: %s: %w", ctx, err)
			}
			if first || a < min {
				min = a
			}
			first = false
		}
		out.hasAud, out.aud = true, min
	}
	if !out.hasTrust && !out.hasAud {
		return out, fmt.Errorf("permissions: %s: empty delta (need trust and/or audience)", ctx)
	}
	return out, nil
}

func compileRequires(r *FlowRequires, ctx string) (compiledRequires, error) {
	var out compiledRequires
	if r == nil {
		return out, nil
	}
	if r.Trust != "" {
		t, err := ParseTrust(r.Trust)
		if err != nil {
			return out, fmt.Errorf("permissions: %s: %w", ctx, err)
		}
		out.hasTrust, out.trust = true, t
	}
	for _, s := range r.AudienceContains {
		a, err := ParseAudience(s)
		if err != nil {
			return out, fmt.Errorf("permissions: %s: %w", ctx, err)
		}
		out.audMin = append(out.audMin, a)
	}
	if !out.hasTrust && len(out.audMin) == 0 {
		return out, fmt.Errorf("permissions: %s: empty requires:{} rejected (need trust and/or audience_contains)", ctx)
	}
	return out, nil
}

// applyDelta folds one compiled delta into l, bumping Version only on a
// narrowing change. Pure; idempotency is enforced by the caller via the
// applied-set keyed by (Sequence, tool_call_id).
func applyDelta(l SessionLabel, d compiledDelta) SessionLabel {
	n := l
	if d.hasTrust && d.trust > n.Trust {
		n.Trust = d.trust
	}
	if d.hasAud && d.aud < n.Audience {
		n.Audience = d.aud
	}
	if n.Trust != l.Trust || n.Audience != l.Audience {
		if n.Version <= l.Version {
			n.Version = l.Version + 1
		} else {
			n.Version++
		}
	}
	return n
}

// unionDelta takes the most-narrowing union of deltas: max trust,
// min audience.
func unionDelta(ds []compiledDelta) compiledDelta {
	var out compiledDelta
	for _, d := range ds {
		if d.hasTrust && (!out.hasTrust || d.trust > out.trust) {
			out.hasTrust, out.trust = true, d.trust
		}
		if d.hasAud && (!out.hasAud || d.aud < out.aud) {
			out.hasAud, out.aud = true, d.aud
		}
	}
	return out
}

// checkRequires reports whether label satisfies every compiled gate.
// Trust gate: label.Trust <= required (requiring trusted is strictest).
// Audience gate: label.Audience >= min(listed) - requiring public is
// strictest; requiring self passes for every ceiling level (which is why
// unknown-sink fail-closed never uses a requires condition).
func checkRequires(label SessionLabel, rs []compiledRequires) bool {
	for _, r := range rs {
		if r.hasTrust && label.Trust > r.trust {
			return false
		}
		if len(r.audMin) > 0 {
			min := r.audMin[0]
			for _, a := range r.audMin[1:] {
				if a < min {
					min = a
				}
			}
			if label.Audience < min {
				return false
			}
		}
	}
	return true
}

// FoldLabel is the exported pure fold used by tests and the session
// registry: Meet semantics on a single delta with version bump on narrow.
func FoldLabel(l SessionLabel, d FlowDelta) (SessionLabel, error) {
	cd, err := compileDelta(&d, "fold")
	if err != nil {
		return l, err
	}
	return applyDelta(l, cd), nil
}

// FlowExperimentalEnabled reports whether version:2 policies load.
// Gate env: HAKASE_FLOW_EXPERIMENTAL=1 (or "true"). Until T2.1+T2.2 land,
// v2 stays experimental: Compile rejects version 2 with a loud error
// naming the gate so no v2 rule can authorize dispatch without flow.
func FlowExperimentalEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HAKASE_FLOW_EXPERIMENTAL")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// flowBinding records pre-dispatch (labelVersion, argsHash) so the
// post-admission re-check (Phase 2) can deny parallel read+send TOCTOU
// under mutex. Phase 1 records; Phase 2 enforces.
type flowBinding struct {
	labelVersion uint64
	argsHash     string
}

type flowBindingsT struct {
	sync.RWMutex
	m map[string]flowBinding
}

var flowBindings = flowBindingsT{m: make(map[string]flowBinding)}

// flowBindingKey scopes a binding by (sessionID, idemKey): flow keys
// repeat across sessions (same Sequence + tool_call_id shape), so a
// session-blind key would let a later fold overwrite an earlier
// session's version and hash.
func flowBindingKey(sessionID, idemKey string) string {
	return sessionID + "\x00" + idemKey
}

// RecordFlowBinding stores (labelVersion, argsHash) for
// (sessionID, idemKey). sessionID may be "" for session-agnostic
// pre-checks (the Phase 2 consumer records session-scoped keys).
func RecordFlowBinding(sessionID, idemKey string, labelVersion uint64, argsHash string) {
	if idemKey == "" {
		return
	}
	flowBindings.Lock()
	defer flowBindings.Unlock()
	flowBindings.m[flowBindingKey(sessionID, idemKey)] = flowBinding{labelVersion: labelVersion, argsHash: argsHash}
}

// FlowBindingFor returns the recorded binding for (sessionID, idemKey),
// if any.
func FlowBindingFor(sessionID, idemKey string) (uint64, string, bool) {
	flowBindings.RLock()
	defer flowBindings.RUnlock()
	b, ok := flowBindings.m[flowBindingKey(sessionID, idemKey)]
	if !ok {
		return 0, "", false
	}
	return b.labelVersion, b.argsHash, true
}
