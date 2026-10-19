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
	"strings"
	"sync"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/tool"
)

// flowWithholdAlternative names the allowed alternative in withhold text.
const flowWithholdAlternative = "ask-human | sanitize | drop-result"

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
// from parent (nil parent seeds the start label).
func WithDelegateFlowState(ctx context.Context, parent *permissions.SessionLabel) context.Context {
	st := &delegateFlowState{}
	if parent != nil {
		st.label = *parent
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
// raw args (M2).
func flowArgsHash(tool, action string, resources []string) string {
	parts := append([]string{tool, action}, resources...)
	return permissions.ArgsHash(parts...)
}

// flowIdemKey is the fold idempotency key scope: one fold per distinct
// tool call. Pure Meet semantics make re-folds of the same delta a no-op,
// so a hash-scoped key is sufficient (the (Sequence, tool_call_id) applied
// set persists at the session layer in Phase 3).
func flowIdemKey(tool, argsHash string) string {
	return "flow:" + tool + ":" + argsHash
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
		line := strings.TrimSpace(cmd + " " + strings.Join(argv, " "))
		if line == "" {
			line = cmd
		}
		return "shell", []string{line}
	case "read_file":
		return "read", []string{strArg(args, "path")}
	case "write_file", "patch":
		return "edit", []string{strArg(args, "path")}
	case "search_files":
		p := strArg(args, "path")
		if p == "" {
			p = "."
		}
		return "grep", []string{p}
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
		return "save_skill", []string{"skills/" + name + ".py"}
	case "vision":
		u := strArg(args, "image_url", "imageUrl", "url", "path")
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "http") {
			return "vision", []string{permissions.NormalizeURL(u)}
		}
		return "vision", []string{u}
	case "send_message":
		to := strArg(args, "recipient", "channel", "to", "chat", "user")
		body := strArg(args, "body", "text", "message", "content")
		if to == "" {
			to = "*"
		}
		return "send_message", []string{to, "body_hash:" + permissions.ShortHash(body)}
	case "webfetch":
		u := strArg(args, "url")
		return "webfetch", []string{permissions.NormalizeURL(u)}
	case "glob":
		return "glob", []string{strArg(args, "pattern", "path")}
	case "grep":
		return "grep", []string{strArg(args, "path", "pattern")}
	case "edit":
		return "edit", []string{strArg(args, "path")}
	case "read":
		return "read", []string{strArg(args, "path")}
	case "shell":
		return "shell", []string{strArg(args, "command")}
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
// (mirrors hooks.blockResult, blocked_by names the flow guard).
func flowBlockResult(toolName, reason string) map[string]any {
	if reason == "" {
		reason = "blocked by flow guard"
	}
	return map[string]any{
		"error":      fmt.Sprintf("tool %q blocked by flow guard: %s", toolName, reason),
		"blocked_by": "flow_guard",
		"tool":       toolName,
	}
}

// flowDenialText renders the FG-005 denial shape (redacted inputs only;
// callers must pass hashes, never raw args).
func flowDenialText(action string, l *permissions.SessionLabel, argsHash string) string {
	ls := "narrowed"
	ver := uint64(0)
	if l != nil {
		trust := "trusted"
		if l.Trust == permissions.TrustSuspicious {
			trust = "suspicious"
		}
		aud := "public"
		switch l.Audience {
		case permissions.AudienceSelf:
			aud = "self"
		case permissions.AudienceInternal:
			aud = "internal"
		}
		ls = trust + "/" + aud
		ver = l.Version
	}
	return fmt.Sprintf("flow: %s blocked (%s v%d, argsHash %s). Allowed: %s",
		action, ls, ver, argsHash, flowWithholdAlternative)
}

// CheckPreFlow evaluates one tool call under the flow label. ok=false means
// no installed policy (caller keeps existing behavior). blocked=true means
// the call must not run: result is the model-legible denial and effect is
// the flow verdict (ask or deny). Flow only escalates: base allows without
// a requires failure pass through, and RiskUnknown-style handling stays
// with the existing per-tool gates.
func CheckPreFlow(ctx context.Context, toolName string, args map[string]any) (ok bool, blocked bool, result map[string]any, effect permissions.Effect, reason string) {
	action, resources := flowMapTool(toolName, args)
	label := flowLabelForCtx(ctx)
	eff, rule, detail, found := permissions.LookupWithLabel(label, action, resources...)
	if !found {
		return false, false, nil, "", ""
	}
	sessionID := flowSessionID(ctx)
	argsHash := flowArgsHash(toolName, action, resources)
	var labelVersion uint64
	if label != nil {
		labelVersion = label.Version
	}
	// Bind (labelVersion, argsHash) for the post-admission re-check (H2):
	// parallel read+send in one turn is denied because send re-validates
	// against the folded label.
	permissions.RecordFlowBinding(sessionID, flowIdemKey(toolName, argsHash), labelVersion, argsHash)
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
			r = detail + " by permissions policy"
		}
		AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
		return true, true, flowBlockResult(toolName, r), eff, r
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
			r = detail + " by permissions policy"
		}
		AuditFlowBlock(toolName, action, argsHash, sessionID, rule, label)
		return true, true, flowBlockResult(toolName, r), eff, r
	default:
		return true, false, nil, eff, ""
	}
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
// persistence errors only debug-log.
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

// marshalResultStrings collects every string in a tool result for secret
// scanning (redact) and taint scanning (withhold).
func marshalResultStrings(result map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	for _, v := range result {
		walk(v)
	}
	return out
}

// redactResultInPlace replaces secret-shaped substrings with typed tokens
// across every string in the result map. Reports whether anything fired.
func redactResultInPlace(result map[string]any) bool {
	fired := false
	var walk func(m map[string]any)
	walk = func(m map[string]any) {
		for k, v := range m {
			switch t := v.(type) {
			case string:
				if red, ok := util.RedactSecrets(t); ok {
					m[k] = red
					fired = true
				}
			case map[string]any:
				walk(t)
			case []any:
				for _, e := range t {
					if em, ok := e.(map[string]any); ok {
						walk(em)
					}
				}
			}
		}
	}
	walk(result)
	return fired
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
	action, resources := flowMapTool(toolName, args)
	// No installed policy: zero behavior change (v1 parity). In
	// particular secrets are NOT redacted here without a v2 policy; the
	// audit-layer redaction in AuditCommandExec still applies.
	label := flowLabelForCtx(ctx)
	if _, _, _, ok := permissions.LookupWithLabel(label, action, resources...); !ok {
		return nil, nil
	}
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
	pre := marshalResultStrings(result)
	sanitized := redactResultInPlace(result)

	// Fold first so the re-check below sees the post-call label (the send
	// in a parallel read+send turn is denied because the read already
	// folded under the same mutex, H2). Re-resolve: label above is a
	// pre-fold copy.
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
		// pre-dispatch gate already asked).
		if flowResultTainted(&permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceSelf}, pre) {
			return flowWithholdOverride(toolName, action, argsHash), nil
		}
		if sanitized {
			AuditFlowSanitized(toolName, action, argsHash, flowSessionID(ctx))
		}
		return nil, nil
	}
	if flowResultTainted(label, pre) {
		// Re-validate requires against the CURRENT (folded) label: a
		// requires failure withholds even if the pre-check passed (H2).
		if eff, _, detail, ok := permissions.LookupWithLabel(label, action, resources...); ok && (eff == permissions.EffectAsk || eff == permissions.EffectDeny || detail != "") {
			return flowWithholdOverride(toolName, action, argsHash), nil
		}
		// No requires on this action (a pure source): tainted source
		// output is still redacted (done above) and audited, but admitted:
		// the SEND will ask at its own pre-dispatch. Withhold only when
		// this result is addressed outward (sinks).
		if isEgressAction(action) {
			return flowWithholdOverride(toolName, action, argsHash), nil
		}
		if sanitized {
			AuditFlowSanitized(toolName, action, argsHash, flowSessionID(ctx))
		}
		return nil, nil
	}
	if sanitized {
		AuditFlowSanitized(toolName, action, argsHash, flowSessionID(ctx))
	}
	return nil, nil
}

// flowWithholdOverride builds the non-admissible override (H1): the result
// never enters context; the model sees the withhold text with the allowed
// alternative instead.
func flowWithholdOverride(toolName, action, argsHash string) map[string]any {
	l := permissions.StartLabel()
	text := flowDenialText(action, &l, argsHash)
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
// withhold (Phase 2: no approval round-trip here; approval reuse is T3.1).
func CheckChannelEgress(sessionID, channel, recipient string) (allow bool, reason string) {
	var label *permissions.SessionLabel
	if sessionID != "" {
		label = hakasesession.DefaultRegistry.Get(sessionID)
	}
	if label == nil && sessionID != "" {
		// Present-but-unregistered session: fail closed on taint only
		// when a policy is installed; without a policy keep v1.
		eff, _, _, ok := permissions.LookupWithLabel(nil, "send_message", recipient)
		if !ok {
			return true, ""
		}
		if eff == permissions.EffectDeny {
			return false, flowDenialText("send_message", nil, permissions.ShortHash(channel, recipient))
		}
		return true, ""
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
	return false, flowDenialText("send_message", label, permissions.ShortHash(channel, recipient))
}

// MergeDelegateLabel merges a finished child's terminal label into the
// parent session (H4): Meet-only (narrowing), hash for audit only.
func MergeDelegateLabel(parentSessionID string, child permissions.SessionLabel) {
	if parentSessionID == "" {
		return
	}
	reg := hakasesession.DefaultRegistry
	cur := reg.Get(parentSessionID)
	if cur == nil {
		l := permissions.StartLabel()
		cur = &l
	}
	merged := permissions.Meet(*cur, child)
	reg.Set(parentSessionID, merged)
	persistFlowLabel(parentSessionID)
}

// AuditFlowBlock records a pre-dispatch flow denial (decision
// "flow_blocked", metadata only: hashes + label snapshot, never raw args).
func AuditFlowBlock(toolName, action, argsHash, sessionID string, rule *permissions.Rule, label *permissions.SessionLabel) {
	var pr permissions.PolicyRule
	if rule != nil {
		pr = rule.Citation()
	}
	ls := "uninitialized"
	if label != nil {
		trust := "trusted"
		if label.Trust == permissions.TrustSuspicious {
			trust = "suspicious"
		}
		aud := "public"
		switch label.Audience {
		case permissions.AudienceSelf:
			aud = "self"
		case permissions.AudienceInternal:
			aud = "internal"
		}
		ls = fmt.Sprintf("%s/%s v%d", trust, aud, label.Version)
	}
	pr.Flow = fmt.Sprintf("label %s argsHash %s", ls, argsHash)
	AuditCommandExec(CommandAuditEntry{
		Tool: toolName, Command: "flow:" + action,
		SessionID: sessionID, Decision: "flow_blocked",
		Reason:     "flow guard blocked " + action,
		PolicyRule: pr,
	})
}

// AuditFlowSanitized records a post-admission redaction (decision
// "flow_sanitized", metadata only).
func AuditFlowSanitized(toolName, action, argsHash, sessionID string) {
	AuditCommandExec(CommandAuditEntry{
		Tool: toolName, Command: "flow:" + action,
		SessionID: sessionID, Decision: "flow_sanitized",
		Reason: "flow guard redacted secret-shaped content",
	})
}

// agentToolName safely reads a tool name (nil-safe for tests).
func agentToolName(t tool.Tool) string {
	if t == nil {
		return ""
	}
	defer func() { _ = recover() }()
	return t.Name()
}
