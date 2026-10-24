// s25_test.go - S25 joint-grant agent wiring tests.
package agent

import (
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestS25AgentDualRecords pins dual mint on fresh approval: one human
// decision installs both the v1 record (sandbox downstream gates) and
// the v2 joint record (narrowed-label fresh rule), and a single
// dual-consume authorizes the exact call once.
func TestS25AgentDualRecords(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	b := flowAskBinding{sessionID: "sess-s25-agent", tool: "send_message", argsHash: "joint-h", labelVersion: 7}
	grantFlowCall(b)
	grantFlowCallV2(b)
	if !hasFlowGrantDual(b) {
		t.Fatal("fresh approval must dual-satisfy a narrowed call")
	}
	if !consumeFlowGrantDual(b) {
		t.Fatal("fresh approval must dual-consume a narrowed call")
	}
	if consumeFlowGrantDual(b) {
		t.Fatal("one-shot: second consume must fail")
	}
}
