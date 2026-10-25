// flowgate.go - Flow Guard Phase 2 dispatch + post-admission gates (FG-003/004).
//
// CheckPreFlow is the single pre-dispatch choke point for every ADK tool
// (built-ins, MCP toolsets, python/download/skill/sidekick/vision): it maps
// the tool call onto a policy (action, resources) pair and evaluates it
// under the session's flow label via permissions.LookupWithLabel. Flow only
// escalates, never widens; with no installed policy every call passes
// through unchanged (v1 parity).
//
// CheckPostFlow is the post-admission choke point wired into
// makeHookAfterToolCallback: on success it withholds/redacts before context
// admission and folds source deltas; on error it redacts + conservatively
// folds, then returns (nil, redactedErr) so the ADK failure is preserved
// with a redacted carrier (never a result override when runErr != nil,
// per docs/hooks/spec.md).
//
// MCP egress needs no separate wiring: MCP tools dispatch through the same
// ADK BeforeToolCallback, so allowlisted MCP tools that never hit an ask
// branch still hit flow here (M3). Unknown sinks fail closed on a narrowed
// label via EvaluateWithLabel (C2).
package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/tool"
)

// delegateFlowKey carries the child-run mutable label for delegate
// inheritance (FG-001/H4, Phase 2 T2.3).
type delegateFlowKey struct{}

// delegateFlowState is the child-run mutable label. The parent seeds it
// with a copy of its label; child pre/post checks fold it under mutex;
// the parent merges the terminal label with Meet at the end.
type delegateFlowState struct {
	mu    sync.Mutex
	label permissions.SessionLabel
}

// WithDelegateFlowState returns ctx carrying a mutable child label seeded
// from parent. A nil parent seeds the narrowest label when a v2 policy is
// installed (fail-closed, S4: an unregistered parent session must not
// widen the child) and the start label otherwise (v1 parity for
// session-less callers when no v2 policy is installed). Callers with an
// explicit session-less context should seed StartLabel themselves;
// nil here means "uninitialized, evaluate as narrowest" under v2.
func WithDelegateFlowState(ctx context.Context, parent *permissions.SessionLabel) context.Context {
	st := &delegateFlowState{}
	if parent != nil {
		st.label = *parent
	} else if permissions.FlowEnabled() {
		st.label = permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceSelf}
	} else {
		st.label = permissions.StartLabel()
	}
	return context.WithValue(ctx, delegateFlowKey{}, st)
}

// DelegateFlowStateFromCtx returns the mutable child state, or nil.
func DelegateFlowStateFromCtx(ctx context.Context) *delegateFlowState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(delegateFlowKey{}).(*delegateFlowState)
	return st
}

// FlowLabelForSandbox is the exported live label resolver for the
// sandbox FlowLabelFunc wiring (S9/H1): delegate child state first,
// then the session snapshot, then the registry - the same precedence
// as flowLabelForCtx. The sandbox package cannot import the agent
// (import cycle), so main wires this in.
func FlowLabelForSandbox(ctx context.Context) *permissions.SessionLabel {
	return flowLabelForCtx(ctx)
}

// flowSessionID resolves the hakase session for flow gating: the
// session-bound run when present, else "" (session-less callers keep v1
// behavior via the start label).
func flowSessionID(ctx context.Context) string {
	return interfaces.SessionIDFromCtx(ctx)
}

// flowLabelForCtx resolves the label for a pre-dispatch check. Precedence:
// delegate child state first (child runs inherit the parent label, never a
// fresh session), then the session registry, then the start label for
// session-less callers (v1 parity). A present-but-unregistered session
// returns nil: fail-closed (evaluated as narrowest by LookupWithLabel).
func flowLabelForCtx(ctx context.Context) *permissions.SessionLabel {
	if st := DelegateFlowStateFromCtx(ctx); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		cp := st.label
		return &cp
	}
	// Delegate ctx propagation (session.WithFlowLabel): the parent seeds a
	// snapshot copy for transports that do not share the mutable state.
	if l := hakasesession.FlowLabelFromCtx(ctx); l != nil {
		return l
	}
	id := flowSessionID(ctx)
	if id == "" {
		l := permissions.StartLabel()
		return &l
	}
	if l := hakasesession.DefaultRegistry.Get(id); l != nil {
		return l
	}
	return nil
}

// flowArgsHash hashes the canonical (tool, action, resources) triple for
// audit metadata and (labelVersion, argsHash) binding. Hashes only, never
// raw args (M2). Delegates to the shared permissions.FlowArgsHash so the
// agent pre-dispatch, the python gate, and the shell binding hash the
// same triple (S14/H2 single identity); the actual tool name (not the
// mapped action) is always the first element.
// canonicalFlowTool normalizes tool spellings that share one execution
// path to a single binding identity (S9/H1 loop 5): system_exec_start
// shares BuildExecCommand with system_exec (Tool system_exec), and the
// generic grep tool shares the search_files implementation. Both
// spellings bind one key so an approval for either admits at the
// sandbox gate.
func canonicalFlowTool(tool string) string {
	switch tool {
	case "system_exec_start":
		return "system_exec"
	case "grep":
		return "search_files"
	default:
		return tool
	}
}

func flowArgsHash(tool, action string, resources []string) string {
	// S14 single identity: the sandbox approval always carries
	// Tool:"system_exec" (sync and start share BuildExecCommand), so
	// the agent pre-dispatch must hash the same canonical tool or the
	// grant never matches. The grep/search_files pair binds the same
	// way (High loop 5).
	tool = canonicalFlowTool(tool)
	return permissions.FlowArgsHash(tool, action, resources)
}

// flowIdemKey is the fold idempotency key scope: one fold per distinct
// tool call. Pure Meet semantics make re-folds of the same delta a no-op,
// so a hash-scoped key is sufficient (the (Sequence, tool_call_id) applied
// set persists at the session layer in Phase 3).
func flowIdemKey(tool, argsHash string) string {
	tool = canonicalFlowTool(tool)
	return "flow:" + tool + ":" + argsHash
}

// flowPathResources builds the dual-match resource list for a
// path-shaped operand (S5): canonical primary, raw fallback. The policy
// engine ORs across resources, so a rule matches either leg; the gate
// audits which leg fired via flowDebugf at the check sites. Identical
// legs collapse to one entry. Shared with the sandbox gates via
// permissions.PathResources (S9 exact-call threading): both sides hash
// the same identity.
func flowPathResources(raw string) []string {
	return permissions.PathResources(raw)
}

// flowAuditPathLeg attributes a dual-match decision to its leg for audit
// (S5): when a path action carried [canonical, raw] and a rule decided,
// it re-evaluates each leg alone (debug-gated, so zero cost in prod) and
// logs which leg fired by argsHash only, never paths. Rule authors use
// it to tighten rules toward the canonical form.
func flowAuditPathLeg(label *permissions.SessionLabel, action string, resources []string, rule *permissions.Rule, detail string) {
	if !flowDebugEnabled() {
		return
	}
	if len(resources) != 2 || resources[0] == resources[1] {
		return
	}
	if rule == nil && detail == "" {
		return
	}
	switch action {
	case "read", "edit", "grep", "glob", "shell":
	default:
		return
	}
	for i, leg := range resources {
		name := "raw"
		if i == 0 {
			name = "canonical"
		}
		_, legRule, legDetail, ok := permissions.LookupWithLabel(label, action, leg)
		if !ok {
			continue
		}
		if legRule != nil || legDetail != "" {
			flowDebugf("dual-match action=%s leg=%s argsHash=%s", action, name, permissions.ShortHash(action, leg))
		}
	}
}

// strArg extracts a string arg, "" when absent/wrong type.
func strArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := args[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// truncateRes caps one resource value for policy matching (policies match
// prefixes/globs; unbounded bodies waste matcher time, never change the
// verdict shape).
func truncateRes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// flowMapTool maps (tool, args) onto a policy (action, resources) pair per
// the FG-002 canonical table. Unknown tools pass through with the tool
// name as action and best-effort string resources: on a narrowed label an
// unmapped action fails closed via EvaluateWithLabel (C2); on the start
// label behavior is v1-identical.
func flowMapTool(toolName string, args map[string]any) (action string, resources []string) {
	tool := strings.ToLower(strings.TrimSpace(toolName))
	if args == nil {
		args = map[string]any{}
	}
	switch tool {
	case "system_exec", "system_exec_start":
		cmd := strArg(args, "command")
		var argv []string
		if raw, ok := args["args"].([]any); ok {
			for _, a := range raw {
				if s, ok := a.(string); ok {
					argv = append(argv, s)
				}
			}
		}
		// S14 single identity: the shell resources are dual
		// (ShellLine primary + bare command fallback, M4) - the same
		// strings the sandbox gate and the ask binding match.
		return "shell", permissions.ShellResources(cmd, argv)
	case "read_file":
		return "read", flowPathResources(strArg(args, "path"))
	case "write_file", "patch":
		return "edit", flowPathResources(strArg(args, "path"))
	case "search_files":
		p := strArg(args, "path")
		if p == "" {
			p = "."
		}
		return "grep", flowPathResources(p)
	case "python_interpreter":
		code := strArg(args, "code")
		res := []string{"code_hash:" + permissions.ShortHash(code)}
		res = append(res, pythonFileOperands(code)...)
		return "python_interpreter", res
	case "download_file":
		u := strArg(args, "url")
		return "download_file", []string{permissions.NormalizeURL(u)}
	case "ask_sidekick":
		q := strArg(args, "question")
		return "ask_sidekick", []string{truncateRes(q, 2000), "question_hash:" + permissions.ShortHash(q)}
	case "save_skill":
		name := strArg(args, "name")
		return "save_skill", flowPathResources("skills/" + name + ".py")
	case "vision":
		u := strArg(args, "image_url", "imageUrl", "url", "path")
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "http") {
			return "vision", []string{permissions.NormalizeURL(u)}
		}
		if u != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "http") {
			return "vision", flowPathResources(u)
		}
		return "vision", []string{u}
	case "send_message":
		to := strArg(args, "recipient", "channel", "to", "chat", "user")
		body := strArg(args, "body", "text", "message", "content")
		if to == "" {
			to = "*"
		}
		return "send_message", []string{to, "body_hash:" + permissions.ShortHash(body)}
	case "webfetch", "web_fetch":
		u := strArg(args, "url")
		return "webfetch", []string{permissions.NormalizeURL(u)}
	case "web_search", "web_search_fallback":
		// Search snippets are untrusted ingress like a fetch: they taint
		// trust under a v2 webfetch rule instead of failing closed as an
		// unknown sink (H3). The web_search_fallback toolset name is
		// accepted too: ADK surfaces the toolset name on some dispatch
		// paths while the tools themselves register as web_search, and
		// both spellings carry the same query-shaped ingress. The query
		// text mirrors the ask_sidekick question shape (truncated text +
		// hash, never raw args in audit).
		q := strArg(args, "query", "q")
		return "webfetch", []string{truncateRes(q, 2000), "query_hash:" + permissions.ShortHash(q)}
	case "glob":
		return "glob", flowPathResources(strArg(args, "pattern", "path"))
	case "grep":
		return "grep", flowPathResources(strArg(args, "path", "pattern"))
	case "edit":
		return "edit", flowPathResources(strArg(args, "path"))
	case "read":
		return "read", flowPathResources(strArg(args, "path"))
	case "shell":
		cmd := strArg(args, "command")
		// Low: the generic shell tool shares the ShellLine identity
		// so its rules match the same string the exec gate enforces.
		return "shell", []string{permissions.ShellLine(cmd, nil)}
	case "delegate_task":
		// Delegation itself is not an egress sink, but a narrowed label
		// must not silently spawn network-capable children: evaluate the
		// goal as a question-gate resource so policies can Ask here (H4).
		// The child toolset still enforces per-call (child egress denied
		// by the parent label via the delegate state).
		goal := strArg(args, "goal")
		return "delegate_task", []string{truncateRes(goal, 2000)}
	default:
		// Generic/MCP tools: action = tool name, resources = best-effort
		// string operands. Unmapped actions on a narrowed label fail
		// closed (C2); on start they keep v1 behavior.
		var res []string
		for _, k := range []string{"path", "url", "command", "question", "recipient", "channel", "to", "image_url", "name", "goal", "prompt"} {
			if s := strArg(args, k); s != "" {
				if k == "url" {
					s = permissions.NormalizeURL(s)
				}
				res = append(res, truncateRes(s, 2000))
			}
		}
		return tool, res
	}
}

// pythonFileOperands extracts best-effort file operands from python code
// for policy matching: single/double-quoted tokens that look like paths.
// Never denies on substring alone (M4): this only feeds resources to the
// glob matcher, and matching still requires a policy rule.
func pythonFileOperands(code string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 500 || seen[s] {
			return
		}
		seen[s] = true
		// Normalize `a/../b` the same way the gate canonicalizes
		// path operands (S5 identity): the engine only Cleans absolute
		// values, so a raw relative token would miss its rule.
		if strings.Contains(s, "/") {
			s = filepath.Clean(s)
		}
		out = append(out, s)
	}
	// Scan quoted tokens; keep those shaped like paths.
	for i := 0; i < len(code); i++ {
		q := code[i]
		if q != '"' && q != '\'' {
			continue
		}
		j := i + 1
		for j < len(code) && code[j] != q {
			if code[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(code) {
			break
		}
		tok := code[i+1 : j]
		i = j
		if strings.Contains(tok, "/") || strings.HasSuffix(tok, ".py") ||
			strings.HasSuffix(tok, ".env") || strings.HasSuffix(tok, ".md") ||
			strings.HasSuffix(tok, ".json") || strings.HasSuffix(tok, ".txt") {
			add(tok)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// flowBlockResult is the model-legible denial returned as the tool result
// so the block lands in the audit/canvas trail like any other outcome
// (mirrors hooks.blockResult, blocked_by names the flow guard). The
// reason is redacted: it may quote a rule resource, and resources can
// carry secrets.
func flowBlockResult(toolName, reason string) map[string]any {
	if reason == "" {
		reason = "blocked by flow guard"
	}
	reason, _ = util.RedactSecrets(reason)
	// S26: neutralize live markup in the model-legible denial.
	reason = neutralizeFlowText(reason)
	// Low: the tool name rides the same model-legible surface, so it is
	// neutralized too (fixed tool names are clean, but the choke is
	// uniform and fail-closed on any future caller-controlled name).
	safeTool, _ := neutralizeFlowTags(toolName)
	return map[string]any{
		"error":      fmt.Sprintf("tool %q blocked by flow guard: %s", safeTool, reason),
		"blocked_by": "flow_guard",
		"tool":       safeTool,
	}
}

// CheckPreFlow evaluates one tool call under the flow label. ok=false means
// no installed policy (caller keeps existing behavior). blocked=true means
// the call must not run: result is the model-legible denial and effect is
// the flow verdict (ask or deny). Flow only escalates: base allows without
// a requires failure pass through, and RiskUnknown-style handling stays
// with the existing per-tool gates.
//
// Phase 3 (FG-005): a secret-shaped payload on a narrowed label is
// sanitized and re-checked when the deciding rule allows it
// (effect:allow + sanitizable:true on a narrow resource); otherwise the
// call escalates to the human ask.
func CheckPreFlow(ctx context.Context, toolName string, args map[string]any) (ok bool, blocked bool, result map[string]any, effect permissions.Effect, reason string) {
	return checkPreFlow(ctx, toolName, args, true)
}

// checkPreFlow is CheckPreFlow with an audit switch: the per-tool
// handlers re-check a call the ADK pre-dispatch callback already
// audited (and possibly asked), so the re-check must not write a second
// flow_blocked entry for the same call.
func checkPreFlow(ctx context.Context, toolName string, args map[string]any, audit bool) (ok bool, blocked bool, result map[string]any, effect permissions.Effect, reason string) {
	// Without a flow-enabled (v2) policy the flow path is inert: every
	// call passes through untouched so v1 behavior is byte-identical
	// (H1). A bare v1 default or a v1 rule verdict (allow/ask/deny) never
	// becomes a flow verdict here; the existing per-tool gates keep
	// enforcing the base policy exactly as before.
	if !permissions.FlowEnabled() {
		return false, false, nil, "", ""
	}
	action, resources := flowMapTool(toolName, args)
	label := flowLabelForCtx(ctx)
	eff, rule, detail, found := permissions.LookupWithLabel(label, action, resources...)
	if !found {
		return false, false, nil, "", ""
	}
	// S5 dual-match audit: log which leg (canonical vs raw) fired so
	// rule authors can tighten toward the canonical form. Debug-gated,
	// zero cost in prod.
	flowAuditPathLeg(label, action, resources, rule, detail)
	sessionID := flowSessionID(ctx)
	argsHash := flowArgsHash(toolName, action, resources)
	var labelVersion uint64
	if label != nil {
		labelVersion = label.Version
	}
	// A pre-v2 session resumed under a conservative label is audited once
	// (flow_recomputed, M1); the one-time notice rides the first denial.
	if audit && hakasesession.DefaultRegistry.TakeFlowRecomputed(sessionID) {
		AuditFlowRecomputed(sessionID, label)
	}
	// Bind (labelVersion, argsHash) for the post-admission re-check (H2):
	// parallel read+send in one turn is denied because send re-validates
	// against the folded label.
	permissions.RecordFlowBinding(sessionID, flowIdemKey(toolName, argsHash), labelVersion, argsHash)
	// Sanitize remedy (FG-005/H1): a secret-shaped payload on a narrowed
	// label is not admissible as-is. When the deciding rule allows a
	// sanitized payload, the guard redacts it and re-checks the policy
	// against the redacted resources; the redaction changes the payload
	// hash, so the binding is recorded again.
	tainted := flowPayloadTainted(label, action, args)
	sanitizedPayload := false
	if tainted && eff != permissions.EffectDeny {
		if redacted, reEff, reDetail, reRule := flowSanitizePass(label, toolName, action, args, rule); redacted {
			resources = flowResources(toolName, args)
			argsHash = flowArgsHash(toolName, action, resources)
			permissions.RecordFlowBinding(sessionID, flowIdemKey(toolName, argsHash), labelVersion, argsHash)
			eff, detail, rule = reEff, reDetail, reRule
			sanitizedPayload = true
			if audit {
				AuditFlowSanitized(toolName, action, argsHash, sessionID, label)
			}
			flowDebugf("sanitized tool=%s action=%s argsHash=%s", toolName, action, argsHash)
		}
	}
	// No flow signal and no deciding rule: v1 behavior (a bare policy
	// default alone never changes a verdict).
	if detail == "" && rule == nil {
		return true, false, nil, eff, ""
	}
	// Base deny always blocks; flow ask/deny (requires failure or
	// unknown-sink fail-closed) blocks with the flow reason.
	switch eff {
	case permissions.EffectDeny:
		r := "denied by permissions policy"
		if rule != nil {
			r += fmt.Sprintf(" (rule: %s %q)", rule.Action, rule.Resource)
		}
		if detail != "" {
			// The flow detail already names the policy; do not repeat it.
			r = detail
			if !strings.Contains(r, "by permissions policy") {
				r += " by permissions policy"
			}
		}
		if audit {
			AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
		}
		return true, true, flowDenialResult(ctx, toolName, action, label, eff), eff, r
	case permissions.EffectAsk:
		// A bare base ask without a deciding rule is the policy default:
		// leave it to the existing per-tool approval path (v1 parity).
		// A flow ask (requires failure / unknown sink) or a rule-backed
		// ask blocks here so it can never be auto-approved elsewhere.
		if detail == "" && rule == nil {
			return true, false, nil, eff, ""
		}
		r := "requires approval by permissions policy"
		if rule != nil {
			r += fmt.Sprintf(" (rule: %s %q)", rule.Action, rule.Resource)
		}
		if detail != "" {
			// The flow detail already names the policy; do not repeat it.
			r = detail
			if !strings.Contains(r, "by permissions policy") {
				r += " by permissions policy"
			}
		}
		// The taint is only worth naming when the payload still carries
		// it: a sanitized payload reached this point clean.
		if tainted && !sanitizedPayload && !strings.Contains(r, flowTaintReason) {
			r += " (" + flowTaintReason + ")"
		}
		if audit {
			AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
		}
		// This result is only a candidate: the flow Ask is routed to the
		// human, and the denial the model actually sees is built when
		// that answer comes back. The one-time resume notice rides that
		// denial, so it is not consumed here.
		return true, true, flowBlockResult(toolName, flowDenialText(action, label, eff)), eff, r
	default:
		// A secret-shaped payload that no rule marked sanitizable is
		// not admissible on a narrowed label: it needs the human (H1).
		if tainted && !sanitizedPayload && !flowSanitizableRule(rule) {
			r := flowTaintReason
			if audit {
				AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
			}
			return true, true, flowDenialResult(ctx, toolName, action, label, permissions.EffectAsk), permissions.EffectAsk, r
		}
		return true, false, nil, eff, ""
	}
}

// checkPreFlowVerdict re-evaluates one call under the flow label
// without audit side effects (the ADK pre-dispatch callback already
// audited the same call and, for a flow Ask, routed it to the human).
// blocked=true means the flow verdict stops the call: effect is deny
// (refuse) or ask (already asked - do not prompt twice).
func checkPreFlowVerdict(ctx context.Context, toolName string, args map[string]any) (blocked bool, effect permissions.Effect, reason string) {
	ok, blocked, _, eff, reason := checkPreFlow(ctx, toolName, args, false)
	if !ok {
		return false, "", ""
	}
	return blocked, eff, reason
}

// foldFlowLabel folds delta into the session label (or the delegate child
// state when present) and best-effort persists the registry back to the
// session store. Sources fold on success AND error (C1); denies never fold.
func foldFlowLabel(ctx context.Context, delta permissions.FlowDelta, idemKey, argsHash string) {
	if st := DelegateFlowStateFromCtx(ctx); st != nil {
		st.mu.Lock()
		next, err := permissions.FoldLabel(st.label, delta)
		if err == nil {
			st.label = next
		}
		st.mu.Unlock()
		permissions.RecordFlowBinding(flowSessionID(ctx), idemKey, st.label.Version, argsHash)
		return
	}
	id := flowSessionID(ctx)
	if id == "" {
		return
	}
	hakasesession.DefaultRegistry.Fold(id, delta, idemKey, argsHash)
	persistFlowLabel(id)
}

// persistFlowLabel best-effort syncs the registry label back into the
// session store so resume keeps restrictions. Never fails the tool call:
// persistence errors only debug-log. The load is resume-only (it never
// imports over live folds, C1); the sync then writes the live label.
func persistFlowLabel(sessionID string) {
	if deps == nil || deps.SessionService == nil || sessionID == "" {
		return
	}
	defer func() { _ = recover() }()
	sess, err := deps.SessionService.Store().Load(sessionID)
	if err != nil || sess == nil {
		return
	}
	hakasesession.DefaultRegistry.Sync(sess)
	_ = deps.SessionService.Store().Save(sess)
}

// flowWalkCaps bounds the reflective result walker (S3, ADR NFR
// budgets): the walker runs on every post-admission result when v2 is
// enabled, so unbounded reflect recursion is a CPU + alloc regression on
// the hot path plus a panic surface (cycles, unexported fields).
// H3: over-budget admissions return the truncated subset (first
// flowWalkMaxStrings strings / flowWalkMaxBytes bytes) plus a refine
// note instead of a full withhold, so the model can narrow the query.
const (
	flowWalkMaxDepth   = 8
	flowWalkMaxStrings = 512
	// flowWalkMaxBytes caps the total string bytes visited by the
	// marshal/redact walkers (M2): a 1MB read result is scanned in
	// full, anything larger fails closed as over-budget instead of
	// burning unbounded CPU/alloc on the hot path.
	flowWalkMaxBytes = 1 << 20
)

// marshalResultStrings collects every string in a tool result for secret
// scanning (redact) and taint scanning (withhold). Fast path first: the
// type switch covers string | map[string]any | []any; typed ADK/MCP
// payloads ([]string, []map[string]string, structs) fall to the bounded
// reflective supplement.
func marshalResultStrings(result map[string]any) []string {
	out, _ := marshalResultStringsCapped(result)
	return out
}

// marshalResultStringsCapped is marshalResultStrings with an over-budget
// flag: true when depth/count caps truncated the walk. H3: the returned
// slice is the truncated subset (first flowWalkMaxStrings strings within
// flowWalkMaxBytes); callers admit the subset with a refine note instead
// of a full withhold. Never skip (fail-closed, S3).
func marshalResultStringsCapped(result map[string]any) ([]string, bool) {
	var out []string
	var pending []any
	over := false
	count := 0
	scanned := 0
	// budget charges one collected string against the count/byte caps
	// (M2): the fast path visits every string before the final
	// truncate, so the caps must apply DURING the walk, not after.
	budget := func(t string) bool {
		count++
		scanned += len(t)
		if count > flowWalkMaxStrings || scanned > flowWalkMaxBytes {
			over = true
			return false
		}
		return true
	}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if over || depth > flowWalkMaxDepth {
			if depth > flowWalkMaxDepth {
				over = true
			}
			return
		}
		switch t := v.(type) {
		case string:
			if !budget(t) {
				return
			}
			out = append(out, t)
		case []any:
			for _, e := range t {
				if over {
					return
				}
				walk(e, depth+1)
			}
		case map[string]any:
			for _, e := range t {
				if over {
					return
				}
				walk(e, depth+1)
			}
		case nil:
		default:
			pending = append(pending, v)
		}
	}
	for _, v := range result {
		if over {
			break
		}
		walk(v, 0)
	}
	if len(pending) > 0 {
		if reflectAppendStrings(pending, &out) {
			over = true
		}
	}
	if len(out) > flowWalkMaxStrings {
		out = out[:flowWalkMaxStrings]
		over = true
	}
	return out, over
}

// reflectAppendStrings appends strings found under roots via reflection
// (S3 supplement for typed slices/maps/structs/pointers/[]byte). Bounded
// by flowWalkMaxDepth/flowWalkMaxStrings; never follows unexported struct
// fields; panics are recovered and reported as over-budget (fail-closed).
// Never logs field names or values (S3 compliance: reflection must not
// leak secret-bearing content on panic paths).
func reflectAppendStrings(roots []any, out *[]string) (over bool) {
	defer func() {
		if recover() != nil {
			over = true
		}
	}()
	count := len(*out)
	scanned := 0
	for _, have := range *out {
		scanned += len(have)
	}
	// charge counts one collected string against the count/byte caps
	// (M2); strings beyond the caps trip over-budget (fail-closed).
	charge := func(t string) bool {
		count++
		scanned += len(t)
		if count > flowWalkMaxStrings || scanned > flowWalkMaxBytes {
			over = true
			return false
		}
		return true
	}
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if over || depth > flowWalkMaxDepth {
			if depth > flowWalkMaxDepth {
				over = true
			}
			return
		}
		if !v.IsValid() {
			return
		}
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			v = v.Elem()
			depth++
			if depth > flowWalkMaxDepth {
				over = true
				return
			}
		}
		switch v.Kind() {
		case reflect.String:
			got := v.String()
			if !charge(got) {
				return
			}
			*out = append(*out, got)
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				// Named byte-slice types (type B []byte) fail the
				// []byte assertion; Bytes() covers them (M1).
				func() {
					defer func() { _ = recover() }()
					if v.Kind() != reflect.Slice || v.IsNil() {
						return
					}
					got := string(v.Bytes())
					if !charge(got) {
						return
					}
					*out = append(*out, got)
				}()
				return
			}
			for i := 0; i < v.Len(); i++ {
				if over {
					return
				}
				walk(v.Index(i), depth+1)
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if over {
					return
				}
				mv := iter.Value()
				if !mv.IsValid() || !mv.CanInterface() {
					continue
				}
				walk(mv, depth+1)
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if over {
					return
				}
				if t.Field(i).PkgPath != "" {
					continue // unexported: never follow
				}
				fv := v.Field(i)
				if !fv.CanInterface() {
					continue
				}
				walk(fv, depth+1)
			}
		}
	}
	for _, r := range roots {
		if over {
			break
		}
		walk(reflect.ValueOf(r), 0)
	}
	return over
}

// redactResultInPlace replaces secret-shaped substrings with typed tokens
// across every string in the result map. Reports whether anything fired.
// Fast path first (string | map[string]any | []any, now including string
// and nested-slice elements); typed shapes fall to the bounded
// reflective supplement.
func redactResultInPlace(result map[string]any) bool {
	fired, _ := redactResultInPlaceCapped(result)
	return fired
}

// redactResultInPlaceCapped is redactResultInPlace with an over-budget
// flag (same fail-closed contract as the marshal cap).
func redactResultInPlaceCapped(result map[string]any) (fired, over bool) {
	return redactResultInPlaceCappedGated(result, true)
}

// redactResultInPlaceCappedGated redacts with an optional reflective
// supplement. reflective=false skips the reflect paths entirely (M2
// IsStart gate): the widest label never taints, so the hot-path walk
// stays on the fast type switch. Skipping is NOT over-budget (the walk
// is complete for the shapes it covers, and start admits anyway).
func redactResultInPlaceCappedGated(result map[string]any, reflective bool) (fired, over bool) {
	defer func() {
		if recover() != nil {
			over = true
		}
	}()
	budget := &flowRedactBudget{reflective: reflective}
	var walk func(m map[string]any, depth int)
	walk = func(m map[string]any, depth int) {
		if over || depth > flowWalkMaxDepth {
			if depth > flowWalkMaxDepth {
				over = true
			}
			return
		}
		for k, v := range m {
			if over {
				return
			}
			switch t := v.(type) {
			case string:
				if !budget.charge(len(t), &over) {
					return
				}
				if red, ok := util.RedactSecrets(t); ok {
					m[k] = red
					fired = true
				}
			case map[string]any:
				walk(t, depth+1)
			case []any:
				for i, e := range t {
					if over {
						break
					}
					switch em := e.(type) {
					case string:
						if !budget.charge(len(em), &over) {
							break
						}
						if red, ok := util.RedactSecrets(em); ok {
							t[i] = red
							fired = true
						}
					case map[string]any:
						walk(em, depth+1)
					default:
						if !reflective {
							continue
						}
						sub := map[string]any{"v": em}
						if reflectRedactValue(reflect.ValueOf(sub), depth+1, &over, budget) {
							fired = true
						}
						if nv, ok := sub["v"]; ok {
							t[i] = nv
						}
					}
				}
			default:
				if !reflective {
					continue
				}
				rv := reflect.ValueOf(map[string]any{"v": v})
				if reflectRedactValue(rv, depth+1, &over, budget) {
					fired = true
				}
				if nv, ok := rv.Interface().(map[string]any)["v"]; ok {
					m[k] = nv
				}
			}
		}
	}
	walk(result, 0)
	if budget.over {
		over = true
	}
	return fired, over
}

// flowRedactBudget carries the M2 count/byte caps through the redact
// walk (fast path + reflective supplement share it).
type flowRedactBudget struct {
	count      int
	bytes      int
	over       bool
	reflective bool
}

// charge accounts one scanned string; false when the caps trip (the
// caller stops the walk and reports over-budget).
func (b *flowRedactBudget) charge(n int, over *bool) bool {
	b.count++
	b.bytes += n
	if b.count > flowWalkMaxStrings || b.bytes > flowWalkMaxBytes {
		b.over = true
		*over = true
		return false
	}
	return true
}

// reflectStringValue dereferences interfaces and reports the string.
func reflectStringValue(v reflect.Value) (string, bool) {
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.String || !v.CanInterface() {
		return "", false
	}
	return v.String(), true
}

// reflectBytesValue dereferences interfaces and reports a []byte slice.
func reflectBytesValue(v reflect.Value) ([]byte, bool) {
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8 || v.IsNil() {
		return nil, false
	}
	if !v.CanInterface() {
		return nil, false
	}
	// Value.Bytes covers named byte-slice types (type B []byte), where
	// the []byte type assertion fails (M1).
	defer func() {
		_ = recover()
	}()
	return v.Bytes(), true
}

// reflectRedactValue redacts secret-shaped strings under v in place (S3
// supplement). Only settable strings are rewritten; unaddressable struct
// string fields are skipped best-effort (their reference-typed siblings
// are still visited). over is set on depth/count/byte exhaustion, on a
// secret that cannot be stored back (incomplete redact, M1), or when the
// value graph needs visited-state the depth cap already bounds (cycles
// terminate at flowWalkMaxDepth).
func reflectRedactValue(v reflect.Value, depth int, over *bool, budget *flowRedactBudget) (fired bool) {
	if budget == nil {
		budget = &flowRedactBudget{reflective: true}
	}
	if depth > flowWalkMaxDepth {
		*over = true
		return false
	}
	if !v.IsValid() {
		return false
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
		depth++
		if depth > flowWalkMaxDepth {
			*over = true
			return false
		}
	}
	switch v.Kind() {
	case reflect.String:
		if !budget.charge(len(v.String()), over) {
			return false
		}
		if !v.CanSet() {
			// Unaddressable (e.g. a struct value behind a map):
			// a secret here cannot be rewritten in place, so the
			// redact is incomplete (M1) - never silently partial.
			if _, ok := util.RedactSecrets(v.String()); ok {
				*over = true
			}
			return false
		}
		if red, ok := util.RedactSecrets(v.String()); ok {
			v.SetString(red)
			return true
		}
		return false
	case reflect.Pointer:
		if v.IsNil() {
			return false
		}
		return reflectRedactValue(v.Elem(), depth+1, over, budget)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.Kind() != reflect.Slice || v.IsNil() {
				return false
			}
			if cur, ok := reflectBytesValue(v); ok {
				if !budget.charge(len(cur), over) {
					return false
				}
				if red, ok := util.RedactSecrets(string(cur)); ok {
					if v.CanSet() {
						v.SetBytes([]byte(red))
						return true
					}
					*over = true
				}
			}
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if *over {
				return fired
			}
			if reflectRedactValue(v.Index(i), depth+1, over, budget) {
				fired = true
			}
		}
		return fired
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if *over {
				return fired
			}
			mv := v.MapIndex(k)
			if !mv.IsValid() || !mv.CanInterface() {
				continue
			}
			// Plain strings redact directly against the map: this
			// covers typed maps (map[string]string) whose values
			// the fast path never sees.
			if s, ok := reflectStringValue(mv); ok {
				if red, ok := util.RedactSecrets(s); ok {
					// Named string types (type S string) need a
					// Convert: SetMapIndex panics unless the value
					// is assignable to the element type.
					setReflectMapString(v, k, red, over)
					fired = true
				}
				continue
			}
			// []byte values redact by replacement the same way
			// (named byte-slice types Convert the same way).
			if cur, ok := reflectBytesValue(mv); ok {
				if red, ok := util.RedactSecrets(string(cur)); ok {
					setReflectMapBytes(v, k, []byte(red), over)
					fired = true
				}
				continue
			}
			// Struct/array values from a map are unaddressable copies
			// (usually behind an interface): redact an addressable
			// copy and store it back (exported fields only;
			// unexported survive via the copy). Slices, maps and
			// pointers share backing and mutate in place.
			uv := mv
			for uv.Kind() == reflect.Interface {
				if uv.IsNil() {
					break
				}
				uv = uv.Elem()
			}
			if uv.IsValid() && (uv.Kind() == reflect.Struct || uv.Kind() == reflect.Array) {
				if uv.CanInterface() {
					cp := reflect.New(uv.Type()).Elem()
					cp.Set(uv)
					if reflectRedactValue(cp, depth+1, over, budget) {
						setReflectMapValue(v, k, cp, over)
						fired = true
					}
				} else {
					// Cannot materialize the copy: the redact is
					// incomplete, never silently partial (M1).
					*over = true
				}
				continue
			}
			// Anything else recurses directly (never wrapped: the
			// map/slice/pointer header shares backing with the
			// original, so in-place edits land). Unaddressable
			// struct string fields are skipped inside; their
			// reference-typed siblings are still visited.
			if reflectRedactValue(mv, depth+1, over, budget) {
				fired = true
			}
		}
		return fired
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if *over {
				return fired
			}
			if t.Field(i).PkgPath != "" {
				continue // unexported: never follow
			}
			fv := v.Field(i)
			if !fv.CanInterface() {
				continue
			}
			if reflectRedactValue(fv, depth+1, over, budget) {
				fired = true
			}
		}
		return fired
	default:
		return false
	}
}

// setReflectMapString stores a redacted string into a map, converting to
// named string element types (type S string) instead of panicking (M1).
// When the value cannot be stored, over is set: the redact is incomplete
// (callers withhold rather than admit a partial redact on sources).
func setReflectMapString(v, k reflect.Value, red string, over *bool) {
	elem := v.Type().Elem()
	rv := reflect.ValueOf(red)
	if rv.Type().AssignableTo(elem) {
		v.SetMapIndex(k, rv)
		return
	}
	if rv.Type().ConvertibleTo(elem) {
		v.SetMapIndex(k, rv.Convert(elem))
		return
	}
	*over = true
}

// setReflectMapBytes stores redacted bytes into a map, converting to
// named byte-slice element types the same way (M1).
func setReflectMapBytes(v, k reflect.Value, red []byte, over *bool) {
	elem := v.Type().Elem()
	rv := reflect.ValueOf(red)
	if rv.Type().AssignableTo(elem) {
		v.SetMapIndex(k, rv)
		return
	}
	if rv.Type().ConvertibleTo(elem) {
		v.SetMapIndex(k, rv.Convert(elem))
		return
	}
	*over = true
}

// setReflectMapValue stores an addressable struct/array copy back into a
// map, converting when the element type is named (M1).
func setReflectMapValue(v, k, cp reflect.Value, over *bool) {
	elem := v.Type().Elem()
	if cp.Type().AssignableTo(elem) {
		v.SetMapIndex(k, cp)
		return
	}
	if cp.Type().ConvertibleTo(elem) {
		v.SetMapIndex(k, cp.Convert(elem))
		return
	}
	*over = true
}

// flowResultTainted reports whether a (redacted) result carries
// delta-tainted content addressed outward. Conservative Phase 2 rule: any
// secret-shaped content (even after redaction the token marks it) on a
// narrowed label is non-admissible without approval (H1). Secret-shaped
// detection runs on the PRE-redaction text; the marker left by redaction
// confirms it.
func flowResultTainted(label *permissions.SessionLabel, preStrings []string) bool {
	if label == nil || permissions.IsStart(*label) {
		return false
	}
	for _, s := range preStrings {
		if _, ok := util.RedactSecrets(s); ok {
			return true
		}
	}
	return false
}

// CheckPostFlow is the FG-004 post-admission check. Contract:
//
//   - runErr != nil: redact result + error string in place, conservatively
//     fold the tool delta, audit, and return (nil, redactedErr): the ADK
//     failure is preserved with a redacted carrier (never a result override,
//     per docs/hooks/spec.md, C1).
//   - success: fold the tool delta; when the (redacted) result carries
//     tainted content on a narrowed label, re-validate requires against the
//     current label (H2 TOCTOU: parallel read+send denied) and withhold when
//     it fails, returning an override so it never enters context (H1).
//   - otherwise (nil, nil): pass through.
func CheckPostFlow(ctx context.Context, t tool.Tool, args, result map[string]any, runErr error) (map[string]any, error) {
	toolName := ""
	if t != nil {
		toolName = t.Name()
	}
	// No flow-enabled (v2) policy: zero behavior change (v1 parity, H2).
	// In particular secrets are NOT redacted here without a v2 policy;
	// the audit-layer redaction in AuditCommandExec still applies. The
	// installed-policy check doubles as the no-policy check: a
	// flow-enabled policy is always installed when this passes.
	// Mapping runs only under v2 (M-1): keep pre-dispatch order
	// (FlowEnabled, flowMapTool, label).
	if !permissions.FlowEnabled() {
		return nil, nil
	}
	action, resources := flowMapTool(toolName, args)
	label := flowLabelForCtx(ctx)
	delta, hasDelta := permissions.DeltaForInstalled(action, resources...)
	idemKey := ""
	argsHash := ""
	if hasDelta || runErr != nil || result != nil {
		argsHash = flowArgsHash(toolName, action, resources)
		idemKey = flowIdemKey(toolName, argsHash)
	}

	if runErr != nil {
		// Error path (C1): redact + conservative fold, preserve failure.
		if result != nil {
			redactResultInPlace(result)
		}
		redacted, _ := util.RedactSecrets(runErr.Error())
		if hasDelta {
			foldFlowLabel(ctx, delta, idemKey, argsHash)
		}
		AuditFlowErrorRedacted(toolName, action, argsHash, flowSessionID(ctx), flowLabelForCtx(ctx))
		flowDebugf("error redacted tool=%s action=%s argsHash=%s", toolName, action, argsHash)
		return nil, errors.New(redacted)
	}

	if result == nil {
		// No result to admit: still fold source deltas (a source that
		// returns nothing still observed its input).
		if hasDelta {
			foldFlowLabel(ctx, delta, idemKey, argsHash)
		}
		return nil, nil
	}

	// Snapshot pre-redaction strings for the taint check, then redact.
	// IsStart short-circuit (S3): a start label never taints, so the
	// walk is skipped on the common widest-label path. Over-budget
	// walks fail closed as tainted + audit (S3 caps).
	var pre []string
	var walkOver bool
	if label == nil || !permissions.IsStart(*label) {
		pre, walkOver = marshalResultStringsCapped(result)
	}
	// M2 IsStart gate: the widest label never taints, so the reflective
	// supplement is skipped on the common start path (the fast type
	// switch still redacts). Skipping is not over-budget.
	sanitized, redactOver := redactResultInPlaceCappedGated(result, label == nil || !permissions.IsStart(*label))
	walkBudgetExceeded := walkOver || redactOver
	if walkBudgetExceeded {
		flowDebugf("walk over-budget tool=%s action=%s argsHash=%s", toolName, action, argsHash)
	}
	sessionID := flowSessionID(ctx)
	// auditSanitized records the redaction once (FG-006); withhold
	// records the block that keeps the redacted result out of the
	// context.
	auditSanitized := func() {
		if sanitized {
			AuditFlowSanitized(toolName, action, argsHash, sessionID, label)
		}
	}
	withhold := func(rule *permissions.Rule) map[string]any {
		auditSanitized()
		AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
		return flowWithholdOverride(toolName, action, label)
	}

	// Fold first so the re-check below sees the post-call label (the send
	// in a parallel read+send turn is denied because the read already
	// folded under the same mutex, H2). Re-resolve: label above is a
	// pre-fold copy.
	//
	// S6 fast path: the pre-check already applied this call's own delta,
	// so a version match between the pre-check binding and the pre-fold
	// label means no interleaving fold happened (TOCTOU impossible) and
	// the re-check below is skipped: one integer compare on the common
	// path. The binding is read BEFORE folding overwrites it.
	var preCheckVersion uint64
	hasPreBinding := false
	var postEntryVersion uint64
	postEntryHasVersion := false
	if label != nil {
		postEntryVersion, postEntryHasVersion = label.Version, true
	}
	if idemKey != "" {
		if v, _, ok := permissions.FlowBindingFor(sessionID, idemKey); ok {
			preCheckVersion, hasPreBinding = v, true
		}
	}
	// M3: an idemKey with no binding is either a call that never ran a
	// pre-check (direct tool path, python gate, session-less re-drive)
	// or a binding evicted under the capacity cap - the lookup alone
	// cannot tell them apart. Either way it is NOT a clean fast path:
	// on a narrowed label the call re-validates requires below
	// (fail-closed), with the eviction count as the audit marker. An
	// empty idemKey means nothing was bound by construction (no delta,
	// no result, no error) and stays skipped.
	bindingMissing := idemKey != "" && !hasPreBinding
	if hasDelta {
		foldFlowLabel(ctx, delta, idemKey, argsHash)
		label = flowLabelForCtx(ctx)
	}
	// Sidekick answers fold as suspicious ingress unconditionally (H4):
	// the answer is untrusted model text, independent of policy deltas.
	if action == "ask_sidekick" {
		foldFlowLabel(ctx, permissions.FlowDelta{Trust: "suspicious"}, idemKey+":answer", argsHash)
		label = flowLabelForCtx(ctx)
	}

	if label == nil {
		// Uninitialized session with an installed v2 policy: fail closed
		// on tainted content only (non-tainted results still pass; the
		// pre-dispatch gate already asked). An over-budget walk is
		// tainted too (S3 caps, fail-closed).
		if walkBudgetExceeded || flowResultTainted(&permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceSelf}, pre) {
			return withhold(nil), nil
		}
		auditSanitized()
		return nil, nil
	}
	// Over-budget walks fail closed, but only where taint matters: a
	// start label never taints (the walk above was already skipped), so
	// a redact-side overrun on widest stays admissible.
	budgetTainted := walkBudgetExceeded && !permissions.IsStart(*label)
	tainted := budgetTainted || flowResultTainted(label, pre)
	// S6 unconditional re-check: when the label moved between the
	// pre-check and admission, re-validate requires even for clean
	// (non-tainted) admissions - the pre-check passed under a stale
	// label (read-then-send in one turn). A missing binding on a
	// narrowed label re-validates the same way (M3: no pre-check proof,
	// evicted or never-recorded). Sources still admit redacted + audit
	// (their SEND asks at its own pre-dispatch); egress withholds.
	// No label move and a bound pre-check means TOCTOU was impossible:
	// skip the extra eval.
	narrowed := !permissions.IsStart(*label)
	if !tainted && ((hasPreBinding && postEntryHasVersion && postEntryVersion != preCheckVersion) ||
		(bindingMissing && narrowed)) {
		if bindingMissing {
			flowDebugf("binding missing tool=%s action=%s argsHash=%s evicted=%d", toolName, action, argsHash, permissions.FlowBindingsEvicted())
		}
		if eff, rule, detail, ok := permissions.LookupWithLabel(label, action, resources...); ok && (eff == permissions.EffectAsk || eff == permissions.EffectDeny || detail != "") {
			if !isEgressAction(action) {
				auditSanitized()
				flowDebugf("toctou admitted source tool=%s action=%s argsHash=%s", toolName, action, argsHash)
				return nil, nil
			}
			return withhold(rule), nil
		}
	}
	if tainted {
		// Re-validate requires against the CURRENT (folded) label: a
		// requires failure withholds even if the pre-check passed (H2).
		eff, rule, detail, ok := permissions.LookupWithLabel(label, action, resources...)
		if ok && (eff == permissions.EffectAsk || eff == permissions.EffectDeny || detail != "") {
			return withhold(rule), nil
		}
		// No requires on this action (a pure source): tainted source
		// output is still redacted (done above) and audited, but admitted:
		// the SEND will ask at its own pre-dispatch. Withhold only when
		// this result is addressed outward (sinks) - or when the redact
		// itself is incomplete (M1: over-budget/un-storable means the
		// admitted text is not proven clean, even for sources). H3:
		// over-budget admits the truncated subset with a refine note
		// instead of a full withhold; egress still withholds (with the
		// same refine note) so the model narrows the query.
		if walkBudgetExceeded {
			if isEgressAction(action) {
				// Egress over-budget withholds like any tainted sink, but
				// carries the H3 truncated subset + refine note so the
				// model narrows the query. Audit both the redaction and
				// the block, same as the withhold path below (an
				// unaudited withhold would leave no trail).
				auditSanitized()
				AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
				return flowBudgetOverride(toolName, action, label, pre), nil
			}
			auditSanitized()
			AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
			return flowBudgetAdmit(pre), nil
		}
		if isEgressAction(action) {
			// Sanitize remedy (FG-005/H1): a rule that marked the
			// resource sanitizable lets the redacted payload proceed -
			// the secret is gone, the destination is unchanged.
			if flowSanitizableRule(rule) {
				auditSanitized()
				flowDebugf("sanitized admit tool=%s action=%s argsHash=%s", toolName, action, argsHash)
				return nil, nil
			}
			return withhold(rule), nil
		}
		auditSanitized()
		return nil, nil
	}
	auditSanitized()
	return nil, nil
}

// flowWithholdOverride builds the non-admissible override (H1): the result
// never enters context; the model sees the withhold text with the allowed
// alternative instead.
func flowWithholdOverride(toolName, action string, label *permissions.SessionLabel) map[string]any {
	text := flowDenialText(action, label, permissions.EffectAsk)
	text, _ = util.RedactSecrets(text)
	// S26: denial text is already neutralized by flowDenialText; keep the
	// redact order (redact-then-neutralize) explicit for future edits.
	text = neutralizeFlowText(text)
	return map[string]any{
		"withheld":   true,
		"reason":     text,
		"blocked_by": "flow_guard",
		"tool":       toolName,
	}
}

// isEgressAction reports whether action addresses content outward (sinks).
// Sources (read/webfetch/grep/glob/shell) admit redacted results; their
// SEND asks at its own pre-dispatch.
func isEgressAction(action string) bool {
	switch action {
	case "send_message", "download_file", "save_skill", "ask_sidekick",
		"vision", "python_interpreter", "edit", "delegate_task":
		return true
	default:
		return false
	}
}

// CheckChannelEgress evaluates an outbound channel push (Telegram/Discord
// answer delivery) under the session label. allow=true keeps v1 behavior
// (no policy, start label, or explicit allow). Otherwise the caller must
// withhold: the channel answer carries no approval round-trip of its own,
// so the withheld notice names the ask-human remedy (FG-005).
func CheckChannelEgress(sessionID, channel, recipient string) (allow bool, reason string) {
	// Without a flow-enabled (v2) policy every push is allowed (v1
	// parity, H2): channel delivery never withholds on a v1 policy.
	if !permissions.FlowEnabled() {
		return true, ""
	}
	var label *permissions.SessionLabel
	if sessionID != "" {
		label = hakasesession.DefaultRegistry.Get(sessionID)
	}
	if label == nil && sessionID != "" {
		// Present-but-unregistered session: nil is fail-closed
		// (evaluated as narrowest). Any non-allow withholds - a flow
		// Ask included: the channel answer carries no approval
		// round-trip of its own, so an Ask can never be satisfied at
		// delivery (S1). A bare default with no deciding rule keeps v1.
		eff, rule, detail, ok := permissions.LookupWithLabel(nil, "send_message", recipient)
		if !ok || (detail == "" && rule == nil) {
			return true, ""
		}
		if eff == permissions.EffectAllow {
			return true, ""
		}
		if eff == permissions.EffectDeny {
			return false, flowDenialText("send_message", nil, permissions.EffectDeny)
		}
		return false, flowDenialText("send_message", nil, permissions.EffectAsk)
	}
	if label == nil {
		l := permissions.StartLabel()
		label = &l
	}
	eff, rule, detail, ok := permissions.LookupWithLabel(label, "send_message", recipient)
	if !ok || (detail == "" && rule == nil) {
		return true, ""
	}
	if eff == permissions.EffectAllow {
		return true, ""
	}
	return false, flowDenialText("send_message", label, eff)
}

// MergeDelegateLabel merges a finished child's terminal label into the
// parent session (H4/M5): Meet-only (narrowing), hash for audit only. The
// merge is atomic in the registry so a concurrent parent Fold cannot be
// overwritten by a stale snapshot (H2). Returns the merged label so
// callers bind the post-run cache key and entry to the same taint the
// parent now carries (a stale version would split the cache line).
func MergeDelegateLabel(parentSessionID string, child permissions.SessionLabel) permissions.SessionLabel {
	if parentSessionID == "" {
		return child
	}
	merged := hakasesession.DefaultRegistry.Merge(parentSessionID, child)
	persistFlowLabel(parentSessionID)
	return merged
}

// agentToolName safely reads a tool name (nil-safe for tests).
func agentToolName(t tool.Tool) string {
	if t == nil {
		return ""
	}
	defer func() { _ = recover() }()
	return t.Name()
}
