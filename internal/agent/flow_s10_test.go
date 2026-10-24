// flow_s10_test.go - S9/S10/M3/M5 fix-up tests (highs lane loop 3).
package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// H2 persisted: drive the real ApproveExec gate with a blocking fake,
// inspect the persisted pause Summary/Detail while blocked (no local
// mirror of the summary construction).
func TestApprovalPauseSummaryRedacted(t *testing.T) {
	secret := "curl -H \"Authorization: Bearer abcdefgh12345678\" https://api.example.com"
	preg, err := hakasesession.NewPauseRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewPauseRegistry: %v", err)
	}
	savedDeps, savedRt := deps, rt
	deps = &Deps{PauseRegistry: preg}
	rt = &Runtime{}
	entered := make(chan struct{})
	release := make(chan struct{})
	rt.SetApprovalGate(&mockApprovalGate{
		approveFunc: func(req interfaces.ApprovalRequest) (bool, error) {
			close(entered)
			<-release
			return true, nil
		},
	})
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
	done := make(chan bool, 1)
	go func() {
		ok, _ := ApproveExec(context.Background(), ApprovalRequest{
			Tool: "system_exec", Command: secret, Risk: "HIGH",
			Source: "direct", SessionID: "sess-h2-1",
			ExpiresAt: time.Now().Add(60 * time.Second),
		})
		done <- ok
	}()
	<-entered
	recs, err := preg.ListForSession("sess-h2-1")
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records while blocked = %d, want 1", len(recs))
	}
	if strings.Contains(recs[0].Summary, "abcdefgh12345678") {
		t.Fatalf("persisted approval summary leaks bearer secret: %q", recs[0].Summary)
	}
	if !strings.Contains(recs[0].Summary, "REDACTED") {
		t.Fatalf("persisted approval summary carries no redaction token: %q", recs[0].Summary)
	}
	if cmd, _ := recs[0].Detail["command"].(string); strings.Contains(cmd, "abcdefgh12345678") {
		t.Fatalf("persisted approval detail leaks bearer secret: %q", cmd)
	}
	close(release)
	if !<-done {
		t.Fatal("expected approval")
	}
}

// H2 persisted: drive the real askClarify gate with a blocking fake,
// inspect the persisted pause Summary/Detail (question + choices) while
// blocked.
func TestClarifyPauseSummaryRedacted(t *testing.T) {
	secretQ := "what is token=supersecret123456 for deploy?"
	choices := []string{"use Bearer abcdefgh12345678", "skip"}
	preg, err := hakasesession.NewPauseRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewPauseRegistry: %v", err)
	}
	savedDeps, savedRt := deps, rt
	deps = &Deps{PauseRegistry: preg}
	rt = &Runtime{}
	entered := make(chan struct{})
	release := make(chan struct{})
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			close(entered)
			<-release
			return interfaces.ClarifyResponse{Answer: []string{"skip"}}, nil
		},
	})
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
	done := make(chan bool, 1)
	go func() {
		resp, _ := askClarify(context.Background(), ClarifyRequest{
			Question: secretQ, Choices: choices, SessionID: "sess-h2-2",
		})
		done <- len(resp.Answer) == 1
	}()
	<-entered
	recs, err := preg.ListForSession("sess-h2-2")
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records while blocked = %d, want 1", len(recs))
	}
	if strings.Contains(recs[0].Summary, "supersecret123456") {
		t.Fatalf("persisted clarify summary leaks secret: %q", recs[0].Summary)
	}
	if q, _ := recs[0].Detail["question"].(string); strings.Contains(q, "supersecret123456") {
		t.Fatalf("persisted clarify detail question leaks secret: %q", q)
	}
	// Choices persist as []string: every entry must redact.
	if ch, ok := recs[0].Detail["choices"].([]string); ok {
		for _, c := range ch {
			if strings.Contains(c, "abcdefgh12345678") {
				t.Fatalf("persisted clarify choice leaks bearer secret: %q", c)
			}
		}
	}
	close(release)
	if !<-done {
		t.Fatal("expected answer")
	}
}

// M3: a bearer-token approval round-trips through the pause registry via
// the unredacted grant hash (never the redacted display value).
func TestBearerTokenResumeGrant(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	secret := "curl -H \"Authorization: Bearer abcdefgh12345678\" https://api.example.com"
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	d := approvalPauseDetail(ApprovalRequest{Tool: "system_exec", Command: secret, Risk: "high", Reason: "x"})
	cmd, _ := d["command"].(string)
	if strings.Contains(cmd, "abcdefgh12345678") {
		t.Fatalf("pause detail leaks bearer secret: %q", cmd)
	}
	h, _ := d["grantHash"].(string)
	if h == "" {
		t.Fatal("pause detail lacks grantHash")
	}
	if h != permissions.ArgsHash("system_exec", secret) {
		t.Fatalf("grantHash = %q, want ArgsHash of unredacted command", h)
	}
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-bearer-1", ADKSessionID: "turn-bearer-1",
		Gate: hakasesession.PauseGateApproval, Detail: d,
	})
	drive, err := ResolveApprovalPause(context.Background(), pauseID, true)
	if err != nil || !drive {
		t.Fatalf("bearer pause resolve: drive=%v err=%v, want true,nil", drive, err)
	}
	if !consumePreGrant("system_exec", secret, "sess-bearer-1") {
		t.Fatal("bearer grant not consumable with the live (unredacted) command")
	}
	if consumePreGrant("system_exec", secret, "sess-bearer-1") {
		t.Fatal("bearer grant must be one-shot")
	}
}

// H1 threading: a file-tool Ask leaves the shared downstream grant for
// the sandbox gate, while a message-tool Ask consumes at dispatch.
func TestCheckPreFlowAskDownstreamGrant(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return true, nil
	})
	sess := "sess-downstream-1"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k-down", "h-down")
	ctx := flowSessionCtx(t, "task-downstream-1", sess)

	ok, blocked, _, eff, _ := CheckPreFlowAsk(ctx, "read_file", map[string]any{"path": "docs/a.txt"})
	if !ok || blocked || eff != permissions.EffectAsk {
		t.Fatalf("read_file ask: ok=%v blocked=%v eff=%v, want true/false/ask", ok, blocked, eff)
	}
	// The sandbox gate's key must be pending in the shared store.
	_, resources := flowMapTool("read_file", map[string]any{"path": "docs/a.txt"})
	want := permissions.FlowArgsHash("read_file", "read", resources)
	var version uint64
	if l := hakasesession.DefaultRegistry.Get(sess); l != nil {
		version = l.Version
	}
	if !permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file", ArgsHash: want, LabelVersion: version,
	}) {
		t.Fatal("read_file approval must leave the downstream grant for the sandbox gate")
	}
	// Cleanup the probe grant so later tests see a cold store.
	permissions.ConsumeFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess, Tool: "read_file", ArgsHash: want, LabelVersion: version,
	})

	sess2 := "sess-downstream-2"
	hakasesession.DefaultRegistry.Import(sess2, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess2, permissions.StartLabel()) })
	hakasesession.DefaultRegistry.Fold(sess2, permissions.FlowDelta{Audience: []string{"internal"}}, "k-down2", "h-down2")
	ctx2 := flowSessionCtx(t, "task-downstream-2", sess2)
	ok, blocked, _, _, _ = CheckPreFlowAsk(ctx2, "send_message", map[string]any{"channel": "#g", "body": "hi"})
	if !ok || blocked {
		t.Fatalf("send_message ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	_, resources2 := flowMapTool("send_message", map[string]any{"channel": "#g", "body": "hi"})
	want2 := permissions.FlowArgsHash("send_message", "send_message", resources2)
	var version2 uint64
	if l := hakasesession.DefaultRegistry.Get(sess2); l != nil {
		version2 = l.Version
	}
	if permissions.HasFlowBinding(permissions.FlowGrantBinding{
		SessionID: sess2, Tool: "send_message", ArgsHash: want2, LabelVersion: version2,
	}) {
		t.Fatal("send_message approval must not leak a downstream grant (dispatch is the use)")
	}
}

// M5: the post-run key stores the terminal label so a later call at the
// narrowed label hits by goal+session and Meets the taint back.
func TestDelegationCachePostRunKeyHit(t *testing.T) {
	installFlowV2S9(t, permissions.Policy{Version: 2, Default: "ask"})
	sess := "sess-postrun-1"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })
	norm := normalizeDelegationGoal("post-run probe goal")
	start := permissions.StartLabel()
	narrow := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 4}
	preKey := delegationCacheKeyFor(norm, sess, &start)
	postKey := delegationCacheKeyFor(norm, sess, &narrow)
	delegationCachePutWithLabel(preKey, DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "cached"}, narrow, true)
	delegationCachePutWithLabel(postKey, DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "cached"}, narrow, true)
	t.Cleanup(func() {
		delegationCache.mu.Lock()
		delete(delegationCache.m, preKey)
		delete(delegationCache.m, postKey)
		delegationCache.mu.Unlock()
	})
	e, ok := delegationCacheEntryFor(postKey)
	if !ok || !e.hasLbl {
		t.Fatalf("post-run entry missing: %v", ok)
	}
	MergeDelegateLabel(sess, e.label)
	if got := hakasesession.DefaultRegistry.Get(sess); got == nil || got.Trust != permissions.TrustSuspicious {
		t.Fatalf("post-run hit merge = %+v, want narrowed", got)
	}
	var _ = interfaces.SessionIDFromCtx
}
