// flowremedy_test.go - Flow Guard Phase 3 remedy, audit and replay tests
// (T3.1-T3.3: ask-human reuse, single-use binding, sanitize re-check,
// denial text, audit decisions, replay fixtures, e2e read-then-send).
package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"

	"google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
)

// installFlowGate wires an approval gate for the ask-human path and
// restores the package globals afterwards. mode is the configured
// approval.mode ("interactive", "allow", "deny").
func installFlowGate(t *testing.T, mode string, approve func(req interfaces.ApprovalRequest) (bool, error)) {
	t.Helper()
	savedDeps, savedRt := deps, rt
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: mode}}
	rt = &Runtime{}
	if approve != nil {
		rt.SetApprovalGate(&mockApprovalGate{approveFunc: approve})
	}
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })
}

// exfilPolicy is the canonical v2 policy: a private read taints the
// audience, a public send needs the widest label.
func exfilPolicy() permissions.Policy {
	return permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "docs/internal/*", Effect: "allow", Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}}
}

// TestFlowAskHumanApproveProceeds pins the ask-human remedy: a flow ask
// reaches the human through the existing approval gate, and an approved
// call runs.
func TestFlowAskHumanApproveProceeds(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	var asked int
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		if req.FlowReason == "" {
			t.Error("flow ask without a flow reason")
		}
		if req.FlowArgsHash == "" {
			t.Error("flow ask without an args hash")
		}
		if req.SessionID == "" {
			t.Error("flow ask without a session id")
		}
		return true, nil
	})
	sess := "flow-ask-approve"
	ctx := flowSessionCtx(t, "task-ask-approve", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	ok, blocked, result, eff, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || blocked {
		t.Fatalf("approved flow ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	if result != nil {
		t.Errorf("approved call must run, got result %v", result)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if asked != 1 {
		t.Errorf("gate asked %d times, want 1", asked)
	}
}

// TestFlowAskHumanDenyDenies pins that a denied flow ask ends in the
// denial text with the remedies named.
func TestFlowAskHumanDenyDenies(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return false, nil
	})
	sess := "flow-ask-deny"
	ctx := flowSessionCtx(t, "task-ask-deny", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	ok, blocked, result, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || !blocked {
		t.Fatalf("denied flow ask: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if result["blocked_by"] != "flow_guard" {
		t.Errorf("result = %v, want blocked_by=flow_guard", result)
	}
	msg, _ := result["error"].(string)
	if !strings.Contains(msg, "Offer: ask-human | sanitize | drop-result") {
		t.Errorf("denial text lacks the remedies: %q", msg)
	}
}

// TestFlowAskModeAllowNeverAutoApproves pins H5: approval.mode=allow
// never auto-approves a flow ask - the human still decides.
func TestFlowAskModeAllowNeverAutoApproves(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	var asked int
	installFlowGate(t, "allow", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		return true, nil
	})
	sess := "flow-ask-mode-allow"
	ctx := flowSessionCtx(t, "task-ask-mode-allow", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || blocked {
		t.Fatalf("flow ask under mode=allow: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	if asked != 1 {
		t.Errorf("gate asked %d times, want 1 (mode=allow must not auto-approve a flow ask)", asked)
	}
}

// TestFlowAskModeAllowHeadlessFailsClosed pins the fail-closed headless
// case: mode=allow with no gate wired denies the flow ask instead of
// running the call.
func TestFlowAskModeAllowHeadlessFailsClosed(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	savedDeps, savedRt := deps, rt
	deps = &Deps{ApprovalCfg: config.ApprovalConfig{Mode: "allow"}}
	rt = nil
	t.Cleanup(func() { deps, rt = savedDeps, savedRt })

	sess := "flow-ask-headless"
	ctx := flowSessionCtx(t, "task-ask-headless", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	ok, blocked, result, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || !blocked {
		t.Fatalf("headless flow ask: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if result == nil {
		t.Error("denied flow ask must return the denial text")
	}
}

// TestFlowApprovalSingleUse pins the single-use binding: an approval
// covers exactly one dispatch of one exact call. The same call asks
// again, and a different payload never reuses the approval.
func TestFlowApprovalSingleUse(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	var asked int
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		return true, nil
	})
	sess := "flow-single-use"
	ctx := flowSessionCtx(t, "task-single-use", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	args := map[string]any{"channel": "#general", "body": "hello"}
	if ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", args); !ok || blocked {
		t.Fatalf("first ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	if asked != 1 {
		t.Fatalf("asked = %d after the first call, want 1", asked)
	}
	// The same call again: the approval was consumed, so it asks again.
	if ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", args); !ok || blocked {
		t.Fatalf("second ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	if asked != 2 {
		t.Errorf("asked = %d after the repeat, want 2 (the approval is single-use)", asked)
	}
	// A different payload never reuses an approval.
	if ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "other"}); !ok || blocked {
		t.Fatalf("third ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	if asked != 3 {
		t.Errorf("asked = %d after a new payload, want 3", asked)
	}
}

// TestFlowApprovalDoesNotResetLabel pins that an approved flow ask never
// widens the session label.
func TestFlowApprovalDoesNotResetLabel(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return true, nil
	})
	sess := "flow-no-reset"
	ctx := flowSessionCtx(t, "task-no-reset", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	before := hakasesession.DefaultRegistry.Get(sess)

	if ok, blocked, _, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"}); !ok || blocked {
		t.Fatalf("approved ask: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	after := hakasesession.DefaultRegistry.Get(sess)
	if after == nil || before == nil {
		t.Fatal("session label missing")
	}
	if after.Audience != before.Audience || after.Trust != before.Trust || after.Version != before.Version {
		t.Errorf("label after approval = %+v, want unchanged %+v", after, before)
	}
}

// TestFlowSanitizeAutoProceedNarrow pins the sanitize remedy: a narrow
// sanitizable rule redacts the payload and the call proceeds.
func TestFlowSanitizeAutoProceedNarrow(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "alerts/*", Effect: "allow", Sanitizable: true},
	}})
	sess := "flow-sanitize-narrow"
	ctx := flowSessionCtx(t, "task-sanitize-narrow", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	args := map[string]any{"channel": "alerts/ops", "body": "token=supersecret123456"}
	ok, blocked, _, _, _ := CheckPreFlow(ctx, "send_message", args)
	if !ok || blocked {
		t.Fatalf("sanitizable payload: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	body, _ := args["body"].(string)
	if strings.Contains(body, "supersecret123456") {
		t.Errorf("payload not redacted in place: %q", body)
	}
	if !strings.Contains(body, "REDACTED") {
		t.Errorf("payload not redacted: %q", body)
	}
}

// TestFlowSanitizeWildcardStillAsks pins H1: a wildcard resource never
// auto-proceeds, even with sanitizable:true - the tainted payload asks.
func TestFlowSanitizeWildcardStillAsks(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Sanitizable: true},
	}})
	sess := "flow-sanitize-wildcard"
	ctx := flowSessionCtx(t, "task-sanitize-wildcard", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	args := map[string]any{"channel": "#general", "body": "token=supersecret123456"}
	ok, blocked, result, eff, _ := CheckPreFlow(ctx, "send_message", args)
	if !ok || !blocked {
		t.Fatalf("wildcard sanitizable: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if result["blocked_by"] != "flow_guard" {
		t.Errorf("result = %v, want blocked_by=flow_guard", result)
	}
	// The payload is NOT redacted behind the user's back: the ask
	// happens on the original payload.
	if body, _ := args["body"].(string); !strings.Contains(body, "supersecret123456") {
		t.Errorf("payload redacted before the ask: %q", body)
	}
}

// TestFlowSanitizeNeverRewritesCode pins that the sanitize remedy only
// touches message-like payloads: a secret-shaped code payload reaches
// the human, and the code itself is never rewritten behind the
// operator's back.
func TestFlowSanitizeNeverRewritesCode(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "python_interpreter", Resource: "*", Effect: "allow", Sanitizable: true},
	}})
	sess := "flow-sanitize-code"
	ctx := flowSessionCtx(t, "task-sanitize-code", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	code := "print('token=supersecret123456')"
	args := map[string]any{"code": code}
	ok, blocked, _, eff, _ := CheckPreFlow(ctx, "python_interpreter", args)
	if !ok || !blocked {
		t.Fatalf("tainted code: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if got, _ := args["code"].(string); got != code {
		t.Errorf("code rewritten: %q", got)
	}
}

// TestFlowSanitizedPayloadAsksWithoutTaintClaim pins that a sanitized
// payload is not blamed for a risk the sanitizer already removed: the
// ask names the real blocker (the requires failure), and the payload
// itself is redacted.
func TestFlowSanitizedPayloadAsksWithoutTaintClaim(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "alerts/*", Effect: "allow", Sanitizable: true,
			Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	sess := "flow-sanitize-requires"
	ctx := flowSessionCtx(t, "task-sanitize-requires", sess)
	// The label is internal: the public send still needs the human.
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	args := map[string]any{"channel": "alerts/ops", "body": "token=supersecret123456"}
	ok, blocked, _, eff, reason := CheckPreFlow(ctx, "send_message", args)
	if !ok || !blocked {
		t.Fatalf("sanitized payload under a requires failure: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if strings.Contains(reason, flowTaintReason) {
		t.Errorf("ask reason blames a taint the sanitizer removed: %q", reason)
	}
	if !strings.Contains(reason, "flow: send_message blocked") {
		t.Errorf("ask reason misses the real blocker: %q", reason)
	}
	if strings.Count(reason, "by permissions policy") != 1 {
		t.Errorf("ask reason repeats the policy suffix: %q", reason)
	}
	if body, _ := args["body"].(string); strings.Contains(body, "supersecret123456") {
		t.Errorf("payload not redacted: %q", body)
	}
}

// TestFlowDenialTextShape pins the FG-005 denial format and that the
// denial text never carries a secret.
func TestFlowDenialTextShape(t *testing.T) {
	l := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 3}
	want := "flow: send_message blocked (suspicious/internal v3). " +
		"Allowed: ask-human (approve this exact call). " +
		"Offer: ask-human | sanitize | drop-result"
	if got := flowDenialText("send_message", &l, permissions.EffectAsk); got != want {
		t.Errorf("denial text = %q, want %q", got, want)
	}
	res := flowBlockResult("send_message", "denied token=supersecret123456")
	msg, _ := res["error"].(string)
	if strings.Contains(msg, "supersecret123456") {
		t.Errorf("denial text leaks the secret: %q", msg)
	}
	if !strings.Contains(msg, "REDACTED") {
		t.Errorf("denial text not redacted: %q", msg)
	}
}

// TestFlowRecomputedNoticeOnFirstDenial pins the one-time resume notice
// (FG-005/M1): a pre-v2 session resumes conservative, the first denial
// the model actually sees says so once, and the recompute is audited.
func TestFlowRecomputedNoticeOnFirstDenial(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	// The human denies the flow ask: the denial the model sees is built
	// on that answer, and the notice rides it.
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		return false, nil
	})
	sess := "flow-recompute-1"
	// A pre-v2 session file: no flow field at all.
	old := hakasesession.NewSession("pre-v2")
	old.ID = sess
	old.Flow = nil
	if !hakasesession.RegisterLoadedWith(hakasesession.DefaultRegistry, old) {
		t.Fatal("RegisterLoadedWith did not recompute a pre-v2 session")
	}
	ctx := flowSessionCtx(t, "task-recompute-1", sess)

	ok, blocked, result, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || !blocked {
		t.Fatalf("pre-v2 send: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	msg, _ := result["error"].(string)
	if !strings.Contains(msg, "pre-v2 session resumed") {
		t.Errorf("first denial lacks the resume notice: %q", msg)
	}
	// The notice is one-time: the next denial carries none.
	_, _, result2, _, _ := CheckPreFlowAsk(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	msg2, _ := result2["error"].(string)
	if strings.Contains(msg2, "pre-v2 session resumed") {
		t.Errorf("second denial repeats the resume notice: %q", msg2)
	}
	// The recompute is audited exactly once.
	entries, err := ReadAuditEntries(AuditDir(), time.Time{})
	if err != nil {
		t.Fatalf("ReadAuditEntries: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Decision == "flow_recomputed" {
			n++
			if e.FlowLabel == "" {
				t.Error("flow_recomputed without a label snapshot")
			}
		}
	}
	if n != 1 {
		t.Errorf("flow_recomputed entries = %d, want 1", n)
	}
}

// TestFlowAuditDecisions pins the four flow decisions on the audit trail
// with their label snapshot and args hash (FG-006).
func TestFlowAuditDecisions(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "docs/internal/*", Effect: "allow", Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	sess := "flow-audit-1"
	ctx := flowSessionCtx(t, "task-audit-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	// flow_blocked: a public send under an internal label.
	CheckPreFlow(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})

	// flow_sanitized: a secret-shaped result is redacted.
	tl := stubTool(t, "send_message")
	CheckPostFlow(ctx, tl, map[string]any{"channel": "#general", "body": "x"},
		map[string]any{"status": "sent token=supersecret123456"}, nil)

	// flow_error_redacted: the tool failed with a secret in the error.
	CheckPostFlow(ctx, tl, map[string]any{"channel": "#general", "body": "x"},
		map[string]any{"status": "sent"}, errFlow("read failed token=supersecret123456"))

	entries, err := ReadAuditEntries(AuditDir(), time.Time{})
	if err != nil {
		t.Fatalf("ReadAuditEntries: %v", err)
	}
	seen := map[string]*CommandAuditEntry{}
	for i := range entries {
		e := entries[i]
		if strings.HasPrefix(e.Decision, "flow_") {
			cp := e
			seen[e.Decision] = &cp
		}
	}
	for _, want := range []string{"flow_blocked", "flow_sanitized", "flow_error_redacted"} {
		e, ok := seen[want]
		if !ok {
			t.Errorf("missing audit decision %q (got %v)", want, seen)
			continue
		}
		if e.FlowArgsHash == "" {
			t.Errorf("%s without an args hash", want)
		}
		if e.FlowLabel == "" {
			t.Errorf("%s without a label snapshot", want)
		}
		if strings.Contains(e.Reason, "supersecret123456") {
			t.Errorf("%s leaks the secret: %q", want, e.Reason)
		}
	}
}

// TestFlowDebugStderrOnly pins M5: the flow debug log goes to stderr
// only, never to the audit file.
func TestFlowDebugStderrOnly(t *testing.T) {
	t.Setenv("HAKASE_FLOW_DEBUG", "1")
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = origAuditDir })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	savedStderr := os.Stderr
	os.Stderr = w
	flowDebugf("ask approved tool=%s argsHash=%s", "send_message", "abc123")
	_ = w.Close()
	os.Stderr = savedStderr
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "argsHash=abc123") {
		t.Errorf("stderr = %q, want the debug line", string(out))
	}
	// The audit file never carries the debug line.
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "x", Decision: "allowed"})
	raw, err := os.ReadFile(filepath.Join(AuditDir(), "exec-audit.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile audit: %v", err)
	}
	if strings.Contains(string(raw), "argsHash=abc123") {
		t.Errorf("debug line landed in the audit file: %q", string(raw))
	}
}

// errFlow builds a test error carrying a secret.
func errFlow(msg string) error { return &flowTestError{msg: msg} }

// flowTestError is a minimal error type for the flow tests.
type flowTestError struct{ msg string }

func (e *flowTestError) Error() string { return e.msg }

// flowWiringSession is a minimal ADK session stub: the flow gate only
// needs the session id (the task id the run is registered under).
type flowWiringSession struct{ id string }

func (s flowWiringSession) ID() string                { return s.id }
func (s flowWiringSession) AppName() string           { return "test" }
func (s flowWiringSession) UserID() string            { return "u" }
func (s flowWiringSession) State() adksession.State   { return nil }
func (s flowWiringSession) Events() adksession.Events { return nil }
func (s flowWiringSession) LastUpdateTime() time.Time { return time.Time{} }

// flowWiringCtx is an ADK invocation context whose session resolves the
// task id, so the flow gate finds the hakase session of the run.
type flowWiringCtx struct {
	agent.ContextMock
	sess adksession.Session
}

func (c flowWiringCtx) Session() adksession.Session { return c.sess }

// TestFlowWiringAskReachesTheGate pins the production path: the ADK
// pre-dispatch callback (the exact callback the agents are built with)
// routes a flow ask to the human, lets an approved call run, and turns a
// denied one into the flow denial.
func TestFlowWiringAskReachesTheGate(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	approve := true
	var asked int
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		return approve, nil
	})
	sess := "flow-wiring-1"
	interfaces.RegisterTaskSession("task-wiring-1", sess)
	t.Cleanup(func() { interfaces.UnregisterTask("task-wiring-1") })
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	ctx := agent.NewContext(&flowWiringCtx{sess: flowWiringSession{id: "task-wiring-1"}})
	cb := makeHookBeforeToolCallback(hookTestRunner(t, hooks.Config{}))

	// Approved: the callback lets the tool run (nil, nil).
	res, err := cb(ctx, &fakeHookTool{name: "send_message"}, map[string]any{"channel": "#general", "body": "hello"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res != nil {
		t.Errorf("approved flow ask must let the tool run, got %v", res)
	}
	if asked != 1 {
		t.Errorf("gate asked %d times, want 1", asked)
	}

	// Denied: the flow denial becomes the tool result.
	approve = false
	res, err = cb(ctx, &fakeHookTool{name: "send_message"}, map[string]any{"channel": "#general", "body": "hello"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res == nil || res["blocked_by"] != "flow_guard" {
		t.Errorf("denied flow ask = %v, want the flow denial", res)
	}
	if asked != 2 {
		t.Errorf("gate asked %d times, want 2", asked)
	}
}

// TestFlowPythonAsksOnce pins that the python tool owns its flow
// approval: the pre-dispatch callback does not prompt for it, so the
// human is asked exactly once - on the python gate's prompt, which
// carries the flow reason.
func TestFlowPythonAsksOnce(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "python_interpreter", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	var asked int
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		if req.FlowReason == "" {
			t.Error("python flow ask without a flow reason")
		}
		return true, nil
	})
	sess := "flow-python-once"
	ctx := flowSessionCtx(t, "task-python-once", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")

	// The callback must not prompt for python: the gate below owns it.
	actx := agent.NewContext(&flowWiringCtx{sess: flowWiringSession{id: "task-python-once"}})
	cb := makeHookBeforeToolCallback(hookTestRunner(t, hooks.Config{}))
	res, err := cb(actx, &fakeHookTool{name: "python_interpreter"}, map[string]any{"code": "print(1)"})
	if err != nil {
		t.Fatalf("callback err: %v", err)
	}
	if res != nil {
		t.Errorf("the python flow ask must not block in the callback: %v", res)
	}
	if asked != 0 {
		t.Errorf("callback asked %d times, want 0 (the python gate owns the ask)", asked)
	}

	// The python gate asks once, with the flow reason.
	sb := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"python_interpreter": "allow"},
	}
	if err := checkPythonGate(ctx, sb, "print(1)", sess); err != nil {
		t.Fatalf("checkPythonGate: %v", err)
	}
	if asked != 1 {
		t.Errorf("python gate asked %d times, want 1", asked)
	}
}

// --- replay fixtures -------------------------------------------------------

// flowFixtureLabel / flowFixtureStep / flowFixtureFile mirror the fixture
// JSON in docs/flow-guard/fixtures.
type flowFixtureLabel struct {
	Trust    string `json:"trust"`
	Audience string `json:"audience"`
}

type flowFixtureStep struct {
	Tool         string           `json:"tool"`
	Args         []string         `json:"args"`
	LabelBefore  flowFixtureLabel `json:"label_before"`
	ApplyDeltaOn string           `json:"apply_delta_on"`
	Expect       string           `json:"expect"`
}

type flowFixtureFile struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Policy      permissions.Policy `json:"policy"`
	Steps       []flowFixtureStep  `json:"steps"`
}

// fixtureToolArgs maps a fixture step onto real tool arguments so the
// replay driver exercises the same (tool, args) -> (action, resources)
// mapping the gate uses.
func fixtureToolArgs(tool string, args []string) map[string]any {
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	switch tool {
	case "shell":
		return map[string]any{"command": first}
	case "read", "edit", "grep", "glob":
		return map[string]any{"path": first}
	case "webfetch", "download_file":
		return map[string]any{"url": first}
	case "ask_sidekick":
		return map[string]any{"question": first}
	case "send_message":
		m := map[string]any{"channel": first}
		if len(args) > 1 {
			m["body"] = args[1]
		}
		return m
	case "python_interpreter":
		return map[string]any{"code": first}
	default:
		return map[string]any{"title": first}
	}
}

// TestFlowReplayFixtures drives the scripted fixtures through the gate's
// own mapping (flowMapTool + LookupWithLabel + Fold) without running any
// tool (FG-006 replay, mirrors `appa replay`).
func TestFlowReplayFixtures(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	dir := filepath.Join("..", "..", "docs", "flow-guard", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir fixtures: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		var fx flowFixtureFile
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		t.Run(fx.Name, func(t *testing.T) {
			cp, err := permissions.Compile(fx.Policy)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			permissions.Install(cp)
			t.Cleanup(func() { permissions.Install(nil) })

			var label permissions.SessionLabel
			for i, st := range fx.Steps {
				trust, err := permissions.ParseTrust(st.LabelBefore.Trust)
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				aud, err := permissions.ParseAudience(st.LabelBefore.Audience)
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				label = permissions.SessionLabel{Trust: trust, Audience: aud}

				toolArgs := fixtureToolArgs(st.Tool, st.Args)
				action, resources := flowMapTool(st.Tool, toolArgs)
				eff, _, _, ok := permissions.LookupWithLabel(&label, action, resources...)
				if !ok {
					t.Fatalf("step %d: no policy installed for %s", i, action)
				}
				if st.Expect != "" && string(eff) != st.Expect {
					t.Errorf("step %d (%s): verdict = %q, want %q", i, st.Tool, eff, st.Expect)
				}
				if st.ApplyDeltaOn == "" {
					continue
				}
				delta, has := permissions.DeltaForInstalled(action, resources...)
				if !has {
					t.Fatalf("step %d: no delta for %s %v", i, action, resources)
				}
				next, err := permissions.FoldLabel(label, delta)
				if err != nil {
					t.Fatalf("step %d: fold: %v", i, err)
				}
				if i+1 >= len(fx.Steps) {
					t.Fatalf("step %d applies a delta with no next step", i)
				}
				wantTrust, _ := permissions.ParseTrust(fx.Steps[i+1].LabelBefore.Trust)
				wantAud, _ := permissions.ParseAudience(fx.Steps[i+1].LabelBefore.Audience)
				if next.Trust != wantTrust || next.Audience != wantAud {
					t.Errorf("step %d fold = %v/%v, next label_before = %v/%v",
						i, next.Trust, next.Audience, wantTrust, wantAud)
				}
			}
		})
	}
}

// TestFlowE2EReadThenSend pins the end-to-end exfil shape (L3): a private
// read narrows the session, the public send is refused with the
// remedies, and the private content never reaches the send.
func TestFlowE2EReadThenSend(t *testing.T) {
	installFlowPolicy(t, exfilPolicy())
	sess := "flow-e2e-1"
	ctx := flowSessionCtx(t, "task-e2e-1", sess)

	// The private read is allowed and folds the audience to internal.
	readTool := stubTool(t, "read_file")
	ok, blocked, _, _, _ := CheckPreFlow(ctx, "read_file", map[string]any{"path": "docs/internal/secret.md"})
	if !ok || blocked {
		t.Fatalf("private read: ok=%v blocked=%v, want true/false", ok, blocked)
	}
	override, ferr := CheckPostFlow(ctx, readTool, map[string]any{"path": "docs/internal/secret.md"},
		map[string]any{"content": "private notes"}, nil)
	if override != nil || ferr != nil {
		t.Fatalf("read result: override=%v err=%v, want nil/nil", override, ferr)
	}
	label := hakasesession.DefaultRegistry.Get(sess)
	if label == nil || label.Audience != permissions.AudienceInternal {
		t.Fatalf("label after the read = %+v, want internal", label)
	}

	// The public send is refused with the remedies named.
	ok, blocked, result, eff, reason := CheckPreFlow(ctx, "send_message", map[string]any{"channel": "#general", "body": "private notes"})
	if !ok || !blocked {
		t.Fatalf("public send: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if reason == "" {
		t.Error("empty flow reason")
	}
	msg, _ := result["error"].(string)
	if !strings.Contains(msg, "flow: send_message blocked") {
		t.Errorf("denial text = %q, want the flow denial", msg)
	}
	if strings.Contains(msg, "private notes") {
		t.Errorf("denial text leaks the payload: %q", msg)
	}
}
