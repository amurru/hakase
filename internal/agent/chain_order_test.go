// chain_order_test.go - T2.3 (AD-004): the security callback chain is pinned
// by observable behavior rather than assumed. ADK runs plugin callbacks
// before agent callbacks and short-circuits on a non-nil response, and
// runner.Run overwrites any inherited plugin manager (plugininternal
// ToContext), which is why these callbacks stay on the agents and are NOT
// migrated to runner.PluginConfig: agenttool builds its own runner, so a
// plugin-only chain would silently skip every delegated sub-agent. See
// docs/adk-adoption/spec.md AD-004.
package agent

import (
	"errors"
	"strings"
	"testing"

	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/permissions"

	"google.golang.org/adk/v2/agent"
)

// TestChainOrderHookDenyPrecedesFlowDeny pins the composed BeforeTool order:
// the hooks runner is consulted first, so a hook denial is what the model
// sees (and what the audit trail records) when a flow deny would also
// apply. Reordering the two changes the user-visible reason and the audit
// family, which is why this is asserted rather than implied.
func TestChainOrderHookDenyPrecedesFlowDeny(t *testing.T) {
	sh := hookTestShell(t)
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectDeny},
	}})
	ctx := flowSessionCtx(t, "task-chain-order", "sess-chain-order")

	r := hookTestRunner(t, hooks.Config{PreToolUse: []hooks.Group{{Matcher: "^send_message$", Hooks: []hooks.Handler{{
		Name:    "deny-first",
		Command: []string{sh, "-c", "echo hook says no >&2; exit 2"},
	}}}}})
	cb := makeHookBeforeToolCallback(r)

	res, err := cb(agent.NewContext(&agent.ContextMock{}), &fakeHookTool{name: "send_message"},
		map[string]any{"channel": "#general", "body": "hi"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil {
		t.Fatal("want a denial result")
	}
	if res["blocked_by"] != "hook" {
		t.Errorf("blocked_by = %v, want hook (hooks must short-circuit before the flow gate)", res["blocked_by"])
	}
	_ = ctx
}

// TestChainOrderFlowDenyFiresWhenHooksAllow pins the other half: with a
// permissive hook the flow gate still denies.
func TestChainOrderFlowDenyFiresWhenHooksAllow(t *testing.T) {
	sh := hookTestShell(t)
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectDeny},
	}})
	ctx := flowSessionCtx(t, "task-chain-flow", "sess-chain-flow")

	r := hookTestRunner(t, hooks.Config{PreToolUse: []hooks.Group{{Hooks: []hooks.Handler{{
		Command: []string{sh, "-c", "exit 0"},
	}}}}})
	cb := makeHookBeforeToolCallback(r)

	res, err := cb(agent.NewContext(&agent.ContextMock{}), &fakeHookTool{name: "send_message"},
		map[string]any{"channel": "#general", "body": "hi"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil {
		t.Fatal("want a flow denial result")
	}
	if res["blocked_by"] != "flow_guard" {
		t.Errorf("blocked_by = %v, want flow_guard", res["blocked_by"])
	}
	_ = ctx
}

// TestChainOrderAfterToolPreservesError (C1) pins flow-guard's hard rule:
// a post-admission callback must never turn a tool failure into a success.
// With no v2 policy the callback is inert (nil, nil): no override, so ADK
// keeps the original error.
func TestChainOrderAfterToolPreservesError(t *testing.T) {
	permissions.Install(nil)
	r := hookTestRunner(t, hooks.Config{})
	cb := makeHookAfterToolCallback(r)

	runErr := errors.New("tool exploded")
	res, err := cb(agent.NewContext(&agent.ContextMock{}), &fakeHookTool{name: "send_message"},
		nil, map[string]any{"partial": "x"}, runErr)
	if res != nil {
		t.Errorf("result = %v, want nil so ADK keeps the error", res)
	}
	if err != nil {
		t.Errorf("err = %v, want nil (no override; ADK keeps the original error)", err)
	}
}

// TestChainOrderAfterToolRedactsErrorUnderV2 is the C1 redaction half: under
// a v2 policy the callback must return a redacted error (never a result
// override), so the failure reaches the model without the secret.
func TestChainOrderAfterToolRedactsErrorUnderV2(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow"})
	ctx := flowSessionCtx(t, "task-chain-c1", "sess-chain-c1")
	r := hookTestRunner(t, hooks.Config{})
	cb := makeHookAfterToolCallback(r)

	runErr := errors.New("tool exploded with token sk-abcdefghijklmnopqrstuvwxyz0123456789")
	res, err := cb(agent.NewContext(&agent.ContextMock{}), &fakeHookTool{name: "send_message"},
		nil, map[string]any{"partial": "x"}, runErr)
	if res != nil {
		t.Errorf("result = %v, want nil (a non-nil result would make ADK drop the error)", res)
	}
	if err == nil {
		t.Fatal("want a redacted error carrier, got nil")
	}
	if strings.Contains(err.Error(), "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("error carried the secret through unredacted: %v", err)
	}
	_ = ctx
}
