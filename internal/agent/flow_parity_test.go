// flow_parity_test.go - Flow Guard fix-up loop 1/2 (epic #124).
//
// H1: the flow pre-dispatch path is inert on v1 policies (byte-identical
// behavior - a base ask never becomes a flow ask).
// H2: the post-admission path and channel egress are inert on v1 policies
// (no redaction, no withhold, no flow audit).
// H3: flowMapTool maps the real web tool names (web_fetch, web_search) to
// the webfetch policy action so v2 webfetch deltas actually taint.
// C1: foldFlowLabel through a real SessionService survives turn end + resume.
package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// installV1Policy compiles a v1 policy (no experimental gate needed) and
// installs it with the same audit/registry isolation as installFlowPolicy.
func installV1Policy(t *testing.T, p permissions.Policy) {
	t.Helper()
	if p.Version == 0 {
		p.Version = 1
	}
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile v1: %v", err)
	}
	if cp.FlowEnabled() {
		t.Fatalf("v1 policy unexpectedly flow-enabled")
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = origAuditDir })
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })
}

// TestCheckPreFlowV1Parity pins H1: with a v1 policy the flow pre-dispatch
// path is inert (ok=false) for every base verdict, so a base ask never
// becomes a flow ask and the existing per-tool gates decide byte-identical
// to a build without flow.
func TestCheckPreFlowV1Parity(t *testing.T) {
	installV1Policy(t, permissions.Policy{Version: 1, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAsk},
		{Action: "shell", Resource: "rm *", Effect: permissions.EffectDeny},
		{Action: "read", Resource: "docs/public/*", Effect: permissions.EffectAllow},
	}})
	ctx := flowSessionCtx(t, "task-v1parity-1", "v1parity-1")
	cases := []struct {
		tool string
		args map[string]any
	}{
		// Base ask (rule-backed): must not become a flow ask.
		{"send_message", map[string]any{"channel": "#general", "body": "hello"}},
		// Base default ask (no matching rule): must not become a flow ask.
		{"write_file", map[string]any{"path": "a/b.txt"}},
		// Base deny: the existing gates enforce it, not the flow path.
		{"system_exec", map[string]any{"command": "rm -rf /tmp/x"}},
		// Base allow: passes through.
		{"read_file", map[string]any{"path": "docs/public/x.md"}},
	}
	for _, c := range cases {
		ok, blocked, result, eff, reason := CheckPreFlow(ctx, c.tool, c.args)
		if ok || blocked || result != nil || eff != "" || reason != "" {
			t.Errorf("%s: = (%v,%v,%v,%q,%q), want (false,false,nil,\"\",\"\")",
				c.tool, ok, blocked, result, eff, reason)
		}
	}
}

// TestCheckPreFlowAskV1Headless pins the H1 end state: with a v1 ask rule
// and no approval mechanism wired (headless), the ask-human remedy is never
// reached - the call passes the flow layer untouched instead of failing
// closed as a flow ask.
func TestCheckPreFlowAskV1Headless(t *testing.T) {
	installV1Policy(t, permissions.Policy{Version: 1, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAsk},
	}})
	savedRt := rt
	rt = nil
	t.Cleanup(func() { rt = savedRt })
	ctx := flowSessionCtx(t, "task-v1parity-2", "v1parity-2")
	ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#g", "body": "hi"})
	if ok || blocked {
		t.Errorf("v1 headless ask: ok=%v blocked=%v, want false/false (flow layer untouched)", ok, blocked)
	}
}

// TestCheckPostFlowV1Parity pins H2 (after-tool): with a v1 policy the
// post-admission path changes nothing - success results and errors pass
// through unredacted and no flow decision is audited.
func TestCheckPostFlowV1Parity(t *testing.T) {
	installV1Policy(t, permissions.Policy{Version: 1, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAsk},
	}})
	ctx := flowSessionCtx(t, "task-v1parity-3", "v1parity-3")
	tl := stubTool(t, "send_message")
	secret := "token=supersecret123456"

	// Success path: no override, no error, result untouched.
	result := map[string]any{"status": "sent " + secret}
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "x"}, result, nil)
	if override != nil || ferr != nil {
		t.Fatalf("v1 success: override=%v err=%v, want nil/nil", override, ferr)
	}
	if got := result["status"].(string); !strings.Contains(got, secret) {
		t.Errorf("v1 success redacted the result: %q (want byte-identical passthrough)", got)
	}

	// Error path: nothing is redacted and no redacted carrier replaces the
	// failure - (nil, nil) falls through to the original err per the ADK
	// AfterTool contract (docs/hooks/spec.md), byte-identical to v1.
	result2 := map[string]any{"content": "config holds " + secret}
	runErr := errors.New("read failed: " + secret)
	override, ferr = CheckPostFlow(ctx, tl, map[string]any{"channel": "#g"}, result2, runErr)
	if override != nil || ferr != nil {
		t.Errorf("v1 error: override=%v err=%v, want nil/nil (original error surfaces via fall-through)", override, ferr)
	}
	if got := result2["content"].(string); !strings.Contains(got, secret) {
		t.Errorf("v1 error redacted the result: %q (want byte-identical passthrough)", got)
	}

	// No flow decision was audited on the v1 policy.
	entries, err := ReadAuditEntries(AuditDir(), time.Time{})
	if err != nil {
		t.Fatalf("ReadAuditEntries: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(string(e.Decision), "flow_") {
			t.Errorf("v1 policy audited flow decision %q, want none", e.Decision)
		}
	}
}

// TestCheckChannelEgressV1Parity pins H2 (channel): with a v1 policy every
// push is allowed - even under a narrowed label and even when the v1 policy
// carries a send_message ask rule.
func TestCheckChannelEgressV1Parity(t *testing.T) {
	installV1Policy(t, permissions.Policy{Version: 1, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAsk},
	}})
	sess := "v1chan-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	if allow, reason := CheckChannelEgress(sess, "telegram", "telegram:1"); !allow || reason != "" {
		t.Errorf("v1 start: allow=%v reason=%q, want true/\"\"", allow, reason)
	}
	// A narrowed label must not withhold on a v1 policy (fail-closed
	// withhold is v2-only).
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")
	if allow, reason := CheckChannelEgress(sess, "telegram", "telegram:1"); !allow || reason != "" {
		t.Errorf("v1 narrowed: allow=%v reason=%q, want true/\"\"", allow, reason)
	}
	// Unknown sessions also stay permissive on v1.
	if allow, _ := CheckChannelEgress("v1chan-missing", "discord", "discord:1"); !allow {
		t.Error("v1 unknown session must allow")
	}
}

// TestFlowMapToolWebRealNames pins H3: the real tool names web_fetch and
// web_search map to the webfetch policy action (the bare "webfetch" tool
// name maps too, for MCP-shaped callers), so v2 webfetch deltas taint real
// web calls instead of leaving the rule inert.
func TestFlowMapToolWebRealNames(t *testing.T) {
	action, res := flowMapTool("web_fetch", map[string]any{"url": "https://Example.com/article?a=1#frag"})
	if action != "webfetch" {
		t.Errorf("web_fetch action = %q, want webfetch", action)
	}
	if len(res) != 1 || !strings.Contains(res[0], "example.com") {
		t.Errorf("web_fetch resources = %v, want normalized URL", res)
	}
	action, res = flowMapTool("web_search", map[string]any{"query": "golang generics", "max_results": 3})
	if action != "webfetch" {
		t.Errorf("web_search action = %q, want webfetch", action)
	}
	if len(res) != 2 || !strings.Contains(strings.Join(res, " "), "golang") {
		t.Errorf("web_search resources = %v, want query text + hash", res)
	}
	action, _ = flowMapTool("webfetch", map[string]any{"url": "https://example.com/x"})
	if action != "webfetch" {
		t.Errorf("webfetch action = %q, want webfetch", action)
	}
}

// TestFlowWebfetchRuleHit pins the H3 end state: the mapped web_fetch action
// actually matches a v2 webfetch rule, so the source delta folds.
func TestFlowWebfetchRuleHit(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "webfetch", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	for _, tool := range []string{"web_fetch", "web_search"} {
		var args map[string]any
		if tool == "web_fetch" {
			args = map[string]any{"url": "https://example.com/article"}
		} else {
			args = map[string]any{"query": "latest release notes"}
		}
		action, resources := flowMapTool(tool, args)
		delta, hasDelta := permissions.DeltaForInstalled(action, resources...)
		if !hasDelta {
			t.Errorf("%s: no delta for (%q %v), want the webfetch taint", tool, action, resources)
			continue
		}
		if delta.Trust != "suspicious" {
			t.Errorf("%s: delta = %+v, want suspicious trust", tool, delta)
		}
		// The full pre-dispatch path sees an installed v2 rule (ok) and
		// lets the source through (a source rarely gates).
		ctx := flowSessionCtx(t, "task-webhit-"+tool, "webhit-"+tool)
		ok, blocked, _, _, _ := CheckPreFlow(ctx, tool, args)
		if !ok || blocked {
			t.Errorf("%s: ok=%v blocked=%v, want true/false", tool, ok, blocked)
		}
	}
}

// TestFlowMapToolWebSearchFallback pins L5/F3: the web_search_fallback
// toolset name maps to the webfetch policy action exactly like the
// web_search tool name (ADK surfaces the toolset name on some dispatch
// paths while the tools register as web_search), carrying the same
// query-shaped resources. The bare "webfetch" spelling still maps too,
// for MCP-shaped callers.
func TestFlowMapToolWebSearchFallback(t *testing.T) {
	action, res := flowMapTool("web_search_fallback", map[string]any{"query": "golang generics"})
	if action != "webfetch" {
		t.Errorf("web_search_fallback action = %q, want webfetch", action)
	}
	if len(res) != 2 || !strings.Contains(res[0], "golang") || !strings.HasPrefix(res[1], "query_hash:") {
		t.Errorf("web_search_fallback resources = %v, want [query text, query_hash:...]", res)
	}
}

// TestFlowMapToolUnknownWebStaysUnknown pins the unknown-sink guard:
// only the exact "web_search" and "web_search_fallback" spellings map
// to webfetch. Any other web_* spelling (e.g. web_search_extra) keeps
// its tool name as the action so a narrowed label fails closed instead
// of inheriting the webfetch taint rule.
func TestFlowMapToolUnknownWebStaysUnknown(t *testing.T) {
	for _, tool := range []string{"web_search_extra", "web_searchx", "web_fetch_extra"} {
		action, _ := flowMapTool(tool, map[string]any{"query": "hi"})
		if action == "webfetch" {
			t.Errorf("%s must not map to webfetch, got %q", tool, action)
		}
		if action != tool {
			t.Errorf("%s action = %q, want tool name passthrough %q", tool, action, tool)
		}
	}
}

// TestCheckPostFlowWebSearchFoldsSuspicious pins F3 end to end: a web_search
// (or web_search_fallback) result folds the session label to suspicious
// under a v2 webfetch taint rule, so a later send is gated by the narrowed
// label.
func TestCheckPostFlowWebSearchFoldsSuspicious(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "webfetch", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	for _, toolName := range []string{"web_search", "web_search_fallback"} {
		sessID := "webfold-" + toolName
		ctx := flowSessionCtx(t, "task-"+sessID, sessID)
		tl := stubTool(t, toolName)
		args := map[string]any{"query": "latest release notes"}
		result := map[string]any{"results": "snippet text without secrets"}
		override, ferr := CheckPostFlow(ctx, tl, args, result, nil)
		if override != nil || ferr != nil {
			t.Fatalf("%s: override=%v err=%v, want nil/nil (clean source admits)", toolName, override, ferr)
		}
		got := hakasesession.DefaultRegistry.Get(sessID)
		if got == nil || got.Trust != permissions.TrustSuspicious {
			t.Fatalf("%s: label after post-flow = %+v, want suspicious trust", toolName, got)
		}
		if got.Version != 1 {
			t.Errorf("%s: label version = %d, want 1 (single fold)", toolName, got.Version)
		}
	}
}

// TestFoldFlowLabelSurvivesTurnEndAndResume pins C1 through the production
// fold path: foldFlowLabel with a real SessionService persists the narrowing
// so it survives the turn-end Load/Save cycle and a later resume.
func TestFoldFlowLabelSurvivesTurnEndAndResume(t *testing.T) {
	dir := t.TempDir()
	store, err := hakasesession.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	savedDeps, savedRt := deps, rt
	deps = &Deps{SessionService: svc}
	rt = &Runtime{}
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })

	sess, err := svc.CreateSession("turn")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ctx := flowSessionCtx(t, "task-turn-1", sess.ID)

	// Turn start: resume imports the persisted (start) label.
	if _, err := store.Load(sess.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A source tool folds mid-turn; foldFlowLabel persists best-effort via
	// the real SessionService.
	foldFlowLabel(ctx, permissions.FlowDelta{Audience: []string{"internal"}}, "fold:k1", "h1")
	if got := hakasesession.DefaultRegistry.Get(sess.ID); got == nil ||
		got.Audience != permissions.AudienceInternal || got.Version != 1 {
		t.Fatalf("registry after fold = %+v, want internal v1", got)
	}

	// Turn end (the persistAgentResponse shape: Load, append, Save).
	turnEnd, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("turn-end Load: %v", err)
	}
	turnEnd.AddMessageWithMetaAndAttachments("agent", "done", "", 0, hakasesession.MessageKindText, nil)
	hakasesession.DefaultRegistry.Sync(turnEnd)
	if err := store.Save(turnEnd); err != nil {
		t.Fatalf("turn-end Save: %v", err)
	}

	// The live registry still holds the narrowing (the Load did not widen
	// it back to the stale on-disk label)...
	if got := hakasesession.DefaultRegistry.Get(sess.ID); got == nil ||
		got.Audience != permissions.AudienceInternal || got.Version != 1 {
		t.Fatalf("registry after turn end = %+v, want live internal v1", got)
	}
	// ...and a later resume keeps the restriction with no double-fold.
	raw, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("resume Load: %v", err)
	}
	if raw.Flow == nil || raw.Flow.Audience != permissions.AudienceInternal || raw.Flow.Version != 1 {
		t.Fatalf("resumed Flow = %+v, want internal v1", raw.Flow)
	}
	r2 := hakasesession.NewFlowRegistry()
	if recomputed := hakasesession.RegisterLoadedWith(r2, raw); recomputed {
		t.Fatal("resumed v2 session recomputed, want imported")
	}
	if after, narrowed := r2.Fold(sess.ID, permissions.FlowDelta{Audience: []string{"internal"}}, "fold:k1", "h1"); narrowed || after.Version != 1 {
		t.Errorf("re-admit after resume = %+v/%v, want no-op v1", after, narrowed)
	}
}
