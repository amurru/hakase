// flow_loop4_test.go - loop 4: both exec spellings consume one canonical
// grant; nil/start/narrowed matrix holds for the new candidates.
package sandbox

import (
	"context"
	"testing"

	"amurru/hakase/internal/permissions"
)

// H1: both exec tool spellings share one canonical shell grant.
func TestShellCandidatesBothSpellings(t *testing.T) {
	got := candidateToolsForAction("shell")
	has := map[string]bool{}
	for _, c := range got {
		has[c] = true
	}
	if !has["system_exec"] || !has["system_exec_start"] {
		t.Fatalf("shell candidates = %v, want both exec spellings", got)
	}
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 5}
	resources := permissions.ShellResources("echo hi", nil)
	hash := permissions.FlowArgsHash("system_exec", "shell", resources)
	// The agent mirror always mints the canonical spelling (H1 loop 4):
	// one canonical grant admits the shell gate whichever exec tool
	// dispatched (both share BuildExecCommand), then it is gone.
	for _, dispatch := range []string{"system_exec", "system_exec_start"} {
		sess := "sess-loop4-shell-" + dispatch
		permissions.GrantFlowBinding(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec",
			ArgsHash: hash, LabelVersion: narrowed.Version,
		})
		// S25: a narrowed dispatch needs the fresh v2 joint record too
		// (one approval mints both twins); the gate drains both.
		permissions.GrantFlowBindingV2(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec",
			ArgsHash: hash, LabelVersion: narrowed.Version,
		})
		if !tryConsumeShellApproval(sess, "echo hi", nil, narrowed.Version) {
			t.Fatalf("dispatch %s: canonical shell grant not consumable", dispatch)
		}
		if tryConsumeShellApproval(sess, "echo hi", nil, narrowed.Version) {
			t.Fatalf("dispatch %s: shell grant must be one-shot", dispatch)
		}
	}
	_ = context.Background
}
