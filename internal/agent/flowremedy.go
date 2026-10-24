// flowremedy.go - Flow Guard Phase 3 remedies (FG-005).
//
// A flow denial offers three remedies to the model:
//
//   - ask-human routes the flow ask through the existing approval gate
//     (ApproveExec), so the web queue, the TUI and the Telegram/Discord
//     transports answer it with the same first-responder-wins flow. A
//     flow ask is never auto-approved by approval.mode=allow (H5), and
//     the answer authorizes exactly one call: (tool, argsHash,
//     labelVersion). The approval never resets the session label.
//   - sanitize redacts the payload with util.RedactSecrets and
//     re-checks the policy against the redacted payload. It
//     auto-proceeds only for a rule with effect:allow and
//     sanitizable:true on a narrow resource (H1) - never on a wildcard,
//     never on a requires failure.
//   - drop-result is the model simply not retrying.
//
// The denial text carries redacted metadata only (action, label
// snapshot, remedy): never raw resource paths or arg preimages, which
// can carry secrets.
package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// flowOffers is the remedy list a flow denial shows the model.
const flowOffers = "ask-human | sanitize | drop-result"

// flowTaintReason names the payload risk in a flow denial (a
// secret-shaped payload that no rule marked sanitizable).
const flowTaintReason = "payload carries secret-shaped content"

// flowAllowedAlternative names what the model may do next: a flow ask
// can still be approved by a human, a flow deny only by dropping the
// result.
func flowAllowedAlternative(effect permissions.Effect) string {
	if effect == permissions.EffectAsk {
		return "ask-human (approve this exact call)"
	}
	return "drop-result (skip this call)"
}

// flowDenialText renders the FG-005 denial shape: redacted metadata
// only (action, label snapshot, remedy) - never raw resource paths or
// arg preimages. S26: the rendered text is tag-neutralized so rule or
// tool text cannot smuggle live markup into the model context.
func flowDenialText(action string, l *permissions.SessionLabel, effect permissions.Effect) string {
	return neutralizeFlowText(fmt.Sprintf("flow: %s blocked (%s). Allowed: %s. Offer: %s",
		action, flowLabelSnapshot(l), flowAllowedAlternative(effect), flowOffers))
}

// flowDenialResult builds the model-legible denial result: the denial
// text plus the one-time resume notice when a pre-v2 session was
// recomputed conservative (FG-005/M1).
func flowDenialResult(ctx context.Context, toolName, action string, l *permissions.SessionLabel, effect permissions.Effect) map[string]any {
	text := flowDenialText(action, l, effect)
	if notice := hakasesession.DefaultRegistry.TakeFlowNotice(flowSessionID(ctx)); notice != "" {
		text += " " + notice
	}
	return flowBlockResult(toolName, text)
}

// flowAskBinding binds one flow ask to the exact call it covers.
type flowAskBinding struct {
	sessionID    string
	tool         string
	argsHash     string
	labelVersion uint64
}

// canonicalGrantTool normalizes the grant tool the same way flowArgsHash
// does (S9/H1 single identity, loop 5): system_exec_start shares
// BuildExecCommand with system_exec (Tool system_exec), and the generic
// grep tool shares the search_files implementation, so all spellings bind
// one key. The shared permissions store canonicalizes again, so this is
// idempotent.
func canonicalGrantTool(tool string) string {
	return canonicalFlowTool(tool)
}

// grantFlowCall installs the one-shot grant for an approved flow ask in
// the shared permissions store (single store, capped, loop 5 L2): the
// sandbox path gates (fileops, system_exec) consume the same binding so
// an approved Ask admits there instead of hard-denying. The store
// canonicalizes the tool, so every spelling binds one key.
func grantFlowCall(b flowAskBinding) {
	permissions.GrantFlowBinding(permissions.FlowGrantBinding{
		SessionID: b.sessionID, Tool: canonicalGrantTool(b.tool),
		ArgsHash: b.argsHash, LabelVersion: b.labelVersion,
	})
}

// flowGrantBinding converts a local ask binding to the shared store key.
func flowGrantBinding(b flowAskBinding) permissions.FlowGrantBinding {
	return permissions.FlowGrantBinding{
		SessionID: b.sessionID, Tool: canonicalGrantTool(b.tool),
		ArgsHash: b.argsHash, LabelVersion: b.labelVersion,
	}
}

// hasFlowGrant reports whether a grant for this exact call is pending
// (an approval that has not been dispatched yet).
func hasFlowGrant(b flowAskBinding) bool {
	return permissions.HasFlowBinding(flowGrantBinding(b))
}

// consumeFlowGrant reports and consumes a matching grant (one-shot).
func consumeFlowGrant(b flowAskBinding) bool {
	return permissions.ConsumeFlowBinding(flowGrantBinding(b))
}

// flowApprovalPreview renders the human-facing approval prompt line:
// the canonical resources (never the raw args) plus the action, so the
// approver sees what is about to run without the payload. Resources can
// carry secrets (a path or a URL with a token), so the preview is
// redacted before it is stored in the pause registry.
func flowApprovalPreview(action string, resources []string) string {
	parts := make([]string, 0, len(resources))
	for _, r := range resources {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		parts = append(parts, truncateRes(r, 120))
	}
	preview := "flow:" + action
	if len(parts) > 0 {
		preview += " " + strings.Join(parts, " ")
	}
	preview, _ = util.RedactSecrets(preview)
	// S26: neutralize live markup in the human-facing preview (a query
	// resource can carry model-controlled <tags>); truncate last.
	if n, changed := neutralizeFlowTags(preview); changed {
		preview = n
	}
	return truncateRes(preview, 400)
}

// ApproveFlowAsk runs the ask-human remedy for a flow ask (FG-005):
// the request goes to the existing approval gate (web queue, TUI,
// Telegram/Discord), never auto-approved by approval.mode=allow, and
// the answer authorizes exactly one dispatch of this call. The returned
// binding must be consumed by the dispatcher; a grant that does not
// match means the approval did not cover this call (fail closed).
func ApproveFlowAsk(ctx context.Context, toolName string, args map[string]any, reason string) (flowAskBinding, bool) {
	action, resources := flowMapTool(toolName, args)
	b := flowAskBinding{
		sessionID: flowSessionID(ctx),
		tool:      toolName,
		argsHash:  flowArgsHash(toolName, action, resources),
	}
	if label := flowLabelForCtx(ctx); label != nil {
		b.labelVersion = label.Version
	}
	// A pending approval for this exact call covers it: no second
	// prompt, and the dispatcher consumes the grant. S25 dual-accept
	// peek (narrowed labels require the v2 joint record).
	if hasFlowGrantDual(b) {
		return b, true
	}
	approved, err := ApproveExec(ctx, ApprovalRequest{
		Tool:             toolName,
		Command:          flowApprovalPreview(action, resources),
		Risk:             "flow",
		Reason:           reason,
		Source:           "direct",
		ExpiresAt:        time.Now().Add(ApprovalExpiry()),
		SessionID:        b.sessionID,
		FlowReason:       reason,
		FlowArgsHash:     b.argsHash,
		FlowLabelVersion: b.labelVersion,
	})
	if err != nil || !approved {
		flowDebugf("ask denied tool=%s action=%s argsHash=%s", toolName, action, b.argsHash)
		return b, false
	}
	// Bind the approval to this exact call: the dispatch consumes it
	// once, so a repeat of the same call asks again. S25: mint dual
	// records (v1 for the sandbox downstream gates, v2 joint-hash for
	// the narrowed-label fresh-approval rule) on one human decision.
	grantFlowCall(b)
	grantFlowCallV2(b)
	flowDebugf("ask approved tool=%s action=%s argsHash=%s labelVersion=%d",
		toolName, action, b.argsHash, b.labelVersion)
	return b, true
}

// CheckPreFlowAsk is the pre-dispatch entry point with the ask-human
// remedy (FG-005). It layers the remedy on top of CheckPreFlow:
//
//   - flow Deny: blocked with the denial text (a deny stays a deny).
//   - flow Ask: routed to the human through ApproveExec. An approved
//     call proceeds (the approval covers exactly this call); a denied
//     one gets the denial text.
//   - no flow verdict: unchanged (v1 parity).
func CheckPreFlowAsk(ctx context.Context, toolName string, args map[string]any) (ok bool, blocked bool, result map[string]any, effect permissions.Effect, reason string) {
	ok, blocked, result, effect, reason = CheckPreFlow(ctx, toolName, args)
	if !ok || !blocked || effect != permissions.EffectAsk {
		return ok, blocked, result, effect, reason
	}
	action, _ := flowMapTool(toolName, args)
	label := flowLabelForCtx(ctx)
	binding, approved := ApproveFlowAsk(ctx, toolName, args, reason)
	// Single shared store (loop 5 L2): a pending grant covers this exact
	// call. Tools with a sandbox downstream gate leave it for that gate
	// to consume (the dispatch is the sandbox exec); all others consume
	// here so the grant stays one-shot (the dispatch is the use). S25:
	// dual-accept consume (narrowed labels require the fresh v2 joint
	// record; StartLabel accepts either).
	if approved && hasFlowGrantDual(binding) {
		// The human approved this exact call: it may run.
		flowDebugf("dispatch allowed tool=%s action=%s argsHash=%s labelVersion=%d",
			binding.tool, action, binding.argsHash, binding.labelVersion)
		// S9/H1: tools with a sandbox downstream gate (file/shell)
		// leave the shared grant for that gate to consume; tools
		// without one consume it here so the grant stays one-shot
		// with no leak (the dispatch is the use).
		if !sandboxDownstreamTool(toolName) {
			consumeFlowGrantDual(binding)
		}
		return true, false, nil, effect, reason
	}
	// Denied, or the approval did not cover this call: the denial text
	// is the tool result.
	return true, true, flowDenialResult(ctx, toolName, action, label, effect), effect, reason
}

// sandboxDownstreamTool reports whether the tool's execution passes
// through a sandbox path gate (S9/H1) that consumes the shared grant.
// Those tools leave the permissions mirror for the gate; all others
// consume at pre-dispatch (the dispatch is the use).
func sandboxDownstreamTool(toolName string) bool {
	switch toolName {
	case "read_file", "write_file", "patch", "search_files",
		"read", "edit", "grep", "glob",
		"system_exec", "system_exec_start", "shell":
		return true
	default:
		return false
	}
}

// CheckPreFlowDeny evaluates the flow verdict for a call whose approval
// is owned by the tool itself. The python gate asks once, with the flow
// reason, on the same prompt as the python risk - so a flow Ask there
// must not also prompt from the pre-dispatch callback (the human would
// be asked twice for one call). Only a hard flow deny stops the call
// here; the Ask is left to the tool's own gate.
func CheckPreFlowDeny(ctx context.Context, toolName string, args map[string]any) (ok bool, denied bool, result map[string]any) {
	ok, blocked, result, eff, _ := CheckPreFlow(ctx, toolName, args)
	if !ok || !blocked || eff != permissions.EffectDeny {
		return ok, false, nil
	}
	return ok, true, result
}

// flowSanitizableRule reports whether a redacted payload may
// auto-proceed: the deciding rule must allow it and mark it
// sanitizable, and the resource must be narrow - a wildcard resource
// never qualifies (H1).
func flowSanitizableRule(rule *permissions.Rule) bool {
	if rule == nil || !rule.Sanitizable || rule.Effect != permissions.EffectAllow {
		return false
	}
	res := strings.TrimSpace(rule.Resource)
	if res == "" || res == "*" || res == "**" {
		return false
	}
	return true
}

// flowTaintCheckedAction reports whether the pre-dispatch payload taint
// check applies to the action. Only outward-facing sinks are checked:
// a local file write is not egress, and its content is judged by the
// post-admission path.
func flowTaintCheckedAction(action string) bool {
	switch action {
	case "send_message", "ask_sidekick", "save_skill", "vision", "python_interpreter":
		return true
	default:
		return false
	}
}

// flowRedactableAction reports whether the payload may be redacted in
// place and proceed. Only message-like payloads qualify: redacting the
// body of a message is the point of the remedy, while redacting code
// (python, a saved skill) would silently corrupt the execution, so
// those payloads only ever reach the human.
func flowRedactableAction(action string) bool {
	switch action {
	case "send_message", "ask_sidekick":
		return true
	default:
		return false
	}
}

// flowPayloadTainted reports whether the call's payload carries
// secret-shaped content on a narrowed label (the pre-dispatch mirror of
// the post-admission taint check).
func flowPayloadTainted(label *permissions.SessionLabel, action string, args map[string]any) bool {
	if label == nil || permissions.IsStart(*label) {
		return false
	}
	if !flowTaintCheckedAction(action) {
		return false
	}
	for _, s := range marshalResultStrings(args) {
		if _, ok := util.RedactSecrets(s); ok {
			return true
		}
	}
	return false
}

// flowSanitizePass runs the sanitize remedy on one pre-dispatch call:
// it redacts the payload, re-evaluates the policy against the redacted
// resources and reports the new verdict and the rule that decided it.
//
// Redacted is false when nothing was redacted (the caller keeps its
// verdict). When the payload was redacted, the returned effect, detail
// and rule are the re-checked ones: a rule that allowed the sanitized
// payload keeps the call admissible, anything else escalates. The rule
// matters as much as the effect - the redaction changes the resources
// (the body hash moves), so the deciding rule can change with them.
func flowSanitizePass(label *permissions.SessionLabel, toolName, action string, args map[string]any, rule *permissions.Rule) (redacted bool, eff permissions.Effect, detail string, reRule *permissions.Rule) {
	if !flowRedactableAction(action) {
		// Code payloads are never rewritten behind the operator's back.
		return false, "", "", nil
	}
	if !flowSanitizableRule(rule) {
		return false, "", "", nil
	}
	// Redact in place: the ADK hands the same args map to the tool
	// after the pre-dispatch callback, so the tool receives the
	// sanitized payload.
	if !redactArgsInPlace(args) {
		return false, "", "", nil
	}
	resources := flowResources(toolName, args)
	eff, reRule, detail, _ = permissions.LookupWithLabel(label, action, resources...)
	return true, eff, detail, reRule
}

// redactArgsInPlace replaces secret-shaped strings in the tool args with
// typed redaction tokens (util.RedactSecrets). Reports whether any rule
// fired.
func redactArgsInPlace(args map[string]any) bool {
	if args == nil {
		return false
	}
	return redactResultInPlace(args)
}

// flowResources maps (tool, args) onto the canonical policy resources.
func flowResources(toolName string, args map[string]any) []string {
	_, resources := flowMapTool(toolName, args)
	return resources
}
