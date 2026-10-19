package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustCompileV2(t *testing.T, p Policy) *CompiledPolicy {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	p.Version = 2
	cp, err := Compile(p)
	if err != nil {
		t.Fatalf("Compile v2: %v", err)
	}
	return cp
}

func labelOf(trust Trust, aud Audience) *SessionLabel {
	l := SessionLabel{Trust: trust, Audience: aud}
	return &l
}

// TestMeetMatrix pins trust-max / audience-min / version-bump-on-narrow.
func TestMeetMatrix(t *testing.T) {
	start := StartLabel()
	if !IsStart(start) {
		t.Fatal("StartLabel is not start")
	}
	narrow := SessionLabel{Trust: TrustSuspicious, Audience: AudienceInternal}
	m := Meet(start, narrow)
	if m.Trust != TrustSuspicious || m.Audience != AudienceInternal {
		t.Errorf("Meet = %+v, want suspicious/internal", m)
	}
	if m.Version != 1 {
		t.Errorf("Meet version = %d, want 1 (narrowing bump)", m.Version)
	}
	// Idempotent re-meet: no further bump.
	m2 := Meet(m, narrow)
	if m2.Version != 1 {
		t.Errorf("re-Meet version = %d, want 1 (no double bump)", m2.Version)
	}
	// Meet with start is a no-op.
	m3 := Meet(m, start)
	if m3.Trust != TrustSuspicious || m3.Audience != AudienceInternal || m3.Version != 1 {
		t.Errorf("Meet(narrowed, start) = %+v, want unchanged", m3)
	}
}

// TestZeroValueTrap pins the Go zero value is NOT the start label and nil
// is fail-closed (L1): never == SessionLabel{} anywhere.
func TestZeroValueTrap(t *testing.T) {
	var zero SessionLabel
	if IsStart(zero) {
		t.Error("zero SessionLabel counts as start, want non-start (trusted/self != trusted/public)")
	}
	if IsNilOrStart(nil) {
		t.Error("nil label counts as start, want fail-closed non-start")
	}
	cp := mustCompileV2(t, Policy{Rules: []Rule{
		{Action: "send_message", Resource: "*", Effect: EffectAllow,
			Requires: &FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	// Nil label on a gated send must not allow.
	if eff, _, _ := cp.EvaluateWithLabel(nil, "send_message", "#general"); eff == EffectAllow {
		t.Error("nil label send_message = allow, want ask/deny (fail-closed)")
	}
}

// TestFoldLabelVersion pins version bumps only on narrowing changes.
func TestFoldLabelVersion(t *testing.T) {
	start := StartLabel()
	n, err := FoldLabel(start, FlowDelta{Trust: "suspicious"})
	if err != nil {
		t.Fatalf("FoldLabel: %v", err)
	}
	if n.Trust != TrustSuspicious || n.Version != 1 {
		t.Errorf("fold trust = %+v, want suspicious v1", n)
	}
	// Non-narrowing re-fold: no bump.
	n2, err := FoldLabel(n, FlowDelta{Trust: "suspicious"})
	if err != nil {
		t.Fatalf("FoldLabel: %v", err)
	}
	if n2.Version != 1 {
		t.Errorf("re-fold version = %d, want 1 (no bump without narrowing)", n2.Version)
	}
	// Audience ceiling min.
	n3, err := FoldLabel(start, FlowDelta{Audience: []string{"internal"}})
	if err != nil {
		t.Fatalf("FoldLabel: %v", err)
	}
	if n3.Audience != AudienceInternal || n3.Version != 1 {
		t.Errorf("fold audience = %+v, want internal v1", n3)
	}
	// Empty delta is an error.
	if _, err := FoldLabel(start, FlowDelta{}); err == nil {
		t.Error("empty delta accepted, want error")
	}
	// Empty requires is an error at compile.
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	if _, err := Compile(Policy{Version: 2, Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow, Requires: &FlowRequires{}},
	}}); err == nil {
		t.Error("empty requires:{} accepted, want error (L2)")
	}
}

// TestCompileV2Gate pins the experimental gate: v2 rejected without it,
// v1 rejects flow keys.
func TestCompileV2Gate(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "")
	if _, err := Compile(Policy{Version: 2, Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow},
	}}); err == nil || !strings.Contains(err.Error(), "flowExperimental") {
		t.Errorf("v2 without gate err = %v, want fail-closed gate error", err)
	}
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	if _, err := Compile(Policy{Version: 2}); err != nil {
		t.Errorf("v2 with gate: %v, want nil", err)
	}
	if _, err := Compile(Policy{Version: 1, Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow, Delta: &FlowDelta{Trust: "suspicious"}},
	}}); err == nil {
		t.Error("v1 with delta accepted, want error")
	}
	if _, err := Compile(Policy{Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow, Sanitizable: true},
	}}); err == nil {
		t.Error("v0 with sanitizable accepted, want error")
	}
}

// TestV1Parity pins no-v2-fields = byte-identical behavior: WithLabel on
// the start label matches Evaluate for every verdict.
func TestV1Parity(t *testing.T) {
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "shell", Resource: "git *", Effect: EffectAllow},
		{Action: "read", Resource: "*.env", Effect: EffectDeny},
	}})
	start := StartLabel()
	for _, tc := range []struct {
		action string
		res    string
	}{
		{"shell", "git status"},
		{"shell", "rm -rf /tmp/x"},
		{"read", ".env"},
		{"read", "main.go"},
	} {
		want, wantRule := cp.Evaluate(tc.action, tc.res)
		got, gotRule, detail := cp.EvaluateWithLabel(&start, tc.action, tc.res)
		if got != want || detail != "" {
			t.Errorf("WithLabel(%q,%q) = %q/%q, want %q (parity)", tc.action, tc.res, got, detail, want)
		}
		if (wantRule == nil) != (gotRule == nil) {
			t.Errorf("WithLabel(%q,%q) rule nil mismatch", tc.action, tc.res)
		}
	}
}

// TestExfilDeny pins read-private-then-send-public denial (FG-003).
func TestExfilDeny(t *testing.T) {
	cp := mustCompileV2(t, Policy{Rules: []Rule{
		{Action: "read", Resource: "docs/internal/*", Effect: EffectAllow,
			Delta: &FlowDelta{Audience: []string{"internal"}}},
		{Action: "send_message", Resource: "*", Effect: EffectAllow,
			Requires: &FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	start := StartLabel()
	// Start label sends fine.
	if eff, _, _ := cp.EvaluateWithLabel(&start, "send_message", "#general"); eff != EffectAllow {
		t.Errorf("start send = %q, want allow", eff)
	}
	// After the private read the label narrows; the send denies/asks.
	narrowed, _ := FoldLabel(start, FlowDelta{Audience: []string{"internal"}})
	eff, _, detail := cp.EvaluateWithLabel(&narrowed, "send_message", "#general")
	if eff == EffectAllow || detail == "" {
		t.Errorf("narrowed send = %q/%q, want escalated with flow detail", eff, detail)
	}
}

// TestWebfetchAsk pins webfetch-then-send requiring Ask (FG-003).
func TestWebfetchAsk(t *testing.T) {
	cp := mustCompileV2(t, Policy{Rules: []Rule{
		{Action: "webfetch", Resource: "*", Effect: EffectAllow,
			Delta: &FlowDelta{Trust: "suspicious"}},
		{Action: "send_message", Resource: "*", Effect: EffectAllow,
			Requires: &FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	start := StartLabel()
	tainted, _ := FoldLabel(start, FlowDelta{Trust: "suspicious"})
	if eff, _, _ := cp.EvaluateWithLabel(&tainted, "send_message", "#general"); eff != EffectAsk {
		t.Errorf("tainted send = %q, want ask", eff)
	}
}

// TestUnknownSinkFailClosed pins C2: narrowed + unmapped asks, start unchanged.
func TestUnknownSinkFailClosed(t *testing.T) {
	cp := mustCompileV2(t, Policy{Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow,
			Delta: &FlowDelta{Trust: "suspicious"}},
	}})
	start := StartLabel()
	if eff, _, detail := cp.EvaluateWithLabel(&start, "frobnicate", "x"); eff != EffectAsk || detail != "" {
		t.Errorf("start unknown = %q/%q, want default ask without flow detail", eff, detail)
	}
	narrowed, _ := FoldLabel(start, FlowDelta{Trust: "suspicious"})
	eff, _, detail := cp.EvaluateWithLabel(&narrowed, "frobnicate", "x")
	if eff != EffectAsk || detail == "" {
		t.Errorf("narrowed unknown = %q/%q, want ask with flow detail", eff, detail)
	}
	// requires audience self passes at every ceiling (so unknown-sink
	// never uses a requires condition for fail-closed).
	cp2 := mustCompileV2(t, Policy{Rules: []Rule{
		{Action: "frobnicate", Resource: "*", Effect: EffectAllow,
			Requires: &FlowRequires{AudienceContains: []string{"self"}}},
	}})
	for _, l := range []SessionLabel{
		{Trust: TrustTrusted, Audience: AudienceSelf},
		{Trust: TrustSuspicious, Audience: AudienceInternal},
		StartLabel(),
	} {
		if eff, _, _ := cp2.EvaluateWithLabel(&l, "frobnicate", "x"); eff != EffectAllow {
			t.Errorf("requires-self at %+v = %q, want allow", l, eff)
		}
	}
}

// TestLayeringFlowMonotonic pins H5: lower layers add gates, never remove
// enterprise ones (union-at-eval), and allowManagedOnly strips widening.
func TestLayeringFlowMonotonic(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	ent, err := Compile(Policy{Version: 2, Rules: []Rule{
		{Action: "send_message", Resource: "*", Effect: EffectAllow, Source: "enterprise",
			Requires: &FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	if err != nil {
		t.Fatalf("Compile enterprise: %v", err)
	}
	proj, err := Compile(Policy{Version: 2, Rules: []Rule{
		{Action: "send_message", Resource: "*", Effect: EffectAllow, Source: "project"},
	}})
	if err != nil {
		t.Fatalf("Compile project: %v", err)
	}
	// Merged evaluation sees the union: enterprise gate survives the
	// gate-less project allow.
	merged := &CompiledPolicy{def: EffectAsk, version: 2, flow: true}
	merged.rules = append(append([]compiledRule{}, ent.rules...), proj.rules...)
	narrowed := SessionLabel{Trust: TrustSuspicious, Audience: AudiencePublic}
	if eff, _, detail := merged.EvaluateWithLabel(&narrowed, "send_message", "#g"); eff != EffectAsk || detail == "" {
		t.Errorf("merged tainted send = %q/%q, want ask (enterprise gate survives)", eff, detail)
	}
	// allowManagedOnly strips the widening project allow.
	var all []Rule
	for _, cr := range ent.rules {
		all = append(all, cr.rule)
	}
	for _, cr := range proj.rules {
		all = append(all, cr.rule)
	}
	stripped := stripWideningAllows(all, len(ent.rules), map[string]bool{"send_message": true})
	for _, r := range stripped[len(ent.rules):] {
		if r.Effect == EffectAllow && r.Requires == nil {
			t.Errorf("widening allow survived stripping: %+v", r)
		}
	}
}

// TestLoaderFlowV2 pins v2 loading through the real layer stack:
// enterprise requires survive a gate-less project allow (union at eval),
// allowManagedOnly strips the widening project allow, and the merged
// policy reports version 2 with flow enabled.
func TestLoaderFlowV2(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	root := t.TempDir()
	proj := ProjectPolicyPath(root)
	writePolicy(t, proj, `{"version":2,"rules":[
		{"action":"send_message","resource":"*","effect":"allow"}]}`)
	raw, _ := os.ReadFile(proj)
	trust := trustMap{FingerprintPolicy(raw): true}

	ent := filepath.Join(t.TempDir(), "enterprise.json")
	writePolicy(t, ent, `{"version":2,"rules":[
		{"action":"send_message","resource":"*","effect":"allow",
		 "requires":{"trust":"trusted","audience_contains":["public"]}}],
		"enterprise":{"allow_managed_only":true}}`)

	l := testLoader()
	l.EnterprisePath = ent
	l.Trust = trust
	lp, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lp.Policy.Version() != 2 || !lp.Policy.FlowEnabled() {
		t.Errorf("merged version/flow = %d/%v, want 2/true", lp.Policy.Version(), lp.Policy.FlowEnabled())
	}
	narrowed := SessionLabel{Trust: TrustSuspicious, Audience: AudiencePublic}
	if eff, _, detail := lp.Policy.EvaluateWithLabel(&narrowed, "send_message", "#g"); eff != EffectAsk || detail == "" {
		t.Errorf("tainted send = %q/%q, want ask (enterprise gate survives project allow)", eff, detail)
	}
	for _, cr := range lp.Policy.rules {
		if cr.rule.Source == "project" && cr.rule.Action == "send_message" &&
			cr.rule.Effect == EffectAllow && !cr.hasRequires {
			t.Errorf("widening project allow survived: %+v", cr.rule)
		}
	}
}

// TestNormalizeURL pins M4 URL canonicalization incl. IDNA.
func TestNormalizeURL(t *testing.T) {
	if got := NormalizeURL("https://example.com/a#frag"); got != "https://example.com/a" {
		t.Errorf("fragment: %q", got)
	}
	if got := NormalizeURL("HTTPS://EXAMPLE.COM/a?x=1#z"); got != "https://example.com/a?x=1" {
		t.Errorf("case/query: %q", got)
	}
	// Homograph: evil with dotless i must punycode, not match evil.com.
	homograph := NormalizeURL("https://ev\u0131l.com/")
	if strings.Contains(homograph, "ev\u0131l.com") || !strings.Contains(homograph, "xn--") {
		t.Errorf("homograph not punycoded: %q", homograph)
	}
	// IPv6: brackets survive with and without a port.
	if got := NormalizeURL("http://[::1]:8080/x"); got != "http://[::1]:8080/x" {
		t.Errorf("ipv6 with port: %q", got)
	}
	if got := NormalizeURL("http://[::1]/x"); got != "http://[::1]/x" {
		t.Errorf("ipv6 without port: %q", got)
	}
	if got := NormalizeURL("not a url at all"); got != "not a url at all" {
		t.Errorf("unparseable changed: %q", got)
	}
	if h1, h2 := ArgsHash("a", "b"), ArgsHash("a", "b"); h1 != h2 || len(h1) != 64 {
		t.Errorf("ArgsHash unstable: %q %q", h1, h2)
	}
	if ArgsHash("a") == ArgsHash("b") {
		t.Error("ArgsHash collision on distinct args")
	}
	if len(ShortHash("x")) != 12 {
		t.Errorf("ShortHash len = %d, want 12", len(ShortHash("x")))
	}
}

// TestFlowBindingRoundTrip pins the (sessionID, labelVersion, argsHash)
// record, scoped per session so reused flow keys never collide.
func TestFlowBindingRoundTrip(t *testing.T) {
	RecordFlowBinding("s1", "test-key", 7, "abc123")
	v, h, ok := FlowBindingFor("s1", "test-key")
	if !ok || v != 7 || h != "abc123" {
		t.Errorf("binding = %d/%q/%v, want 7/abc123/true", v, h, ok)
	}
	if _, _, ok := FlowBindingFor("s1", "missing"); ok {
		t.Error("missing binding found, want false")
	}
	// Same idemKey in another session is a distinct binding.
	RecordFlowBinding("s2", "test-key", 1, "def456")
	v, h, ok = FlowBindingFor("s2", "test-key")
	if !ok || v != 1 || h != "def456" {
		t.Errorf("s2 binding = %d/%q/%v, want 1/def456/true", v, h, ok)
	}
	if v, _, _ := FlowBindingFor("s1", "test-key"); v != 7 {
		t.Errorf("s1 binding overwritten = %d, want 7", v)
	}
}
