// flow_loop5_test.go - loop 5 M2: production-hook integration (cmd/hakase
// imports both sides). The production FlowLabelFunc (init.go wiring)
// resolves a narrowed delegate child label, a flow Ask is approved, and
// the sandbox gate admits the exact call once (one-shot).
package main

import (
	"context"
	"testing"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
)

// loop5Ctx carries an ADK-style task id for SessionIDFromCtx resolution.
type loop5Ctx struct {
	context.Context
	taskID string
}

func (c loop5Ctx) SessionID() string { return c.taskID }

func TestProductionDelegateApproveAdmitsOnce(t *testing.T) {
	// Install a v2 policy: shell requires the widest audience, so a
	// narrowed child asks.
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })

	sess := "sess-loop5-prod-e2e"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-loop5", "h")
	parentLbl := hakasesession.DefaultRegistry.Get(sess)
	if parentLbl == nil {
		t.Fatal("parent label missing")
	}

	// Delegate child ctx (narrowed), session-carrying, sandbox-pinned:
	// mirrors production where the ADK invocation ctx carries SessionID
	// and the delegate values ride the context chain.
	taskID := "task-loop5-prod-e2e"
	interfaces.RegisterTaskSession(taskID, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	base := hakaseagent.WithDelegateFlowState(context.Background(), parentLbl)
	sb := &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths}
	ctx := loop5Ctx{Context: sandbox.WithConfig(base, sb), taskID: taskID}

	// Production hook resolves the live narrowed child (not start).
	got := sandbox.FlowLabelFunc(ctx)
	if got == nil || got.Audience != permissions.AudienceInternal {
		t.Fatalf("production FlowLabelFunc = %+v, want narrowed internal", got)
	}

	// The pre-dispatch sees a flow Ask under the narrowed child.
	ok, blocked, _, eff, _ := hakaseagent.CheckPreFlow(ctx, "system_exec", map[string]any{"command": "echo hi"})
	if !ok || !blocked || eff != permissions.EffectAsk {
		t.Fatalf("pre-dispatch: ok=%v blocked=%v eff=%v, want true/true/ask", ok, blocked, eff)
	}

	// Approve: mint the exact grant the agent would leave (same triple
	// the pre-dispatch bound). This is what grantFlowCall installs on
	// human approval.
	resources := permissions.ShellResources("echo hi", nil)
	hash := permissions.FlowArgsHash("system_exec", "shell", resources)
	var version uint64
	if l := sandbox.FlowLabelFunc(ctx); l != nil {
		version = l.Version
	}
	permissions.GrantFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "system_exec", ArgsHash: hash, LabelVersion: version,
	})
	// S25: a narrowed dispatch needs the fresh v2 joint twin too (one
	// approval mints both); the gate drains both (one-shot).
	permissions.GrantFlowBindingV2(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "system_exec", ArgsHash: hash, LabelVersion: version,
	})
	t.Cleanup(func() {
		permissions.ConsumeFlowGrantDual(permissions.FlowGrantBinding{
			SessionID: sess, Tool: "system_exec", ArgsHash: hash, LabelVersion: version,
		})
	})

	// The sandbox gate admits the exact call once through the
	// production wiring, then denies again (one-shot: the grant is
	// consumed - the second call has no approval and fails).
	if _, err := sandbox.BuildExecCommandFor(ctx, "echo hi", nil, "", nil); err != nil {
		t.Fatalf("first sandbox admit denied: %v", err)
	}
	if _, err := sandbox.BuildExecCommandFor(ctx, "echo hi", nil, "", nil); err == nil {
		t.Fatal("second sandbox admit passed, want denial (one-shot grant consumed)")
	}
}
