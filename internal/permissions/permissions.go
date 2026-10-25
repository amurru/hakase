// Package permissions implements the hakase permissions policy engine.
//
// A policy is an ordered list of opencode-style {action, resource, effect}
// triples plus a default effect. Evaluation is deny > ask > allow across
// all matching rules (never last-wins); a multi-resource evaluation
// denies when any resource denies. No match falls back to the default,
// which is "ask" when unset.
//
// Resource patterns are globs where "*" spans separators, "?" matches one
// non-separator character, and "[...]" classes pass through. Patterns
// undergo "~"/"$HOME" expansion, and absolute values are cleaned before
// matching so "/a/../b" matches "/b". Callers must canonicalize values
// first (e.g. via sandbox ResolveScopedPath); the engine only cleans.
//
// The Flow Guard extension (version 2 policies: delta, requires,
// sanitizable, and the session data-flow label) is inspired by the
// OpenAPPA APPA model (Agentic Permissions Policy Algebra), paper arxiv
// 2607.24625, MIT (c) 2026 Archestra Inc. No OpenAPPA code was copied:
// clean-room Go implementation, no TOML, no sidecar.
//
// Layering (user < project < enterprise), file loading, and the enterprise
// sync land in Phase 2; this file is the pure engine plus the policy
// schema (JSON tags included so the loader reuses these types).
package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Effect is a rule outcome: allow, ask, or deny.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectAsk   Effect = "ask"
	EffectDeny  Effect = "deny"
)

// ParseEffect validates a raw effect string.
func ParseEffect(s string) (Effect, error) {
	switch Effect(strings.ToLower(strings.TrimSpace(s))) {
	case EffectAllow:
		return EffectAllow, nil
	case EffectAsk:
		return EffectAsk, nil
	case EffectDeny:
		return EffectDeny, nil
	default:
		return "", fmt.Errorf("permissions: invalid effect %q (want allow|ask|deny)", s)
	}
}

// Rule is one {action, resource, effect} triple. Action names a tool
// domain ("shell", "read", "edit", "glob", "grep", "webfetch",
// "subagent") or "*" for all actions. Resource is a glob matched
// against the command line (shell) or path (read/edit/...). Source
// names the originating layer ("enterprise", "user", "project") and is
// set by the loader; it is never serialized (json:"-") and exists so
// audit entries can cite a rule's provenance.
type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   Effect `json:"effect"`
	Source   string `json:"-"`
	// Flow Guard v2 (FG-002): optional taint-the-future (delta) and
	// dispatch gate (requires). Only valid in version:2 policies;
	// version 0/1 with these keys is a compile error.
	Delta       *FlowDelta    `json:"delta,omitempty"`
	Requires    *FlowRequires `json:"requires,omitempty"`
	Sanitizable bool          `json:"sanitizable,omitempty"`
}

// PolicyRule is the audit citation of a deciding rule.
type PolicyRule struct {
	Source   string `json:"source,omitempty"`
	Action   string `json:"action,omitempty"`
	Resource string `json:"resource,omitempty"`
	Effect   string `json:"effect,omitempty"`
	// Flow carries the flow citation (requires, label snapshot,
	// argsHash) for audit.go. Empty when no flow check applied.
	Flow string `json:"flow,omitempty"`
}

// Citation renders a Rule as an audit citation (nil-safe).
func (r *Rule) Citation() PolicyRule {
	if r == nil {
		return PolicyRule{}
	}
	return PolicyRule{Source: r.Source, Action: r.Action, Resource: r.Resource, Effect: string(r.Effect)}
}

// Policy is the rule set plus the no-match default. Agents holds
// per-agent rule overlays merged at evaluation (Phase 2); Enterprise
// carries the enterprise-layer flags (Phase 2).
type Policy struct {
	Version    int                    `json:"version,omitempty"`
	Default    Effect                 `json:"default,omitempty"`
	Rules      []Rule                 `json:"rules"`
	Agents     map[string]AgentPolicy `json:"agents,omitempty"`
	Enterprise EnterprisePolicy       `json:"enterprise,omitempty"`
}

// AgentPolicy is a named rule overlay (e.g. "reviewer").
type AgentPolicy struct {
	Rules []Rule `json:"rules"`
}

// EnterprisePolicy carries enterprise-layer controls. AllowManagedOnly
// drops allow rules from lower layers; DisableBypass neutralizes
// approval.mode=allow; PolicyURL (or HAKASE_ENTERPRISE_POLICY_URL) is
// polled every PollMinutes (default 60).
type EnterprisePolicy struct {
	AllowManagedOnly bool   `json:"allow_managed_only,omitempty"`
	DisableBypass    bool   `json:"disable_bypass,omitempty"`
	PolicyURL        string `json:"policy_url,omitempty"`
	PollMinutes      int    `json:"poll_minutes,omitempty"`
}

// compiledRule pairs a Rule with its translated matcher.
type compiledRule struct {
	rule        Rule
	re          *regexp.Regexp
	hasDelta    bool
	delta       compiledDelta
	hasRequires bool
	requires    compiledRequires
}

// CompiledPolicy is a validated, match-ready Policy.
type CompiledPolicy struct {
	def     Effect
	rules   []compiledRule
	agents  map[string][]compiledRule
	version int
	// flow is true when the policy is version 2 (flow fields enabled).
	flow bool
}

// Version reports the policy version the compiled form was built from.
func (cp *CompiledPolicy) Version() int {
	if cp == nil {
		return 0
	}
	return cp.version
}

// FlowEnabled reports whether flow (v2) checks apply.
func (cp *CompiledPolicy) FlowEnabled() bool {
	return cp != nil && cp.flow
}

// Compile validates a Policy and returns its match-ready form.
// An empty Default becomes EffectAsk; Version must be 0, 1, or 2.
// Version 2 enables flow keys (delta/requires/sanitizable) but only when
// the flowExperimental gate is on (HAKASE_FLOW_EXPERIMENTAL=1); otherwise
// v2 is rejected fail-closed with an error naming the gate. Versions 0/1
// reject any flow keys.
func Compile(p Policy) (*CompiledPolicy, error) {
	if p.Version != 0 && p.Version != 1 && p.Version != 2 {
		return nil, fmt.Errorf("permissions: unsupported version %d (want 1)", p.Version)
	}
	if p.Version == 2 && !FlowExperimentalEnabled() {
		return nil, fmt.Errorf("permissions: version 2 requires the flowExperimental gate (set HAKASE_FLOW_EXPERIMENTAL=1); refusing to load v2 policy without flow enforcement")
	}
	def := p.Default
	if def == "" {
		def = EffectAsk
	} else if _, err := ParseEffect(string(def)); err != nil {
		return nil, err
	}
	cp := &CompiledPolicy{def: def, version: p.Version, flow: p.Version == 2}
	var err error
	if cp.rules, err = compileRules(p.Rules, p.Version); err != nil {
		return nil, err
	}
	if len(p.Agents) > 0 {
		cp.agents = make(map[string][]compiledRule, len(p.Agents))
		for name, ap := range p.Agents {
			cr, err := compileRules(ap.Rules, p.Version)
			if err != nil {
				return nil, fmt.Errorf("permissions: agent %q: %w", name, err)
			}
			cp.agents[strings.ToLower(name)] = cr
		}
	}
	return cp, nil
}

// compileRules validates and compiles one rule list.
func compileRules(rules []Rule, version int) ([]compiledRule, error) {
	var out []compiledRule
	for i, r := range rules {
		action := strings.ToLower(strings.TrimSpace(r.Action))
		if action == "" {
			return nil, fmt.Errorf("permissions: rule %d has empty action", i)
		}
		if strings.TrimSpace(r.Resource) == "" {
			return nil, fmt.Errorf("permissions: rule %d has empty resource", i)
		}
		eff, err := ParseEffect(string(r.Effect))
		if err != nil {
			return nil, fmt.Errorf("permissions: rule %d: %w", i, err)
		}
		hasFlowKeys := r.Delta != nil || r.Requires != nil || r.Sanitizable
		if version != 2 && hasFlowKeys && !IsV1DeltaRegistered(action) {
			return nil, fmt.Errorf("permissions: rule %d: flow keys (delta/requires/sanitizable) require version 2", i)
		}
		re, err := compilePattern(normalizeSeparators(expandPattern(r.Resource)))
		if err != nil {
			return nil, fmt.Errorf("permissions: rule %d: %w", i, err)
		}
		cr := compiledRule{
			rule: Rule{Action: action, Resource: r.Resource, Effect: eff, Source: r.Source, Delta: r.Delta, Requires: r.Requires, Sanitizable: r.Sanitizable},
			re:   re,
		}
		if r.Delta != nil {
			cd, err := compileDelta(r.Delta, fmt.Sprintf("rule %d delta", i))
			if err != nil {
				return nil, err
			}
			cr.hasDelta, cr.delta = true, cd
		}
		if r.Requires != nil {
			cq, err := compileRequires(r.Requires, fmt.Sprintf("rule %d requires", i))
			if err != nil {
				return nil, err
			}
			cr.hasRequires, cr.requires = true, cq
		}
		out = append(out, cr)
	}
	return out, nil
}

// Evaluate resolves one action against zero or more resources and
// returns the winning effect plus the rule that produced it (nil when
// the default applied). Precedence is deny > ask > allow across every
// matching rule on every resource; ties resolve to the earliest rule.
func (cp *CompiledPolicy) Evaluate(action string, resources ...string) (Effect, *Rule) {
	return cp.evaluateIn(cp.rules, nil, action, resources...)
}

// EvaluateForAgent merges the base rules with the named agent's overlay
// (unknown agent = base only) under the same deny > ask > allow
// precedence.
func (cp *CompiledPolicy) EvaluateForAgent(agentName, action string, resources ...string) (Effect, *Rule) {
	var extra []compiledRule
	if cp != nil {
		extra = cp.agents[strings.ToLower(agentName)]
	}
	return cp.evaluateIn(cp.rules, extra, action, resources...)
}

// matchCandidates returns the spellings a resource matches against.
// H6: on Windows the volume (drive letter) is stripped as a fallback
// candidate so "/specific/*" author-facing globs keep matching
// "D:/specific/..." absolute values; the (?i) pattern prefix already
// folds case.
func matchCandidates(res string) []string {
	v := normalizeSeparators(res)
	if filepath.IsAbs(v) {
		v = filepath.Clean(v)
		v = normalizeSeparators(v)
	}
	if runtime.GOOS != "windows" {
		return []string{v}
	}
	vol := filepath.VolumeName(v)
	if vol == "" {
		// normalizeSeparators turned \ into /: VolumeName needs the
		// OS form, so retry on the raw value.
		if vol2 := filepath.VolumeName(res); vol2 != "" {
			trimmed := strings.TrimPrefix(v, normalizeSeparators(vol2))
			if trimmed == "" {
				trimmed = "/"
			}
			if trimmed != v {
				return []string{v, trimmed}
			}
		}
		return []string{v}
	}
	trimmed := strings.TrimPrefix(v, vol)
	if trimmed == "" {
		trimmed = "/"
	}
	if trimmed == v {
		return []string{v}
	}
	return []string{v, trimmed}
}

// evaluateIn is the shared matcher over base plus optional overlay rules.
func (cp *CompiledPolicy) evaluateIn(base, extra []compiledRule, action string, resources ...string) (Effect, *Rule) {
	action = strings.ToLower(strings.TrimSpace(action))
	var winner *compiledRule
	consider := func(rules []compiledRule, res string) {
		for i := range rules {
			cr := &rules[i]
			if cr.rule.Action != "*" && cr.rule.Action != action {
				continue
			}
			matched := false
			for _, cand := range matchCandidates(res) {
				if cr.re.MatchString(cand) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if winner == nil || precedence(cr.rule.Effect) > precedence(winner.rule.Effect) {
				winner = cr
			}
		}
	}
	for _, res := range resources {
		consider(base, res)
		consider(extra, res)
	}
	if winner == nil {
		return cp.def, nil
	}
	r := winner.rule
	return r.Effect, &r
}

// precedence ranks effects for deny > ask > allow comparison.
func precedence(e Effect) int {
	switch e {
	case EffectDeny:
		return 2
	case EffectAsk:
		return 1
	default:
		return 0
	}
}

// EvaluateWithLabel resolves one action against resources under a flow
// label (FG-003). Without a v2 policy (flow disabled) it is byte-identical
// to Evaluate. With v2, flow can only escalate: a requires failure moves
// allow->ask (or ->deny when the requiring rule is deny), and a narrowed
// label with no mapped rule fails closed (ask, or deny when the default
// is deny). detail is the flow reason ("" when no flow check applied).
func (cp *CompiledPolicy) EvaluateWithLabel(label *SessionLabel, action string, resources ...string) (Effect, *Rule, string) {
	return cp.evaluateAgentWithLabel("", label, action, resources...)
}

// EvaluateForAgentWithLabel merges the named agent overlay under the label.
func (cp *CompiledPolicy) EvaluateForAgentWithLabel(agentName string, label *SessionLabel, action string, resources ...string) (Effect, *Rule, string) {
	return cp.evaluateAgentWithLabel(agentName, label, action, resources...)
}

func (cp *CompiledPolicy) evaluateAgentWithLabel(agentName string, label *SessionLabel, action string, resources ...string) (Effect, *Rule, string) {
	if cp == nil {
		return EffectAsk, nil, ""
	}
	var extra []compiledRule
	extra = cp.agents[strings.ToLower(agentName)]
	baseEff, baseRule := cp.evaluateIn(cp.rules, extra, action, resources...)
	if !cp.flow {
		return baseEff, baseRule, ""
	}
	effLabel := StartLabel()
	narrowed := false
	if label == nil {
		// Nil = uninitialized, fail-closed (L1): evaluate as narrowest.
		effLabel = SessionLabel{Trust: TrustSuspicious, Audience: AudienceSelf}
		narrowed = true
	} else if !IsStart(*label) {
		effLabel = *label
		narrowed = true
	}
	matched := cp.matchingRules(extra, action, resources...)
	if len(matched) == 0 {
		if !narrowed {
			return baseEff, baseRule, ""
		}
		// Unknown-sink fail-closed (C2) on a narrowed label. Start-label
		// unknown tools keep v1 behavior (handled above).
		detail := fmt.Sprintf("flow: unknown action %q on narrowed label (%s, v%d)", action, labelString(effLabel), effLabel.Version)
		if cp.def == EffectDeny {
			return EffectDeny, nil, detail
		}
		return EffectAsk, nil, detail
	}
	var deltas []compiledDelta
	var gates []compiledRequires
	denyGate := false
	for _, m := range matched {
		if m.hasDelta {
			deltas = append(deltas, m.delta)
		}
		if m.hasRequires {
			gates = append(gates, m.requires)
			if m.rule.Effect == EffectDeny {
				denyGate = true
			}
		}
	}
	effective := applyDelta(effLabel, unionDelta(deltas))
	if len(gates) > 0 && !checkRequires(effective, gates) {
		hash := ShortHash(append([]string{action}, resources...)...)
		detail := fmt.Sprintf("flow: %s blocked (%s, v%d) by permissions policy (argsHash %s)", action, labelString(effective), effective.Version, hash)
		if baseEff == EffectDeny || denyGate {
			return EffectDeny, baseRule, detail
		}
		return EffectAsk, baseRule, detail
	}
	return baseEff, baseRule, ""
}

// matchingRules returns every compiled rule (base + overlay) matching the
// action and any of the resources.
func (cp *CompiledPolicy) matchingRules(extra []compiledRule, action string, resources ...string) []compiledRule {
	action = strings.ToLower(strings.TrimSpace(action))
	var out []compiledRule
	consider := func(rules []compiledRule) {
		for i := range rules {
			cr := &rules[i]
			if cr.rule.Action != "*" && cr.rule.Action != action {
				continue
			}
			for _, res := range resources {
				matched := false
				for _, cand := range matchCandidates(res) {
					if cr.re.MatchString(cand) {
						matched = true
						break
					}
				}
				if matched {
					out = append(out, *cr)
					break
				}
			}
		}
	}
	consider(cp.rules)
	consider(extra)
	return out
}

// DeltaFor returns the most-narrowing union of deltas from every rule
// (base only) matching action+resources, for post-admission folding
// (FG-004, Phase 2). ok is false when no matched rule carries a delta or
// the policy is not flow-enabled. The union takes max trust / min
// audience across matches, mirroring evaluateAgentWithLabel.
func (cp *CompiledPolicy) DeltaFor(action string, resources ...string) (FlowDelta, bool) {
	if cp == nil {
		return FlowDelta{}, false
	}
	// V1-DELTA register (S25 guardrail exception): a non-flow policy
	// honors deltas only for explicitly registered actions; everything
	// else keeps exact v1 behavior (no folding).
	registeredOnly := !cp.flow
	if registeredOnly && !IsV1DeltaRegistered(action) {
		return FlowDelta{}, false
	}
	matched := cp.matchingRules(nil, action, resources...)
	var ds []compiledDelta
	for _, m := range matched {
		if m.hasDelta {
			ds = append(ds, m.delta)
		}
	}
	if len(ds) == 0 {
		return FlowDelta{}, false
	}
	u := unionDelta(ds)
	var out FlowDelta
	if u.hasTrust {
		if u.trust == TrustSuspicious {
			out.Trust = "suspicious"
		} else {
			out.Trust = "trusted"
		}
	}
	if u.hasAud {
		switch u.aud {
		case AudienceSelf:
			out.Audience = []string{"self"}
		case AudienceInternal:
			out.Audience = []string{"internal"}
		default:
			out.Audience = []string{"public"}
		}
	}
	return out, true
}

func labelString(l SessionLabel) string {
	trust := "trusted"
	if l.Trust == TrustSuspicious {
		trust = "suspicious"
	}
	aud := "public"
	switch l.Audience {
	case AudienceSelf:
		aud = "self"
	case AudienceInternal:
		aud = "internal"
	}
	return trust + "/" + aud
}

// normalizeSeparators canonicalizes values and patterns to forward
// slashes so author-facing `/` globs match Windows `\` paths.
func normalizeSeparators(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

// expandPattern applies "~" and "$HOME" expansion to a resource pattern.
func expandPattern(pattern string) string {
	if pattern == "~" || strings.HasPrefix(pattern, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + pattern[1:]
		}
	}
	if strings.HasPrefix(pattern, "$HOME") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + pattern[len("$HOME"):]
		}
	}
	return pattern
}

// compilePattern translates a glob into an anchored regexp: "*" spans
// separators, "?" matches one non-separator character, "[...]" classes
// are passed through, everything else is quoted. H6: on Windows the
// filesystem is case-insensitive, so patterns compile case-insensitive
// ((?i) prefix); POSIX stays case-sensitive.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	var sb strings.Builder
	if runtime.GOOS == "windows" {
		sb.WriteString("(?i)^")
	} else {
		sb.WriteString("^")
	}
	i := 0
	for i < len(pattern) {
		switch pattern[i] {
		case '*':
			sb.WriteString(".*")
			i++
		case '?':
			sb.WriteString("[^/]")
			i++
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("invalid pattern %q: unterminated [", pattern)
			}
			sb.WriteString(pattern[i : i+end+1])
			i += end + 1
		default:
			sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}
