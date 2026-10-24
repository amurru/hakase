// flow_s9s15_test.go - S11 delegation cache label binding + S13 python
// code preview redaction (highs fix-up lane, epic #124).
package agent

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
)

// installFlowV2 compiles and installs a v2 policy for the test.
func installFlowV2S9(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

// S11: the cache key binds the label (not disable). Same goal under a
// different label is different work; without v2 the key stays the bare
// goal (v1 parity).
func TestDelegationCacheKeyLabelBinding(t *testing.T) {
	norm := normalizeDelegationGoal("Summarize the repo")
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
	start := permissions.StartLabel()

	// v1: keys are the bare goal regardless of label (byte-identical).
	permissions.Install(nil)
	if got := delegationCacheKey(norm, &narrowed); got != norm {
		t.Errorf("v1 narrowed key = %q, want bare goal %q", got, norm)
	}
	if got := delegationCacheKey(norm, nil); got != norm {
		t.Errorf("v1 nil key = %q, want bare goal %q", got, norm)
	}

	installFlowV2S9(t, permissions.Policy{Version: 2, Default: "ask"})

	startKey := delegationCacheKey(norm, &start)
	narrowedKey := delegationCacheKey(norm, &narrowed)
	nilKey := delegationCacheKey(norm, nil)
	if startKey == narrowedKey {
		t.Errorf("start and narrowed keys collide: %q", startKey)
	}
	if nilKey == startKey || nilKey == narrowedKey {
		t.Errorf("nil key collides: nil=%q start=%q narrowed=%q", nilKey, startKey, narrowedKey)
	}
	moved := narrowed
	moved.Version++
	if again := delegationCacheKey(norm, &moved); again == narrowedKey {
		t.Errorf("version move must change the key: %q", again)
	}
	if again := delegationCacheKey(norm, &narrowed); again != narrowedKey {
		t.Errorf("same label must hit: %q vs %q", again, narrowedKey)
	}
}

// S11 end-to-end: a cached widest-label result is not served to a
// narrowed label (and vice versa) under v2.
func TestDelegationCacheLabelIsolation(t *testing.T) {
	installFlowV2S9(t, permissions.Policy{Version: 2, Default: "ask"})
	norm := normalizeDelegationGoal("isolation probe goal")
	start := permissions.StartLabel()
	narrowed := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}

	r := DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "wide"}
	delegationCachePut(delegationCacheKey(norm, &start), r)

	if _, ok := delegationCacheGet(delegationCacheKey(norm, &narrowed)); ok {
		t.Error("narrowed label hit a widest-label cache entry")
	}
	if got, ok := delegationCacheGet(delegationCacheKey(norm, &start)); !ok || got.Summary != "wide" {
		t.Errorf("same-label lookup = (%v,%v), want hit", got, ok)
	}
	// Cleanup the probe entry so later tests see a cold cache.
	delegationCache.mu.Lock()
	for k := range delegationCache.m {
		if strings.HasPrefix(k, norm) {
			delete(delegationCache.m, k)
		}
	}
	delegationCache.mu.Unlock()
}

// S13: the approval prompt carries the full truncated code (the human
// must see what they approve); redaction applies audit-only (audit
// entries + pause detail) - never the prompt, never the gate verdict.
func TestPythonGateCodePreviewRedacted(t *testing.T) {
	origRt := rt
	origAuditDir := auditLogDir
	auditLogDir = t.TempDir()
	defer func() {
		rt = origRt
		auditLogDir = origAuditDir
	}()

	secretCode := "import os\napi_key = \"supersecretvalue123\"\nprint(os.environ)"
	var captured string
	rt = &Runtime{}
	rt.SetApprovalGate(&mockApprovalGate{
		approveFunc: func(req ApprovalRequest) (bool, error) {
			captured = req.Command
			return true, nil
		},
	})

	sb := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"python_interpreter": "ask"},
	}
	if err := checkPythonGate(context.Background(), sb, secretCode, ""); err != nil {
		t.Fatalf("ask+approve must pass: %v", err)
	}
	// Prompt preservation: the approver sees the full code.
	if !strings.Contains(captured, "supersecretvalue123") {
		t.Errorf("approval prompt must preserve full code, got %q", captured)
	}
	// Pause detail redaction: the persisted record must not leak.
	d := approvalPauseDetail(ApprovalRequest{Tool: "python_interpreter", Command: secretCode, Risk: "high", Reason: "x"})
	if s, _ := d["command"].(string); strings.Contains(s, "supersecretvalue123") {
		t.Errorf("pause detail leaks code secret: %q", s)
	}
	if s, _ := d["command"].(string); !strings.Contains(s, "REDACTED") {
		t.Errorf("pause detail carries no redaction token: %q", s)
	}
	for _, e := range readAuditLines(t) {
		blob := e.Command + "\x00" + e.Reason
		if strings.Contains(blob, "supersecretvalue123") {
			t.Errorf("audit leaks code secret: %+v", e)
		}
	}
	foundRedacted := false
	for _, e := range readAuditLines(t) {
		if strings.Contains(e.Command, "REDACTED") {
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Error("audit carries no redaction token in approved entry")
	}
}

// H2 canonical hash: the agent binding must equal the shared
// permissions.FlowArgsHash triple (tool, action, resources) with the
// actual tool name, so pause records round-trip the same hash the
// re-drive consumes.
func TestFlowArgsHashCanonical(t *testing.T) {
	action, res := flowMapTool("python_interpreter", map[string]any{"code": "print(1)"})
	got := flowArgsHash("python_interpreter", action, res)
	want := permissions.FlowArgsHash("python_interpreter", action, res)
	if got != want {
		t.Fatalf("flowArgsHash = %q, want shared %q", got, want)
	}
	// Shell canonical tool: system_exec_start normalizes to system_exec.
	_, resShell := flowMapTool("system_exec_start", map[string]any{"command": "echo hi"})
	if h1, h2 := flowArgsHash("system_exec", "shell", resShell), flowArgsHash("system_exec_start", "shell", resShell); h1 != h2 {
		t.Fatalf("shell tool normalize mismatch: %q vs %q", h1, h2)
	}
}

// S11: same label in different sessions must not share; a cache hit
// still narrows the parent via Meet.
func TestDelegationCacheCrossSessionIsolation(t *testing.T) {
	installFlowV2S9(t, permissions.Policy{Version: 2, Default: "ask"})
	norm := normalizeDelegationGoal("cross-session probe")
	start := permissions.StartLabel()
	kA := delegationCacheKeyFor(norm, "sess-a", &start)
	kB := delegationCacheKeyFor(norm, "sess-b", &start)
	if kA == kB {
		t.Fatalf("cross-session keys collide: %q", kA)
	}
	delegationCachePut(kA, DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "a"})
	if _, ok := delegationCacheGet(kB); ok {
		t.Error("session B hit session A entry")
	}
	delegationCache.mu.Lock()
	for k := range delegationCache.m {
		if len(k) >= len(norm) && k[:len(norm)] == norm {
			delete(delegationCache.m, k)
		}
	}
	delegationCache.mu.Unlock()
}

func TestDelegationCacheMergeOnHit(t *testing.T) {
	installFlowV2S9(t, permissions.Policy{Version: 2, Default: "ask"})
	sess := "sess-merge-hit"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })
	norm := normalizeDelegationGoal("merge-on-hit probe")
	lbl, _ := hakasesession.DefaultRegistry.Get(sess), 0
	_ = lbl
	narrow := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 1}
	key := delegationCacheKeyFor(norm, sess, &permissions.SessionLabel{Trust: 0, Audience: 2})
	delegationCachePutWithLabel(key, DelegateTaskResult{TaskID: "t", Status: "completed", Summary: "cached"}, narrow, true)
	t.Cleanup(func() {
		delegationCache.mu.Lock()
		delete(delegationCache.m, key)
		delegationCache.mu.Unlock()
	})
	e, ok := delegationCacheEntryFor(key)
	if !ok || !e.hasLbl {
		t.Fatalf("entry missing: %v", ok)
	}
	MergeDelegateLabel(sess, e.label)
	if got := hakasesession.DefaultRegistry.Get(sess); got == nil || got.Trust != permissions.TrustSuspicious || got.Audience != permissions.AudienceInternal {
		t.Fatalf("merge-on-hit = %+v, want suspicious/internal", got)
	}
}

// V1 parity + no-fold: without a v2 policy every gate passes through
// and the label never moves.
func TestV1ParityNoFold(t *testing.T) {
	permissions.Install(nil)
	sess := "sess-v1-parity"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	before := hakasesession.DefaultRegistry.Get(sess)
	ok, blocked, _, _, _ := CheckPreFlow(flowSessionCtx(t, "task-v1-parity", sess), "send_message", map[string]any{"body": "hello"})
	if ok && blocked {
		t.Error("v1 CheckPreFlow blocked, want passthrough")
	}
	after := hakasesession.DefaultRegistry.Get(sess)
	if before.Version != after.Version {
		t.Errorf("v1 folded: v%d -> v%d", before.Version, after.Version)
	}
}
