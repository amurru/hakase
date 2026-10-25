// init_flow_test.go - H3: the sandbox approval hook forwards the Flow
// Guard binding (FlowReason/FlowArgsHash/FlowLabelVersion) into the agent
// gate request, so a sandbox-raised flow ask keeps its resume scope.
package main

import (
	"testing"

	"amurru/hakase/internal/interfaces"
)

func TestSandboxApprovalRequestForwardsFlow(t *testing.T) {
	req := interfaces.ApprovalRequest{
		Tool:             "system_exec",
		Command:          "ls",
		Risk:             "high",
		Reason:           "test",
		FlowReason:       "flow: shell blocked",
		FlowArgsHash:     "abc123",
		FlowLabelVersion: 7,
	}
	got := sandboxApprovalRequest(req)
	if got.FlowReason != "flow: shell blocked" || got.FlowArgsHash != "abc123" || got.FlowLabelVersion != 7 {
		t.Fatalf("flow binding dropped: %+v", got)
	}
	if got.Tool != "system_exec" || got.Command != "ls" {
		t.Fatalf("base fields dropped: %+v", got)
	}

	// Non-flow requests map cleanly (no phantom binding).
	plain := sandboxApprovalRequest(interfaces.ApprovalRequest{Tool: "system_exec", Command: "ls"})
	if plain.FlowReason != "" || plain.FlowArgsHash != "" || plain.FlowLabelVersion != 0 {
		t.Fatalf("phantom flow binding: %+v", plain)
	}
}
