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
// Layering (user < project < enterprise), file loading, and the enterprise
// sync land in Phase 2; this file is the pure engine plus the policy
// schema (JSON tags included so the loader reuses these types).
package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// against the command line (shell) or path (read/edit/...).
type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   Effect `json:"effect"`
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
	rule Rule
	re   *regexp.Regexp
}

// CompiledPolicy is a validated, match-ready Policy.
type CompiledPolicy struct {
	def    Effect
	rules  []compiledRule
	agents map[string][]compiledRule
}

// Compile validates a Policy and returns its match-ready form.
// An empty Default becomes EffectAsk; Version must be 0 or 1.
func Compile(p Policy) (*CompiledPolicy, error) {
	if p.Version != 0 && p.Version != 1 {
		return nil, fmt.Errorf("permissions: unsupported version %d (want 1)", p.Version)
	}
	def := p.Default
	if def == "" {
		def = EffectAsk
	} else if _, err := ParseEffect(string(def)); err != nil {
		return nil, err
	}
	cp := &CompiledPolicy{def: def}
	var err error
	if cp.rules, err = compileRules(p.Rules); err != nil {
		return nil, err
	}
	if len(p.Agents) > 0 {
		cp.agents = make(map[string][]compiledRule, len(p.Agents))
		for name, ap := range p.Agents {
			cr, err := compileRules(ap.Rules)
			if err != nil {
				return nil, fmt.Errorf("permissions: agent %q: %w", name, err)
			}
			cp.agents[strings.ToLower(name)] = cr
		}
	}
	return cp, nil
}

// compileRules validates and compiles one rule list.
func compileRules(rules []Rule) ([]compiledRule, error) {
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
		re, err := compilePattern(expandPattern(r.Resource))
		if err != nil {
			return nil, fmt.Errorf("permissions: rule %d: %w", i, err)
		}
		out = append(out, compiledRule{
			rule: Rule{Action: action, Resource: r.Resource, Effect: eff},
			re:   re,
		})
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
			if !cr.re.MatchString(res) {
				continue
			}
			if winner == nil || precedence(cr.rule.Effect) > precedence(winner.rule.Effect) {
				winner = cr
			}
		}
	}
	for _, res := range resources {
		v := res
		if filepath.IsAbs(v) {
			v = filepath.Clean(v)
		}
		consider(base, v)
		consider(extra, v)
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
// are passed through, everything else is quoted.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
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
