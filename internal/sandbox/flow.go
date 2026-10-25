// flow.go - Flow Guard pre-dispatch label resolution for sandbox gates.
//
// FlowLabelFunc resolves the flow label for a call context (nil = feature
// off: callers keep exact v1 behavior). Main wires it to the session
// registry via interfaces.SessionIDFromCtx; production gates fall back to
// the start label when no session or no label is registered.
package sandbox

import (
	"context"
	"strings"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/session"
)

// flowShellAskBinding computes the Flow Guard ask binding for a shell
// Ask decision (S14): empty unless the ask is flow-caused. Pure helper -
// no API change to BuildExecCommand. The shell identity comes from
// permissions.ShellLine (the same string the gate matched), and the hash
// covers the canonical (action, resource) pair the same way the agent
// pre-dispatch binds it. The binding derives from the SAME lookup
// decision the gate made (decision.Reason carries the flow detail),
// never a second divergent lookup: a re-lookup between gate and bind
// could see a moved label and disagree. Callers attach the triple to the approval
// request so approval.mode=allow never auto-approves a flow ask
// (fail-closed, same discipline as the python gate); base asks keep
// empty Flow fields so v1 behavior is byte-identical without v2.
func flowShellAskBinding(label *permissions.SessionLabel, command string, args []string, decision GateDecision) (reason, argsHash string, labelVersion uint64) {
	if decision.Action != ActionAsk || !permissions.FlowEnabled() {
		return "", "", 0
	}
	// Flow-caused asks carry the flow detail in the reason (gate 4b
	// prefixes "flow: ... by permissions policy"); base asks ("requires
	// approval...", "unknown command...", risk asks) carry none.
	if !strings.Contains(decision.Reason, "flow:") {
		return "", "", 0
	}
	if label != nil {
		labelVersion = label.Version
	}
	// Canonical shell binding (H2/S14/M4): the actual invoking tool is
	// system_exec and the policy action is shell, mirroring
	// flowMapTool("system_exec") -> ("shell", ShellResources).
	return decision.Reason, permissions.FlowArgsHash("system_exec", "shell", permissions.ShellResources(command, args)), labelVersion
}

// FlowLabelFunc is set by main to resolve the Flow Guard label for a call
// context. Nil = flow gates off (callers keep v1 behavior exactly).
var FlowLabelFunc func(ctx context.Context) *permissions.SessionLabel

// candidateToolsForAction lists the agent tools that map onto one policy
// action (S9 exact-call threading): the sandbox gate knows the action
// and raw operand but not which tool the pre-dispatch bound, so it
// tries each candidate's canonical hash and consumes the one the human
// approved.
func candidateToolsForAction(action string) []string {
	switch action {
	case "read":
		return []string{"read_file", "read"}
	case "edit":
		return []string{"write_file", "patch", "edit"}
	case "grep":
		return []string{"search_files", "grep"}
	case "glob":
		return []string{"glob"}
	case "shell":
		// H1: both exec spellings share BuildExecCommand (Tool
		// system_exec); the pre-dispatch may bind either spelling,
		// so both candidates are tried.
		return []string{"system_exec", "system_exec_start"}
	default:
		return nil
	}
}

// canonicalFileTool normalizes the file-tool spelling the same way the
// agent pre-dispatch does (loop 5 single identity): the generic grep tool
// shares the search_files implementation, so both spellings bind one
// canonical key.
func canonicalFileTool(tool string) string {
	if tool == "grep" {
		return "search_files"
	}
	return tool
}

// tryConsumeFileApproval consumes the downstream grant for a path operand
// (S9): resources mirror the agent mapper (dual canonical+raw legs), so
// the hash matches the pre-dispatch binding exactly. Every candidate
// spelling hashes through the canonical tool so a grep-spelled dispatch
// consumes the canonical search_files grant (High loop 5 single identity).
func tryConsumeFileApproval(sessionID, action, raw string, labelVersion uint64) bool {
	// L5 test hygiene: an empty action can never match (no candidates);
	// an empty sessionID is valid (session-less callers bind grants
	// under "") so it must not fail closed here.
	if action == "" {
		return false
	}
	resources := permissions.PathResources(raw)
	for _, tool := range candidateToolsForAction(action) {
		canon := canonicalFileTool(tool)
		// S25: dual consume drains the twin v1+v2 records one
		// approval minted, so the grant stays one-shot.
		if permissions.ConsumeFlowGrantDual(permissions.FlowGrantBinding{
			SessionID: sessionID, Tool: canon,
			ArgsHash:     permissions.FlowArgsHash(canon, action, resources),
			LabelVersion: labelVersion,
		}) {
			return true
		}
	}
	return false
}

// canonicalShellTool normalizes the exec tool spelling the same way the
// agent pre-dispatch does (S9/H1 single identity): system_exec_start
// shares BuildExecCommand with system_exec, so both spellings bind one
// canonical key.
func canonicalShellTool(tool string) string {
	if tool == "system_exec_start" {
		return "system_exec"
	}
	return tool
}

// tryConsumeShellApproval consumes the downstream grant for a shell line
// (S9/S14 single identity): the hash covers the canonical ShellLine
// triple the agent pre-dispatch bound. Every candidate spelling hashes
// through the canonical tool so a start-spelled dispatch consumes the
// canonical grant.
func tryConsumeShellApproval(sessionID, command string, args []string, labelVersion uint64) bool {
	resources := permissions.ShellResources(command, args)
	for _, tool := range candidateToolsForAction("shell") {
		// S25: dual consume drains the twin v1+v2 records one
		// approval minted, so the grant stays one-shot.
		if permissions.ConsumeFlowGrantDual(permissions.FlowGrantBinding{
			SessionID: sessionID, Tool: canonicalShellTool(tool),
			ArgsHash:     permissions.FlowArgsHash(canonicalShellTool(tool), "shell", resources),
			LabelVersion: labelVersion,
		}) {
			return true
		}
	}
	return false
}

// flowLabelFromCtx resolves the label for ctx: the wired func when set,
// else nil (caller keeps v1 behavior). An empty session ID or v1 (flow
// off) yields the start label; a present-but-unregistered session returns
// nil when flow is enabled (fail-closed, mirroring flowLabelForCtx), so an
// unknown label can never pass as widest.
func flowLabelFromCtx(ctx context.Context) *permissions.SessionLabel {
	if FlowLabelFunc != nil {
		if l := FlowLabelFunc(ctx); l != nil {
			cp := *l
			return &cp
		}
		return nil
	}
	// Snapshot copy for transports that do not share the delegate
	// mutable state (H1): the parent seeds it at delegation start.
	if l := session.FlowLabelFromCtx(ctx); l != nil {
		return l
	}
	// Default wiring (no func set): resolve via the session registry so
	// out-of-the-box runs still enforce narrowed labels.
	if ctx != nil {
		if id := interfaces.SessionIDFromCtx(ctx); id != "" {
			if l := session.DefaultRegistry.Get(id); l != nil {
				return l
			}
			if permissions.FlowEnabled() {
				return nil
			}
		}
	}
	l := permissions.StartLabel()
	return &l
}
