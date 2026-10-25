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
	"time"
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

// maxFlowBindings bounds the binding registry: it is a per-call
// advisory record, and a long-running process (the web server) must
// not grow it without limit. The oldest entries are dropped first.
const maxFlowBindings = 4096

// Lock ordering: flowBindings (RWMutex) and flowGrantStore (Mutex) are
// never held together. Callers record/consume one store at a time in
// idemKey shape "flow:<tool>:<argsHash>" (see agent.flowIdemKey); the
// pure policy evaluator (EvaluateWithLabel) records nothing, so no
// path nests these locks and no inversion is possible.
type flowBindingsT struct {
	sync.RWMutex
	m map[string]flowBinding
	// order keeps the insertion order so the oldest entry can be
	// dropped when the registry is full.
	order []string
	// evicted counts dropped entries (M3): a post-admission lookup miss
	// is ambiguous (never-recorded vs evicted under pressure), so the
	// re-check path reports the count as its audit marker instead of
	// guessing which case it is.
	evicted uint64
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
	key := flowBindingKey(sessionID, idemKey)
	if _, ok := flowBindings.m[key]; !ok {
		flowBindings.order = append(flowBindings.order, key)
	}
	flowBindings.m[key] = flowBinding{labelVersion: labelVersion, argsHash: argsHash}
	for len(flowBindings.order) > maxFlowBindings {
		oldest := flowBindings.order[0]
		flowBindings.order = flowBindings.order[1:]
		delete(flowBindings.m, oldest)
		flowBindings.evicted++
	}
}

// FlowBindingsEvicted reports how many bindings have been dropped under
// the capacity cap (M3 audit marker for the binding-missing re-check).
func FlowBindingsEvicted() uint64 {
	flowBindings.RLock()
	defer flowBindings.RUnlock()
	return flowBindings.evicted
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

// FlowGrantBinding binds one approved flow ask to the exact call it
// covers (S9 shared one-shot grant): the agent pre-dispatch mints it on
// human approval and the sandbox path gates consume it, so an approved
// Ask admits at the filesystem/exec layer instead of hard-denying.
// Keyed by (session, tool, argsHash, labelVersion) with NUL separators
// so no value forges another key. Hashes only, never raw args.
type FlowGrantBinding struct {
	SessionID    string
	Tool         string
	ArgsHash     string
	LabelVersion uint64
}

// canonicalFlowGrantTool normalizes tool spellings that share one
// execution path to a single grant identity (loop 5 single identity):
// system_exec_start shares BuildExecCommand with system_exec, and the
// generic grep tool shares the search_files implementation.
func canonicalFlowGrantTool(tool string) string {
	switch tool {
	case "system_exec_start":
		return "system_exec"
	case "grep":
		return "search_files"
	default:
		return tool
	}
}

func flowGrantKey(b FlowGrantBinding) string {
	return b.SessionID + "\x00" + canonicalFlowGrantTool(b.Tool) + "\x00" + b.ArgsHash + "\x00" +
		fmt.Sprintf("%d", b.LabelVersion)
}

// maxFlowGrants bounds the one-shot grant store (S17): grants are
// per-call records on a long-lived process (the web server), so the
// oldest entries are dropped first rather than growing without bound.
// A dropped grant fails closed (the call asks again) - the cap never
// widens access. Legacy pre-grants share the same TTL and cap via the
// agent resume store; the key domains stay separate (a legacy key never
// matches a flow binding and vice versa, ADR condition 3).
const maxFlowGrants = 1024

// flowGrantTTL bounds one approved flow ask (S17): a grant minted for an
// exact call expires 15 minutes after mint. Expiry is lazy on Has/Consume
// plus an opportunistic sweep on Grant (no goroutine, no sliding window:
// reads never extend the deadline).
const flowGrantTTL = 15 * time.Minute

// flowGrantEntry is one live one-shot grant with its mint deadline.
type flowGrantEntry struct {
	expires time.Time
}

var flowGrantStore = struct {
	sync.Mutex
	m       map[string]flowGrantEntry
	order   []string
	evicted uint64
}{m: make(map[string]flowGrantEntry)}

// sweepFlowGrantsLocked drops expired entries. Callers hold flowGrantStore.
func sweepFlowGrantsLocked(now time.Time) {
	for k, e := range flowGrantStore.m {
		if !now.Before(e.expires) {
			delete(flowGrantStore.m, k)
		}
	}
	if len(flowGrantStore.order) > len(flowGrantStore.m)*2 {
		kept := flowGrantStore.order[:0]
		for _, k := range flowGrantStore.order {
			if _, ok := flowGrantStore.m[k]; ok {
				kept = append(kept, k)
			}
		}
		flowGrantStore.order = kept
	}
}

// emptyVersionedGrant reports whether b must never satisfy a grant:
// an empty session is valid only for the session-less StartLabel
// carve-out (v1-parity callers bind labelVersion 0). Any versioned
// binding with no session fails closed on both mint and consume, so a
// grant minted for one session's narrowed label can never be picked up
// session-less.
func emptyVersionedGrant(b FlowGrantBinding) bool {
	return b.SessionID == "" && b.LabelVersion != 0
}

// GrantFlowBinding installs the one-shot grant for an approved flow ask.
func GrantFlowBinding(b FlowGrantBinding) {
	if emptyVersionedGrant(b) {
		return
	}
	flowGrantStore.Lock()
	defer flowGrantStore.Unlock()
	now := time.Now()
	sweepFlowGrantsLocked(now)
	k := flowGrantKey(b)
	if _, ok := flowGrantStore.m[k]; !ok {
		flowGrantStore.order = append(flowGrantStore.order, k)
	}
	flowGrantStore.m[k] = flowGrantEntry{expires: now.Add(flowGrantTTL)}
	for len(flowGrantStore.order) > maxFlowGrants {
		oldest := flowGrantStore.order[0]
		flowGrantStore.order = flowGrantStore.order[1:]
		delete(flowGrantStore.m, oldest)
		flowGrantStore.evicted++
	}
}

// FlowGrantsEvicted reports how many one-shot grants have been dropped
// under the capacity cap (audit marker: a missing grant is either
// never-minted or evicted, both fail closed to another ask).
func FlowGrantsEvicted() uint64 {
	flowGrantStore.Lock()
	defer flowGrantStore.Unlock()
	return flowGrantStore.evicted
}

// HasFlowBinding reports whether a grant for this exact call is pending.
// Expired grants report false (lazy expiry: the entry is dropped).
// Empty-session versioned bindings never match (mirror of the mint
// guard: a grant that could not be installed can never be observed).
func HasFlowBinding(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	flowGrantStore.Lock()
	defer flowGrantStore.Unlock()
	e, ok := flowGrantStore.m[flowGrantKey(b)]
	if !ok {
		return false
	}
	if !time.Now().Before(e.expires) {
		delete(flowGrantStore.m, flowGrantKey(b))
		return false
	}
	return true
}

// ConsumeFlowBinding reports and consumes a matching grant (one-shot).
// Expired grants report false (lazy expiry: the entry is dropped).
// Empty-session versioned bindings never match (mirror of the mint
// guard).
func ConsumeFlowBinding(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	return consumeFlowGrantByKey(flowGrantKey(b))
}

func consumeFlowGrantByKey(k string) bool {
	flowGrantStore.Lock()
	defer flowGrantStore.Unlock()
	e, ok := flowGrantStore.m[k]
	if !ok {
		return false
	}
	delete(flowGrantStore.m, k)
	// Drop the consumed key from the eviction order so the cap
	// counts live grants only (no leak, no premature evict).
	for i, q := range flowGrantStore.order {
		if q == k {
			flowGrantStore.order = append(flowGrantStore.order[:i], flowGrantStore.order[i+1:]...)
			break
		}
	}
	// An expired grant consumes as absent: the entry is dropped and the
	// consume reports false (fail-closed, no sliding window).
	if !time.Now().Before(e.expires) {
		return false
	}
	return true
}
