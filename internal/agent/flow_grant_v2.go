// flow_grant_v2.go - S25 joint-grant agent wiring (dual-accept).
//
// Every fresh flow approval mints two records for the exact call: the
// legacy v1 key (consumed by the sandbox downstream gates, which keep
// their v1 store) and the v2 joint-hash record (session, tool,
// argsHash, labelVersion bound in one hash). Consumption is dual:
//
//   - StartLabel (labelVersion 0): v1 OR v2 satisfies (dual-accept -
//     pre-upgrade grants keep working, v1 parity).
//   - narrowed labels: only the v2 joint record satisfies (fresh
//     approval - a v1 record minted before the upgrade never authorizes
//     a narrowed call).
//
// Hash-only throughout (argsHash, never raw args); one-shot (consume
// deletes); TTL 15m lazy expiry shared with the v1 discipline.
package agent

import "amurru/hakase/internal/permissions"

// grantFlowCallV2 mints the v2 joint-hash record for an approved flow
// ask. Called alongside the v1 mint on every fresh approval (dual
// records, one human decision).
func grantFlowCallV2(b flowAskBinding) {
	permissions.GrantFlowBindingV2(permissions.FlowGrantBinding{
		SessionID: b.sessionID, Tool: canonicalGrantTool(b.tool),
		ArgsHash: b.argsHash, LabelVersion: b.labelVersion,
	})
}

// hasFlowGrantDual is the dual-accept peek (no consume): StartLabel
// accepts v1 or v2, narrowed requires v2.
func hasFlowGrantDual(b flowAskBinding) bool {
	return permissions.HasFlowGrantDual(flowGrantBinding(b))
}

// consumeFlowGrantDual is the dual-accept consume (one-shot): StartLabel
// consumes v1 or v2, narrowed consumes v2 only (fresh approval).
func consumeFlowGrantDual(b flowAskBinding) bool {
	return permissions.ConsumeFlowGrantDual(flowGrantBinding(b))
}
