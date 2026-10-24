// flow_highs_test.go - Flow Guard highs fix-up lane tests (S1-S8, epic #124).
//
// Each stream has at least one acceptance test here; transport-level
// streams (S2 streaming, S8 fan-out) live in their channel packages.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

const flowHighsSecret = "token=supersecret123456"

// stubBenchTool builds a named stub tool outside *testing.T (benchmarks).
func stubBenchTool(name string) (tool.Tool, error) {
	return util.NewDocTool(functiontool.Config{Name: name, Description: "bench stub"},
		func(ctx agent.Context, args map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		})
}

// S3: typed slices are collected for taint scanning.
func TestMarshalTypedSlicesCollectStrings(t *testing.T) {
	result := map[string]any{
		"items": []string{"clean", "leak " + flowHighsSecret},
		"maps":  []map[string]string{{"k": "v " + flowHighsSecret}},
	}
	got := marshalResultStrings(result)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, flowHighsSecret) {
		t.Fatalf("typed payload strings missing from marshal: %q", got)
	}
}

// S3: typed slices and maps are redacted in place.
func TestRedactTypedSlicesAndMaps(t *testing.T) {
	secret := flowHighsSecret
	result := map[string]any{
		"items": []string{"clean", "leak " + secret},
		"maps":  []map[string]string{{"k": "v " + secret}},
	}
	if !redactResultInPlace(result) {
		t.Fatal("redact did not fire on typed payload")
	}
	for _, s := range marshalResultStrings(result) {
		if strings.Contains(s, secret) {
			t.Fatalf("secret survives redact: %q", s)
		}
	}
	if items, ok := result["items"].([]string); !ok || !strings.Contains(items[0], "clean") {
		t.Fatalf("clean entry corrupted: %v", result["items"])
	}
}

// S3: structs redact exported fields, skip unexported, never panic.
func TestReflectStructRedactSkipsUnexported(t *testing.T) {
	type payload struct {
		Exported   string
		unexported string
	}
	p := payload{Exported: "x " + flowHighsSecret, unexported: "y " + flowHighsSecret}
	result := map[string]any{"p": p, "pp": &payload{Exported: "z " + flowHighsSecret}}
	if !redactResultInPlace(result) {
		t.Fatal("redact did not fire on struct payload")
	}
	got, ok := result["p"].(payload)
	if !ok {
		t.Fatalf("struct value type changed: %T", result["p"])
	}
	if strings.Contains(got.Exported, flowHighsSecret) {
		t.Errorf("exported struct field not redacted: %q", got.Exported)
	}
	// Unexported fields are never followed: the value survives untouched
	// (best-effort, no panic, no silent rewrite of unaddressable data).
	if !strings.Contains(got.unexported, flowHighsSecret) {
		t.Errorf("unexported field was rewritten (must be skipped): %q", got.unexported)
	}
	if pp, ok := result["pp"].(*payload); !ok || strings.Contains(pp.Exported, flowHighsSecret) {
		t.Fatalf("pointer struct field not redacted: %+v", result["pp"])
	}
}

// S3: cycles terminate via the depth cap and report over-budget.
func TestWalkCycleTerminates(t *testing.T) {
	m := map[string]any{"k": "v"}
	m["self"] = m
	if _, over := marshalResultStringsCapped(m); !over {
		t.Error("cyclic walk should report over-budget (depth cap)")
	}
	if _, over := redactResultInPlaceCapped(m); !over {
		t.Error("cyclic redact should report over-budget (depth cap)")
	}
}

// S3: over-budget walks fail closed on a narrowed label.
func TestWalkOverBudgetFailClosed(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	sess := "flow-overbudget-1"
	ctx := flowSessionCtx(t, "task-overbudget-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	bulk := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		bulk = append(bulk, fmt.Sprintf("benign line %d with padding to look like a large read result", i))
	}
	result := map[string]any{"bulk": bulk}
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "hi"}, result, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("over-budget walk on narrowed label must withhold, got %v", override)
	}
}

// S3: IsStart short-circuits the taint walk (clean admit, still redacted).
func TestIsStartShortCircuitAdmits(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	sess := "flow-startshort-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	ctx := flowSessionCtx(t, "task-startshort-1", sess)
	tl := stubTool(t, "send_message")
	result := map[string]any{"status": "sent " + flowHighsSecret}
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "x"}, result, nil)
	if ferr != nil || override != nil {
		t.Fatalf("start label must admit: override=%v err=%v", override, ferr)
	}
	if got := result["status"].(string); strings.Contains(got, flowHighsSecret) {
		t.Errorf("start-label result not redacted: %q", got)
	}
}

// S3: admission microbench (ADR perf budget input: p50 overhead vs the
// pre-lane baseline on a 1MB read result).
func BenchmarkCheckPostFlow1MBReadResult(b *testing.B) {
	b.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	if err != nil {
		b.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	defer permissions.Install(nil)
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	defer func() { hakasesession.DefaultRegistry = origReg }()
	sess := "flow-bench-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	payload := strings.Repeat("benign log line with no secrets 0123456789\n", 25000) // ~1MB
	tl, err := stubBenchTool("read_file")
	if err != nil {
		b.Fatalf("stub: %v", err)
	}
	args := map[string]any{"path": "docs/big.log"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := map[string]any{"content": payload}
		if _, err := CheckPostFlow(context.Background(), tl, args, result, nil); err != nil {
			b.Fatalf("err = %v", err)
		}
	}
}

// S5: canonicalization matrix (a/../b, trailing slash, absolute vs
// relative, nonexistent write target, symlinked dir).
func TestFlowCanonicalPathMatrix(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want func(t *testing.T, canon string, resolved bool)
	}{
		{"dotdot", "a/../b", func(t *testing.T, canon string, resolved bool) {
			t.Helper()
			if strings.Contains(canon, "..") {
				t.Errorf("canon %q keeps ..", canon)
			}
			if !filepath.IsAbs(canon) {
				t.Errorf("canon %q not absolute", canon)
			}
			if !strings.HasSuffix(canon, string(filepath.Separator)+"b") {
				t.Errorf("canon %q lost tail", canon)
			}
		}},
		{"trailing slash", "a/b/", func(t *testing.T, canon string, resolved bool) {
			t.Helper()
			if strings.HasSuffix(canon, "/") && canon != "/" {
				t.Errorf("canon %q keeps trailing slash", canon)
			}
		}},
		{"nonexistent write target", filepath.Join(os.TempDir(), "hakase-no-such-dir-xyz", "a", "..", "b.txt"), func(t *testing.T, canon string, resolved bool) {
			t.Helper()
			if strings.Contains(canon, "..") {
				t.Errorf("canon %q keeps ..", canon)
			}
			if resolved {
				t.Errorf("nonexistent target must not report resolved")
			}
			if !strings.HasSuffix(canon, "b.txt") {
				t.Errorf("canon %q lost tail", canon)
			}
		}},
		{"empty", "", func(t *testing.T, canon string, resolved bool) {
			t.Helper()
			if canon != "" || resolved {
				t.Errorf("empty = %q,%v, want \"\",false", canon, resolved)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			canon, resolved := permissions.CanonicalPath(c.raw)
			c.want(t, canon, resolved)
		})
	}
}

// S5: symlinks resolve to the real path (never error the gate).
func TestFlowCanonicalSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "secret.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	canon, resolved := permissions.CanonicalPath(filepath.Join(link, "secret.md"))
	if !resolved {
		t.Error("existing symlinked path must report resolved")
	}
	if canon != filepath.Join(real, "secret.md") {
		t.Errorf("canon = %q, want %q", canon, filepath.Join(real, "secret.md"))
	}
}

// S5: dual-match legs (canonical primary, raw fallback).
func TestFlowMapToolDualMatchLegs(t *testing.T) {
	_, resources := flowMapTool("read_file", map[string]any{"path": "docs/internal/x.md"})
	if len(resources) != 2 {
		t.Fatalf("dual-match resources = %v, want [canonical raw]", resources)
	}
	if !filepath.IsAbs(resources[0]) {
		t.Errorf("primary leg %q must be absolute", resources[0])
	}
	if resources[1] != "docs/internal/x.md" {
		t.Errorf("fallback leg = %q, want raw input", resources[1])
	}
	if resources[0] == resources[1] {
		t.Error("legs must differ for relative input")
	}
	// Absolute clean inputs collapse to one leg.
	abs := filepath.Join(os.TempDir(), "f.md")
	_, single := flowMapTool("read_file", map[string]any{"path": abs})
	if len(single) != 1 {
		t.Errorf("absolute clean input legs = %v, want one", single)
	}
}

// S5: an absolute deny rule catches a `..` bypass the raw string misses.
func TestFlowCanonicalDenyClosesBypass(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := permissions.CanonicalPath(filepath.Join("flowtest-dir", "..", "flowtest-bypass.txt"))
	_ = canon
	denyTarget, _ := permissions.CanonicalPath("flowtest-bypass.txt")
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: denyTarget, Effect: permissions.EffectDeny},
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow},
	}})
	_ = cwd
	ctx := flowSessionCtx(t, "task-bypass-1", "bypass-1")
	ok, blocked, _, eff, _ := CheckPreFlow(ctx, "read_file", map[string]any{"path": "flowtest-dir/../flowtest-bypass.txt"})
	if !ok || !blocked {
		t.Fatalf("canonical deny bypassed: ok=%v blocked=%v", ok, blocked)
	}
	if eff != permissions.EffectDeny {
		t.Errorf("effect = %q, want deny", eff)
	}
}

// S5: the raw leg still matches relative rules (migration fallback).
func TestFlowRawLegFallback(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "flowtest/*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
	}})
	ctx := flowSessionCtx(t, "task-rawleg-1", "rawleg-1")
	ok, blocked, _, _, _ := CheckPreFlow(ctx, "read_file", map[string]any{"path": "flowtest/x.md"})
	if !ok || blocked {
		t.Fatalf("raw-leg allow missed: ok=%v blocked=%v", ok, blocked)
	}
	if l := hakasesession.DefaultRegistry.Get("rawleg-1"); l != nil {
		t.Errorf("pre-check must not fold by itself: %+v", l)
	}
}

// S5: leg attribution runs debug-gated without panicking.
func TestFlowAuditPathLegNoPanic(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow},
	}})
	t.Setenv("HAKASE_FLOW_DEBUG", "1")
	label := permissions.StartLabel()
	_, resources := flowMapTool("read_file", map[string]any{"path": "docs/x.md"})
	eff, rule, detail, _ := permissions.LookupWithLabel(&label, "read", resources...)
	flowAuditPathLeg(&label, "read", resources, rule, detail)
	flowAuditPathLeg(&label, "read", resources, nil, "")
	flowAuditPathLeg(&label, "read", []string{"only"}, rule, detail)
	_ = eff
}

// S1: unregistered-session Ask withholds at delivery (no approval
// round-trip exists for a channel reply).
func TestCheckChannelEgressUnregisteredAskWithholds(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	if allow, reason := CheckChannelEgress("s1-missing-session", "telegram", "telegram:1"); allow || reason == "" {
		t.Errorf("unregistered Ask must withhold, got allow=%v reason=%q", allow, reason)
	}
}

// S1: unregistered-session unknown sink withholds (nil = narrowest).
func TestCheckChannelEgressUnregisteredUnknownSinkWithholds(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Trust: "suspicious"}},
	}})
	if allow, _ := CheckChannelEgress("s1-missing-unknown", "discord", "discord:1"); allow {
		t.Error("unregistered unknown sink must withhold under v2")
	}
}

// S6: read-then-send in one turn withholds the clean send.
func TestCheckPostFlowToctouWithholdsCleanSend(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-toctou-1"
	ctx := flowSessionCtx(t, "task-toctou-1", sess)
	hakasesession.DefaultRegistry.Ensure(sess)
	args := map[string]any{"channel": "#general", "body": "hello"}
	if ok, blocked, _, _, _ := CheckPreFlow(ctx, "send_message", args); !ok || blocked {
		t.Fatal("send pre-check at start label must pass")
	}
	// A parallel read folds between pre-check and admission.
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "other-read", "h")
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, args, map[string]any{"status": "sent ok"}, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("stale-label clean send must withhold, got %v", override)
	}
}

// S6: version-compare fast path (no fold between checks admits clean).
func TestCheckPostFlowNoMoveAdmitsCleanSend(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-toctou-2"
	ctx := flowSessionCtx(t, "task-toctou-2", sess)
	hakasesession.DefaultRegistry.Ensure(sess)
	args := map[string]any{"channel": "#general", "body": "hello"}
	if ok, blocked, _, _, _ := CheckPreFlow(ctx, "send_message", args); !ok || blocked {
		t.Fatal("send pre-check at start label must pass")
	}
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, args, map[string]any{"status": "sent ok"}, nil)
	if ferr != nil || override != nil {
		t.Fatalf("unmoved clean send must admit: override=%v err=%v", override, ferr)
	}
}

// S6: source/egress split (clean sources admit after a label move).
func TestCheckPostFlowToctouAdmitsCleanSource(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-toctou-3"
	ctx := flowSessionCtx(t, "task-toctou-3", sess)
	hakasesession.DefaultRegistry.Ensure(sess)
	args := map[string]any{"path": "docs/a.md"}
	if ok, blocked, _, _, _ := CheckPreFlow(ctx, "read_file", args); !ok || blocked {
		t.Fatal("read pre-check at start label must pass")
	}
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "other-read", "h")
	tl := stubTool(t, "read_file")
	override, ferr := CheckPostFlow(ctx, tl, args, map[string]any{"content": "clean text"}, nil)
	if ferr != nil || override != nil {
		t.Fatalf("moved clean source must admit: override=%v err=%v", override, ferr)
	}
}

// S7: pre-grants never satisfy a flow ask.
func TestApproveExecFlowSkipsPreGrant(t *testing.T) {
	savedRt := rt
	rt = nil
	t.Cleanup(func() { rt = savedRt })
	grantApproval("flow-skip-t", "flow-skip-c", "sess-flow-skip")
	t.Cleanup(func() { consumePreGrant("flow-skip-t", "flow-skip-c", "sess-flow-skip") })
	ok, err := ApproveExec(context.Background(), ApprovalRequest{
		Tool: "flow-skip-t", Command: "flow-skip-c", FlowReason: "flow: send_message blocked",
	})
	if err == nil || ok {
		t.Fatalf("flow ask with pre-grant must fail closed headless, got ok=%v err=%v", ok, err)
	}
	if !consumePreGrant("flow-skip-t", "flow-skip-c", "sess-flow-skip") {
		t.Fatal("flow ask must not consume the pre-grant")
	}
}

// S7: legacy (flow-less) pause records fail closed under v2.
func TestResolveApprovalPauseLegacyFailsClosedUnderV2(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAsk},
	}})
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-legacy-1", ADKSessionID: "turn-legacy-1",
		Gate:   hakasesession.PauseGateApproval,
		Detail: map[string]any{"tool": "system_exec", "command": "ls /tmp"},
	})
	if _, err := ResolveApprovalPause(context.Background(), pauseID, true); err == nil ||
		!strings.Contains(err.Error(), "flow") {
		t.Fatalf("legacy pause under v2 must fail closed naming flow, got %v", err)
	}
	if recs, _ := preg.ListForSession("sess-legacy-1"); len(recs) != 1 {
		t.Fatal("refused legacy pause must keep its record for manual handling")
	}
}

// S7: flow-scoped pauses install the exact flow binding (no legacy grant).
func TestResolveApprovalPauseFlowInstallsFlowGrant(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: "sess-flow-1", ADKSessionID: "turn-flow-1",
		Gate:   hakasesession.PauseGateApproval,
		Detail: map[string]any{"tool": "send_message", "command": "flow:send_message #g", "flowReason": "flow: send_message blocked", "flowArgsHash": "abc123", "flowLabelVersion": uint64(3)},
	})
	drive, err := ResolveApprovalPause(context.Background(), pauseID, true)
	if err != nil || !drive {
		t.Fatalf("flow pause: drive=%v err=%v, want true,nil", drive, err)
	}
	b := flowAskBinding{sessionID: "sess-flow-1", tool: "send_message", argsHash: "abc123", labelVersion: 3}
	if !hasFlowGrant(b) {
		t.Fatal("flow pause must install the exact flow binding")
	}
	if !consumeFlowGrant(b) {
		t.Fatal("flow grant not consumable one-shot")
	}
	if hasFlowGrant(b) {
		t.Fatal("flow grant must be one-shot")
	}
}

// S7: pause detail carries flow keys under v2, absent without.
func TestApprovalPauseDetailKeys(t *testing.T) {
	req := ApprovalRequest{Tool: "send_message", Command: "flow:send_message #g", FlowReason: "flow: blocked", FlowArgsHash: "h1", FlowLabelVersion: 2}
	permissions.Install(nil)
	if d := approvalPauseDetail(req); d["flowReason"] != nil {
		t.Fatalf("v1 pause detail must not carry flow keys: %v", d)
	}
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	d := approvalPauseDetail(req)
	if d["flowReason"] != "flow: blocked" || d["flowArgsHash"] != "h1" || d["flowLabelVersion"] != uint64(2) {
		t.Fatalf("v2 pause detail missing flow binding: %v", d)
	}
	plain := approvalPauseDetail(ApprovalRequest{Tool: "system_exec", Command: "ls"})
	if _, ok := plain["flowReason"]; !ok {
		t.Fatalf("v2 non-flow pause must carry (empty) flow keys to mark itself new: %v", plain)
	}
}

// S7: flow label version survives pause detail round-trips.
func TestFlowDetailVersion(t *testing.T) {
	cases := []struct {
		in   any
		want uint64
	}{
		{uint64(3), 3}, {int(3), 3}, {float64(3), 3}, {"3", 3},
		{nil, 0}, {"bogus", 0}, {int(-1), 0},
	}
	for _, c := range cases {
		if got := flowDetailVersion(c.in); got != c.want {
			t.Errorf("flowDetailVersion(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// S4: delegate seed matrix (session-less vs unregistered, v1 vs v2).
func TestParentFlowForDelegateMatrix(t *testing.T) {
	// v1: everything seeds StartLabel (byte-identical parity).
	installV1Policy(t, permissions.Policy{Version: 1, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAsk},
	}})
	if l := parentFlowForDelegate(""); l == nil || !permissions.IsStart(*l) {
		t.Fatalf("v1 session-less = %+v, want start", l)
	}
	if l := parentFlowForDelegate("s4-missing-v1"); l == nil || !permissions.IsStart(*l) {
		t.Fatalf("v1 unregistered = %+v, want start", l)
	}
	// v2: session-less keeps StartLabel; unregistered propagates nil.
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	if l := parentFlowForDelegate(""); l == nil || !permissions.IsStart(*l) {
		t.Fatalf("v2 session-less = %+v, want start", l)
	}
	if l := parentFlowForDelegate("s4-missing-v2"); l != nil {
		t.Fatalf("v2 unregistered = %+v, want nil (fail-closed)", l)
	}
	sess := "s4-reg-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	if l := parentFlowForDelegate(sess); l == nil || !permissions.IsStart(*l) {
		t.Fatalf("v2 registered = %+v, want start copy", l)
	}
}

// S4: nil seeds narrowest under v2 (fail-closed), StartLabel otherwise.
func TestWithDelegateFlowStateNilMatrix(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	st := DelegateFlowStateFromCtx(WithDelegateFlowState(context.Background(), nil))
	if st == nil {
		t.Fatal("nil state")
	}
	st.mu.Lock()
	got := st.label
	st.mu.Unlock()
	if got.Trust != permissions.TrustSuspicious || got.Audience != permissions.AudienceSelf {
		t.Fatalf("v2 nil seed = %+v, want narrowest", got)
	}
	permissions.Install(nil)
	st = DelegateFlowStateFromCtx(WithDelegateFlowState(context.Background(), nil))
	st.mu.Lock()
	got = st.label
	st.mu.Unlock()
	if !permissions.IsStart(got) {
		t.Fatalf("v1 nil seed = %+v, want start", got)
	}
}

// S4: a nil-seeded child denies egress under v2; session-less allows.
func TestDelegateNilChildEgressDenied(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	nilChild := WithDelegateFlowState(context.Background(), nil)
	ok, blocked, _, _, _ := CheckPreFlow(nilChild, "send_message", map[string]any{"channel": "#g"})
	if !ok || !blocked {
		t.Fatalf("v2 nil-seeded child: ok=%v blocked=%v, want true/true", ok, blocked)
	}
	l := permissions.StartLabel()
	startChild := WithDelegateFlowState(context.Background(), &l)
	ok, blocked, _, _, _ = CheckPreFlow(startChild, "send_message", map[string]any{"channel": "#g", "body": "hi"})
	if !ok || blocked {
		t.Fatalf("session-less child at start: ok=%v blocked=%v, want true/false", ok, blocked)
	}
}

// H2: python flow ask carries the (argsHash, labelVersion) binding and the
// resume grant round-trips: approve -> pause -> ResolveApprovalPause
// installs the exact grant -> re-drive consumes it without prompting
// (one-shot).
func TestFlowPythonAskResumeRoundTrip(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "python_interpreter", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted"}},
	}})
	var captured interfaces.ApprovalRequest
	var asked int
	installFlowGate(t, "interactive", func(req interfaces.ApprovalRequest) (bool, error) {
		asked++
		captured = req
		return true, nil
	})
	sess := "flow-python-resume"
	ctx := flowSessionCtx(t, "task-python-resume", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")
	sb := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"python_interpreter": "allow"},
	}

	if err := checkPythonGate(ctx, sb, "print(1)", sess); err != nil {
		t.Fatalf("checkPythonGate: %v", err)
	}
	if asked != 1 {
		t.Fatalf("asked = %d, want 1", asked)
	}
	if captured.FlowReason == "" {
		t.Fatalf("python flow ask missing flow reason: %+v", captured)
	}
	if captured.FlowArgsHash == "" {
		t.Fatalf("python flow ask missing args hash (unresumable): %+v", captured)
	}
	if captured.FlowLabelVersion == 0 {
		t.Fatalf("python flow ask missing label version (unresumable): %+v", captured)
	}

	// Restart: the pause record scopes the resume grant to the exact call.
	dir := t.TempDir()
	preg := resumeTestDeps(t, dir)
	pauseID := recordPause(t, preg, hakasesession.PauseRecord{
		HakaseSessionID: sess, ADKSessionID: "turn-python-1",
		Gate: hakasesession.PauseGateApproval, Detail: approvalPauseDetail(captured),
	})
	drive, err := ResolveApprovalPause(ctx, pauseID, true)
	if err != nil || !drive {
		t.Fatalf("ResolveApprovalPause: drive=%v err=%v, want true,nil", drive, err)
	}

	// Headless re-drive: the exact call consumes the grant, no prompt.
	savedRt := rt
	rt = nil
	t.Cleanup(func() { rt = savedRt })
	if err := checkPythonGate(ctx, sb, "print(1)", sess); err != nil {
		t.Fatalf("re-drive must consume the flow grant without prompting: %v", err)
	}
	if asked != 1 {
		t.Fatalf("re-drive prompted again (asked=%d), grant was not consumed", asked)
	}
	// One-shot: a repeat of the same call asks again (headless: fails).
	if err := checkPythonGate(ctx, sb, "print(1)", sess); err == nil {
		t.Fatal("consumed grant must not cover a second dispatch")
	}
}

// H4: canonicalizer handles ~, Win32 aliases and workspace anchoring
// without ever erroring (the raw leg still matches).
func TestFlowCanonicalHomeAndWinAlias(t *testing.T) {
	home, herr := os.UserHomeDir()
	if herr == nil && home != "" {
		canon, _ := permissions.CanonicalPath("~/flowtest-x.md")
		if strings.Contains(canon, "~") {
			t.Errorf("~/x not expanded: %q", canon)
		}
		if !filepath.IsAbs(canon) {
			t.Errorf("~/x not absolute: %q", canon)
		}
	}
	stripCases := []struct{ in, notWant string }{
		{`\\?\C:\ws` + string(filepath.Separator) + `flowtest.txt`, `?`},
		{`C:\ws` + string(filepath.Separator) + `flowtest.txt:ads`, ":ads"},
	}
	for _, tc := range stripCases {
		canon, _ := permissions.CanonicalPath(tc.in)
		if strings.Contains(canon, tc.notWant) {
			t.Errorf("permissions.CanonicalPath(%q) = %q, want %q stripped", tc.in, canon, tc.notWant)
		}
	}
	// M1 loop 5: Win32 trailing-dot/space folds apply only to
	// windows-shaped inputs. A POSIX relative name keeps its trailing
	// dot (distinct file on POSIX); a drive-absolute spelling folds.
	posixTrail, _ := permissions.CanonicalPath("flowtest-dir" + string(filepath.Separator) + "flowtest.txt.")
	if !strings.HasSuffix(posixTrail, ".") {
		t.Errorf("POSIX trailing dot must survive: %q", posixTrail)
	}
	winTrail, _ := permissions.CanonicalPath(`C:\ws\flowtest.txt.`)
	if strings.HasSuffix(winTrail, ".") || strings.HasSuffix(winTrail, " ") {
		t.Errorf("windows trailing dot not folded: %q", winTrail)
	}
	// POSIX regression (M1): "a:b" is a valid POSIX name and must survive
	// unchanged on non-Windows (no drive/slash shape, no UNC).
	if posixColon, _ := permissions.CanonicalPath("a:b"); posixColon != "a:b" && !filepath.IsAbs(posixColon) {
		// Anchored absolute form still must keep the colon.
		if !strings.Contains(posixColon, ":") {
			t.Errorf("POSIX a:b lost its colon: %q", posixColon)
		}
	} else if !strings.Contains(posixColon, ":") {
		t.Errorf("POSIX a:b lost its colon: %q", posixColon)
	}
	// Workspace-root anchoring: a relative path canonicalizes under CWD.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := permissions.CanonicalPath(filepath.Join("flowtest-dir", "..", "flowtest-anchored.txt"))
	if !filepath.IsAbs(canon) {
		t.Errorf("relative input not anchored: %q", canon)
	}
	if !strings.HasPrefix(canon, cwd) {
		t.Errorf("canon %q not under CWD %q", canon, cwd)
	}
}

// M1: named string / []byte map types redact without panicking (Convert,
// not a blind type assertion).
func TestRedactNamedMapTypesNoPanic(t *testing.T) {
	type secString string
	type secBytes []byte
	secret := flowHighsSecret
	result := map[string]any{
		"named":  map[string]secString{"k": secString("leak " + secret)},
		"bytes":  map[string]secBytes{"k": secBytes("leak " + secret)},
		"direct": map[string]any{"s": secString("leak " + secret)},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("redact panicked on named types: %v", r)
		}
	}()
	if !redactResultInPlace(result) {
		t.Fatal("redact did not fire on named-type payload")
	}
	for _, s := range marshalResultStrings(result) {
		if strings.Contains(s, secret) {
			t.Fatalf("secret survives named-type redact: %q", s)
		}
	}
}

// M1: an incomplete redact (over-budget) withholds even a pure source on
// a narrowed label - never partial-admit.
func TestIncompleteRedactSourceWithholds(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	sess := "flow-incomplete-1"
	ctx := flowSessionCtx(t, "task-incomplete-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")
	bulk := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		bulk = append(bulk, fmt.Sprintf("bulk line %d with padding", i))
	}
	tl := stubTool(t, "read_file")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"path": "docs/a.md"}, map[string]any{"bulk": bulk}, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("incomplete redact on narrowed source must withhold, got %v", override)
	}
}

// M2: the fast-path walk counts during the walk (not just the final
// truncate) and the byte budget caps huge payloads.
func TestWalkCapsCountAndBytes(t *testing.T) {
	many := map[string]any{}
	for i := 0; i < 600; i++ {
		many[fmt.Sprintf("k%d", i)] = fmt.Sprintf("benign line %d", i)
	}
	got, over := marshalResultStringsCapped(many)
	if !over {
		t.Error("600-string walk must report over-budget")
	}
	if len(got) != flowWalkMaxStrings {
		t.Errorf("collected = %d, want %d", len(got), flowWalkMaxStrings)
	}
	huge := map[string]any{"big": strings.Repeat("x", flowWalkMaxBytes+1)}
	if _, over := marshalResultStringsCapped(huge); !over {
		t.Error("over-byte walk must report over-budget")
	}
	if _, over := redactResultInPlaceCapped(huge); !over {
		t.Error("over-byte redact must report over-budget")
	}
}

// M2: the IsStart gate skips the reflective supplement (huge typed
// payloads on widest stay admissible without an over-budget trip).
func TestIsStartSkipsReflection(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	sess := "flow-startrefl-1"
	hakasesession.DefaultRegistry.Ensure(sess)
	ctx := flowSessionCtx(t, "task-startrefl-1", sess)
	bulk := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		bulk = append(bulk, fmt.Sprintf("benign line %d", i))
	}
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "x"}, map[string]any{"bulk": bulk}, nil)
	if ferr != nil || override != nil {
		t.Fatalf("start label must admit without over-budget: override=%v err=%v", override, ferr)
	}
}

// M3: a missing binding on a narrowed label re-validates requires
// (fail-closed) instead of skipping the re-check.
func TestBindingMissingNarrowedWithholds(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow, Delta: &permissions.FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-bindmiss-1"
	ctx := flowSessionCtx(t, "task-bindmiss-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "other-read", "h")
	// No CheckPreFlow ran for this send: the binding is missing.
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "hello"}, map[string]any{"status": "sent ok"}, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("binding-missing clean send on narrowed label must withhold, got %v", override)
	}
}

// M3: a missing binding on the start label still admits (v1-equivalent:
// the pre-check would have passed there too).
func TestBindingMissingStartAdmits(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-bindmiss-2"
	ctx := flowSessionCtx(t, "task-bindmiss-2", sess)
	hakasesession.DefaultRegistry.Ensure(sess)
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "hello"}, map[string]any{"status": "sent ok"}, nil)
	if ferr != nil || override != nil {
		t.Fatalf("binding-missing clean send on start must admit: override=%v err=%v", override, ferr)
	}
}

// M3: evicted bindings fail closed the same way (the cap never widens).
func TestBindingEvictedFailsClosed(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow, Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	sess := "flow-evicted-1"
	ctx := flowSessionCtx(t, "task-evicted-1", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	before := permissions.FlowBindingsEvicted()
	for i := 0; i < 5000; i++ {
		permissions.RecordFlowBinding(sess, fmt.Sprintf("fill-%d", i), 0, "h")
	}
	if permissions.FlowBindingsEvicted() <= before {
		t.Fatal("filling past the cap must evict bindings")
	}
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "hello"}, map[string]any{"status": "sent ok"}, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("evicted-binding send on narrowed label must withhold, got %v", override)
	}
}
