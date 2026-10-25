// flowgate_test.go - Flow Guard Phase 2 dispatch + post-admission tests
// (T2.1-T2.4: sinks, error redact-and-fold, TOCTOU, delegate inherit,
// exfil fixtures at the gate layer).
package agent

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"

	"amurru/hakase/internal/util"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// flowTestCtx is an ADK-style invocation context carrying a task id.
type flowTestCtx struct {
	context.Context
	taskID string
}

func (f flowTestCtx) SessionID() string { return f.taskID }

// flowSessionCtx registers taskID -> sessionID and returns a ctx resolving
// to it.
func flowSessionCtx(t *testing.T, taskID, sessionID string) context.Context {
	t.Helper()
	interfaces.RegisterTaskSession(taskID, sessionID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	return flowTestCtx{Context: context.Background(), taskID: taskID}
}

// installFlowPolicy compiles and installs a v2 policy for the test (the
// flowExperimental gate is required for version 2 to load).
func installFlowPolicy(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
	// Flow decisions audit to exec-audit.jsonl: keep test writes out of
	// the working tree (logs/ is gitignored runtime state).
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	t.Cleanup(func() { auditLogDir = origAuditDir })
	// Flow folds persist best-effort via the session store: isolate the
	// label registry per test (parallel-safe, no store writes since deps
	// is nil in tests).
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })
}

// stubTool builds a named stub tool for post-flow tests.
func stubTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, err := util.NewDocTool(functiontool.Config{Name: name, Description: "test stub"},
		func(ctx agent.Context, args map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		})
	if err != nil {
		t.Fatalf("NewDocTool: %v", err)
	}
	return tl
}

func TestFlowMapTool(t *testing.T) {
	cases := []struct {
		tool     string
		args     map[string]any
		action   string
		wantRes  int
		wantFrag string
	}{
		{"system_exec", map[string]any{"command": "ls -la"}, "shell", 1, "ls -la"},
		{"read_file", map[string]any{"path": "docs/internal/x.md"}, "read", 2, "docs/internal/x.md"},
		{"write_file", map[string]any{"path": "a/b.txt"}, "edit", 2, "a/b.txt"},
		{"search_files", map[string]any{"path": "src"}, "grep", 2, "src"},
		{"python_interpreter", map[string]any{"code": "print(1)"}, "python_interpreter", 1, "code_hash:"},
		{"download_file", map[string]any{"url": "https://Example.com/x?a=1#frag"}, "download_file", 1, "example.com"},
		{"ask_sidekick", map[string]any{"question": "why?"}, "ask_sidekick", 2, "why?"},
		{"save_skill", map[string]any{"name": "foo"}, "save_skill", 2, "skills/foo.py"},
		{"vision", map[string]any{"image_url": "/tmp/a.png"}, "vision", 1, "/tmp/a.png"},
		{"send_message", map[string]any{"channel": "#g", "body": "hi"}, "send_message", 2, "#g"},
		{"mcp__github__create_issue", map[string]any{"title": "bug"}, "mcp__github__create_issue", 0, ""},
	}
	for _, c := range cases {
		action, res := flowMapTool(c.tool, c.args)
		if action != c.action {
			t.Errorf("%s: action = %q, want %q", c.tool, action, c.action)
		}
		if len(res) != c.wantRes {
			// H6: on Windows "/tmp/a.png" absolutizes with a volume
			// ("D:/tmp/a.png") alongside the raw leg. Accept the
			// volume-variant pair as the same single resource.
			if !(runtime.GOOS == "windows" && c.tool == "vision" && len(res) == 2 && strings.Contains(strings.Join(res, " "), c.wantFrag)) {
				t.Errorf("%s: %d resources, want %d (%v)", c.tool, len(res), c.wantRes, res)
			}
		}
		if c.wantFrag != "" && !strings.Contains(strings.Join(res, " "), c.wantFrag) {
			t.Errorf("%s: resources %v lack %q", c.tool, res, c.wantFrag)
		}
	}
}

func TestCheckPreFlowNoPolicy(t *testing.T) {
	permissions.Install(nil)
	ok, blocked, _, _, _ := CheckPreFlow(context.Background(), "send_message", map[string]any{"channel": "#g"})
	if ok || blocked {
		t.Errorf("no policy: ok=%v blocked=%v, want false/false (v1 parity)", ok, blocked)
	}
}

func TestCheckPreFlowExfilDenied(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "docs/internal/*", Effect: "allow", Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-exfil-1"
	ctx := flowSessionCtx(t, "task-exfil-1", sess)
	// Source folds the audience ceiling to internal...
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "flow:read:h", "h")
	// ...so the public send asks (escalates, never widens).
	ok, blocked, result, eff, reason := CheckPreFlow(ctx, "send_message", map[string]any{"channel": "#general", "body": "hello"})
	if !ok || !blocked {
		t.Fatalf("ok=%v blocked=%v, want true/true", ok, blocked)
	}
	if eff != permissions.EffectAsk {
		t.Errorf("effect = %q, want ask", eff)
	}
	if result["blocked_by"] != "flow_guard" {
		t.Errorf("result = %v, want blocked_by=flow_guard", result)
	}
	if reason == "" {
		t.Error("empty reason")
	}
}

func TestCheckPreFlowUnknownSinkFailClosed(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: "allow", Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	sess := "flow-unknown-1"
	ctx := flowSessionCtx(t, "task-unknown-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")
	// Unmapped sink on a narrowed label fails closed (C2)...
	ok, blocked, _, _, _ := CheckPreFlow(ctx, "frobnicate", map[string]any{"x": "1"})
	if !ok || !blocked {
		t.Fatalf("narrowed unknown sink: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	// ...while the start label keeps v1 behavior.
	ok, blocked, _, _, _ = CheckPreFlow(context.Background(), "frobnicate", map[string]any{"x": "1"})
	if !ok || blocked {
		t.Fatalf("start unknown sink: ok=%v blocked=%v, want true/false", ok, blocked)
	}
}

func TestCheckPostFlowErrorRedactFold(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: "allow", Delta: &permissions.FlowDelta{Trust: "suspicious"}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	sess := "flow-err-1"
	ctx := flowSessionCtx(t, "task-err-1", sess)
	tl := stubTool(t, "read_file")
	secret := "token=supersecret123456"
	result := map[string]any{"content": "config holds " + secret}
	runErr := errors.New("read failed: " + secret)
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"path": "a.txt"}, result, runErr)
	// Never a result override on error: (nil, redactedErr) preserves the
	// ADK failure with a redacted carrier (C1).
	if override != nil {
		t.Errorf("override = %v, want nil on error path", override)
	}
	if ferr == nil || strings.Contains(ferr.Error(), secret) {
		t.Errorf("redactedErr = %v, want redacted failure", ferr)
	}
	if got := result["content"].(string); strings.Contains(got, secret) || !strings.Contains(got, "REDACTED") {
		t.Errorf("result not redacted: %q", got)
	}
	// Conservative fold happened despite the error.
	if l := hakasesession.DefaultRegistry.Get(sess); l == nil || l.Trust != permissions.TrustSuspicious {
		t.Errorf("label after error = %+v, want suspicious", l)
	}
}

func TestCheckPostFlowWithholdTaintedEgress(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-withhold-1"
	// Narrow the session first (tainted by an earlier read).
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	ctx := flowSessionCtx(t, "task-withhold-1", sess)
	tl := stubTool(t, "send_message")
	secret := "token=supersecret123456"
	result := map[string]any{"status": "sent " + secret}
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "x"}, result, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("override = %v, want withhold", override)
	}
}

func TestCheckPostFlowPassthroughNoPolicy(t *testing.T) {
	permissions.Install(nil)
	secret := "token=supersecret123456"
	result := map[string]any{"content": secret}
	tl := stubTool(t, "read_file")
	override, ferr := CheckPostFlow(context.Background(), tl, map[string]any{"path": "a"}, result, nil)
	if override != nil || ferr != nil {
		t.Fatalf("no policy: override=%v err=%v, want nil/nil (v1 parity)", override, ferr)
	}
	if result["content"] != secret {
		t.Errorf("no policy must not redact: %q", result["content"])
	}
}

func TestDelegateFlowInheritAndMerge(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: "allow", Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	parent := "flow-parent-1"
	hakasesession.DefaultRegistry.Fold(parent, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	parentLabel := hakasesession.DefaultRegistry.Get(parent)

	// Child inherits the parent label via ctx (never a fresh session).
	childCtx := WithDelegateFlowState(context.Background(), parentLabel)
	ok, blocked, _, _, _ := CheckPreFlow(childCtx, "send_message", map[string]any{"channel": "#g"})
	if !ok || !blocked {
		t.Fatalf("child egress: ok=%v blocked=%v, want true/true (parent label denies)", ok, blocked)
	}
	// Child folds further (suspicious read)...
	foldFlowLabel(childCtx, permissions.FlowDelta{Trust: "suspicious"}, "ck", "h2")
	// ...and the terminal child label merges back narrowing-only (H4).
	st := DelegateFlowStateFromCtx(childCtx)
	st.mu.Lock()
	terminal := st.label
	st.mu.Unlock()
	MergeDelegateLabel(parent, terminal)
	if got := hakasesession.DefaultRegistry.Get(parent); got == nil ||
		got.Trust != permissions.TrustSuspicious || got.Audience != permissions.AudienceInternal {
		t.Errorf("merged parent = %+v, want suspicious/internal", got)
	}
}

func TestFlowParallelFoldRace(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: "allow", Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{AudienceContains: []string{"public"}}},
	}})
	sess := "flow-race-1"
	ctx := flowSessionCtx(t, "task-race-1", sess)
	// Parallel folds under the registry mutex (CAS): the send re-check
	// sees the folded label (H2 TOCTOU).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "race", "h")
		}()
	}
	wg.Wait()
	ok, blocked, _, _, _ := CheckPreFlow(ctx, "send_message", map[string]any{"channel": "#g"})
	if !ok || !blocked {
		t.Fatalf("post-fold send: ok=%v blocked=%v, want true/true", ok, blocked)
	}
}

func TestCheckChannelEgress(t *testing.T) {
	// No policy: always allow (v1 parity).
	if allow, _ := CheckChannelEgress("nope", "telegram", "telegram:1"); !allow {
		t.Error("no policy must allow")
	}
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	// Start label: allow.
	sess := "flow-chan-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	if allow, _ := CheckChannelEgress(sess, "telegram", "telegram:1"); !allow {
		t.Error("start label must allow")
	}
	// Narrowed label: withhold with a reason.
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	if allow, reason := CheckChannelEgress(sess, "telegram", "telegram:1"); allow || reason == "" {
		t.Errorf("narrowed must withhold, got allow=%v reason=%q", allow, reason)
	}
}

func TestPythonGateFlowDeny(t *testing.T) {
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() { auditLogDir = origAuditDir }()
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "python_interpreter", Resource: "*", Effect: "deny"},
	}})
	sb := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"python_interpreter": "allow"},
	}
	if err := checkPythonGate(context.Background(), sb, "print(1)", ""); err == nil ||
		!strings.Contains(err.Error(), "flow guard") {
		t.Errorf("want flow-guard denial, got %v", err)
	}
}
