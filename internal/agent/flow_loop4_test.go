// flow_loop4_test.go - S9-S15 impasse loop 4: mirror identity, live
// labels, delegation merge, Windows alias gate (epic #124).
package agent

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// H1: the agent grant uses a single canonical identity (loop 5 L2):
// system_exec_start shares BuildExecCommand with system_exec, so both
// spellings bind one key in the shared capped store.
func TestGrantFlowCallCanonicalTool(t *testing.T) {
	sess := "sess-loop4-grant-1"
	b := flowAskBinding{sessionID: sess, tool: "system_exec_start", argsHash: "h1", labelVersion: 7}
	grantFlowCall(b)
	t.Cleanup(func() {
		permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec", ArgsHash: "h1", LabelVersion: 7,
		})
		permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec_start", ArgsHash: "h1", LabelVersion: 7,
		})
	})
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "system_exec", ArgsHash: "h1", LabelVersion: 7,
	}) {
		t.Fatal("system_exec_start grant must mirror as canonical system_exec")
	}
	// Single identity: the start spelling resolves to the same canonical
	// key, so it hits too (no divergent second key).
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "system_exec_start", ArgsHash: "h1", LabelVersion: 7,
	}) {
		t.Fatal("start spelling must hit the canonical grant (single identity)")
	}
	if !hasFlowGrant(b) {
		t.Fatal("shared grant must be pending under the dispatched spelling")
	}
}

// H1: approve->sandbox e2e for both exec spellings with a real session:
// the pre-dispatch approval leaves the canonical grant the sandbox gate
// consumes (one-shot).
func TestApproveExecToSandboxBothExecTools(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return true, nil
	})
	for _, toolName := range []string{"system_exec", "system_exec_start"} {
		sess := "sess-loop4-e2e-" + toolName
		hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
		hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-"+toolName, "h")
		ctx := flowSessionCtx(t, "task-loop4-e2e-"+toolName, sess)
		ok, blocked, _, eff, _ := CheckPreFlowAsk(ctx, toolName, map[string]any{"command": "echo hi"})
		if !ok || blocked || eff != permissions.EffectAsk {
			t.Fatalf("%s ask: ok=%v blocked=%v eff=%v, want true/false/ask", toolName, ok, blocked, eff)
		}
		_, resources := flowMapTool(toolName, map[string]any{"command": "echo hi"})
		want := permissions.FlowArgsHash("system_exec", "shell", resources)
		var version uint64
		if l := hakasesession.DefaultRegistry.Get(sess); l != nil {
			version = l.Version
		}
		if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec", ArgsHash: want, LabelVersion: version,
		}) {
			t.Fatalf("%s approval must leave the canonical sandbox grant", toolName)
		}
		// Simulate the sandbox gate consuming the approved call.
		if !permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec", ArgsHash: want, LabelVersion: version,
		}) {
			t.Fatalf("%s sandbox consume must hit the canonical grant", toolName)
		}
	}
}

// High loop 5 single identity: an approved search_files Ask leaves the
// canonical grep grant the sandbox fileops gate consumes (one-shot).
// The pre-dispatch binds ("search_files","grep",PathResources) and the
// sandbox consumes the same triple - no split read/grep identity.
func TestApproveSearchFilesToSandbox(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "grep", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return true, nil
	})
	sess := "sess-loop5-search-e2e"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-search", "h")
	ctx := flowSessionCtx(t, "task-loop5-search-e2e", sess)
	ok, blocked, _, eff, _ := CheckPreFlowAsk(ctx, "search_files", map[string]any{"path": "docs", "pattern": "foo"})
	if !ok || blocked || eff != permissions.EffectAsk {
		t.Fatalf("search_files ask: ok=%v blocked=%v eff=%v, want true/false/ask", ok, blocked, eff)
	}
	_, resources := flowMapTool("search_files", map[string]any{"path": "docs", "pattern": "foo"})
	want := permissions.FlowArgsHash("search_files", "grep", resources)
	var version uint64
	if l := hakasesession.DefaultRegistry.Get(sess); l != nil {
		version = l.Version
	}
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "search_files", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("search_files approval must leave the canonical grep grant for the sandbox gate")
	}
	// The generic grep spelling hits the same canonical grant (single
	// identity): the sandbox tries both candidates through the canonical
	// tool.
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "grep", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("grep spelling must hit the same canonical grant (single identity)")
	}
	// Simulate the sandbox gate consuming the approved call (one-shot).
	if !permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "search_files", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("sandbox consume must hit the canonical grep grant")
	}
	if permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "search_files", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("grep grant must be one-shot")
	}
}

// H1: the live delegate child label resolves through the sandbox
// accessor (not a stale snapshot), including folds after the snapshot.
func TestFlowLabelForSandboxLiveChild(t *testing.T) {
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
	ctx := WithDelegateFlowState(context.Background(), &narrowed)
	if got := FlowLabelForSandbox(ctx); got == nil || got.Trust != permissions.TrustSuspicious {
		t.Fatalf("sandbox accessor missed live child label: %+v", got)
	}
	st := DelegateFlowStateFromCtx(ctx)
	st.mu.Lock()
	st.label.Audience = permissions.AudienceSelf
	st.mu.Unlock()
	if got := FlowLabelForSandbox(ctx); got == nil || got.Audience != permissions.AudienceSelf {
		t.Fatalf("sandbox accessor missed post-fold child label: %+v", got)
	}
	// Snapshot propagation for transports without shared state.
	snap := permissions.StartLabel()
	snapCtx := hakasesession.WithFlowLabel(context.Background(), &snap)
	if got := FlowLabelForSandbox(snapCtx); got == nil || !permissions.IsStart(*got) {
		t.Fatalf("sandbox accessor missed session snapshot: %+v", got)
	}
}

// H1 delegate->sandbox: a child-run approval threads the parent session
// grant the sandbox consumes.
func TestDelegateSandboxApproveThreading(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return true, nil
	})
	sess := "sess-loop4-deleg-1"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-deleg", "h")
	parentLbl := hakasesession.DefaultRegistry.Get(sess)
	// Wrap order: delegate state INSIDE the session-carrying ctx, mirroring
	// production where the ADK invocation ctx carries SessionID() and the
	// delegate values ride the context chain.
	base := WithDelegateFlowState(context.Background(), parentLbl)
	taskID := "task-loop4-deleg-1"
	interfaces.RegisterTaskSession(taskID, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	ctx := flowTestCtx{Context: base, taskID: taskID}
	ok, blocked, _, eff, _ := CheckPreFlowAsk(ctx, "read_file", map[string]any{"path": "docs/a.txt"})
	if !ok || blocked || eff != permissions.EffectAsk {
		t.Fatalf("delegate read ask: ok=%v blocked=%v eff=%v, want true/false/ask", ok, blocked, eff)
	}
	_, resources := flowMapTool("read_file", map[string]any{"path": "docs/a.txt"})
	want := permissions.FlowArgsHash("read_file", "read", resources)
	var version uint64
	if l := FlowLabelForSandbox(ctx); l != nil {
		version = l.Version
	}
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("delegate approval must leave the sandbox grant under the parent session")
	}
	permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file", ArgsHash: want, LabelVersion: version,
	})
}

// H4 gate-level: Win32 alias spellings hit the same canonical rule as
// the plain path (shared permissions.CanonicalPath identity).
func TestFlowCanonicalWindowsGateLevel(t *testing.T) {
	canon, _ := permissions.CanonicalPath(`C:\ws\flowtest.txt`)
	if strings.Contains(canon, ":ads") || strings.HasSuffix(canon, ".") {
		t.Fatalf("shared canonical keeps alias bytes: %q", canon)
	}
	g1, _ := permissions.CanonicalPath(`\\?\C:\ws\flowtest.txt`)
	g2, _ := permissions.CanonicalPath(`C:\ws\flowtest.txt`)
	if g1 != g2 {
		t.Fatalf("device-namespace fold diverges: %q vs %q", g1, g2)
	}
	g3, _ := permissions.CanonicalPath(`C:\ws\flowtest.txt:ads`)
	if g3 != g2 {
		t.Fatalf("ADS fold diverges: %q vs %q", g3, g2)
	}
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: canon, Effect: permissions.EffectDeny},
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow},
	}})
	ctx := flowSessionCtx(t, "task-loop4-win-1", "sess-loop4-win-1")
	for _, raw := range []string{`C:\ws\flowtest.txt`, `C:\ws\flowtest.txt:ads`} {
		ok, blocked, _, eff, _ := CheckPreFlow(ctx, "read_file", map[string]any{"path": raw})
		if !ok || !blocked || eff != permissions.EffectDeny {
			t.Fatalf("alias %q: ok=%v blocked=%v eff=%v, want deny via canonical leg", raw, ok, blocked, eff)
		}
	}
}

// M5: MergeDelegateLabel returns the merged label; the post-run cache
// key and entry bind that merged taint (handler-level hit).
func TestDelegationCacheMergedPostKeyHit(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask"})
	sess := "sess-loop4-postrun"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })
	norm := normalizeDelegationGoal("loop4 post-run merged probe")
	terminal := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 4}
	merged := MergeDelegateLabel(sess, terminal)
	if merged.Trust != permissions.TrustSuspicious || merged.Audience != permissions.AudienceInternal {
		t.Fatalf("MergeDelegateLabel returned %+v, want narrowed", merged)
	}
	postKey := delegationCacheKeyFor(norm, sess, &merged)
	delegationCachePutWithLabel(postKey, DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "cached"}, merged, true)
	t.Cleanup(func() {
		delegationCache.mu.Lock()
		delete(delegationCache.m, postKey)
		delegationCache.mu.Unlock()
	})
	e, ok := delegationCacheEntryFor(postKey)
	if !ok || !e.hasLbl {
		t.Fatalf("merged post-run entry missing: %v", ok)
	}
	before := hakasesession.DefaultRegistry.Get(sess)
	MergeDelegateLabel(sess, e.label)
	after := hakasesession.DefaultRegistry.Get(sess)
	if after.Trust != permissions.TrustSuspicious || after.Audience != permissions.AudienceInternal {
		t.Fatalf("post-run hit merge = %+v, want narrowed", after)
	}
	if before.Version != after.Version && before.Trust == after.Trust && before.Audience == after.Audience {
		t.Fatalf("idempotent re-merge bumped version: %+v -> %+v", before, after)
	}
	var _ = interfaces.SessionIDFromCtx
}
