// flow_grant_v2_test.go - S25 joint-hash v2 dual-accept tests.
package permissions

import (
	"strings"
	"testing"
	"time"
)

// resetGrantV2Store isolates the v2 joint store per test (same pattern as
// the v1 store tests: process-wide state, test-local reset).
func resetGrantV2Store(t *testing.T) {
	t.Helper()
	flowGrantV2Store.Lock()
	flowGrantV2Store.m = make(map[string]flowGrantV2Entry)
	flowGrantV2Store.order = nil
	flowGrantV2Store.Unlock()
	saved := flowGrantV2Now
	flowGrantV2Now = time.Now
	t.Cleanup(func() { flowGrantV2Now = saved })
}

// TestS25JointHashBindsAllLegs pins the joint binding: changing any one
// leg (session, tool, argsHash, version) misses the grant.
func TestS25JointHashBindsAllLegs(t *testing.T) {
	resetGrantV2Store(t)
	base := FlowGrantBinding{SessionID: "sess-joint", Tool: "send_message", ArgsHash: "abc123", LabelVersion: 3}
	GrantFlowBindingV2(base)
	mk := func(mut func(*FlowGrantBinding)) FlowGrantBinding {
		b := base
		mut(&b)
		return b
	}
	for name, other := range map[string]FlowGrantBinding{
		"session": mk(func(b *FlowGrantBinding) { b.SessionID = "sess-other" }),
		"tool":    mk(func(b *FlowGrantBinding) { b.Tool = "read" }),
		"hash":    mk(func(b *FlowGrantBinding) { b.ArgsHash = "def456" }),
		"version": mk(func(b *FlowGrantBinding) { b.LabelVersion = 4 }),
	} {
		if ConsumeFlowGrantV2(other) {
			t.Fatalf("leg %s: swapped leg must not consume", name)
		}
	}
	if !ConsumeFlowGrantV2(base) {
		t.Fatal("exact four-leg match must consume")
	}
	if ConsumeFlowGrantV2(base) {
		t.Fatal("grant is one-shot: second consume must fail")
	}
}

// TestS25DualAcceptStartLabel pins dual-accept at StartLabel: a legacy v1
// record (pre-upgrade grant) still satisfies a version-0 check.
func TestS25DualAcceptStartLabel(t *testing.T) {
	resetGrantV2Store(t)
	b := FlowGrantBinding{SessionID: "sess-dual", Tool: "read", ArgsHash: "h1", LabelVersion: 0}
	GrantFlowBinding(b) // legacy v1 record only, no v2
	if !HasFlowGrantDual(b) {
		t.Fatal("StartLabel: legacy v1 record must dual-accept")
	}
	if !ConsumeFlowGrantDual(b) {
		t.Fatal("StartLabel: legacy v1 record must dual-consume")
	}
	if ConsumeFlowGrantDual(b) {
		t.Fatal("one-shot: second consume must fail")
	}
}

// TestS25NarrowedRequiresFreshV2 pins fresh approval: on a narrowed
// label a legacy v1 record alone never satisfies; only the v2 joint
// record does.
func TestS25NarrowedRequiresFreshV2(t *testing.T) {
	resetGrantV2Store(t)
	b := FlowGrantBinding{SessionID: "sess-fresh", Tool: "send_message", ArgsHash: "h2", LabelVersion: 5}
	GrantFlowBinding(b) // legacy v1 only
	if HasFlowGrantDual(b) {
		t.Fatal("narrowed label: v1-only record must not satisfy (fresh v2 required)")
	}
	if ConsumeFlowGrantDual(b) {
		t.Fatal("narrowed label: v1-only record must not consume")
	}
	GrantFlowBindingV2(b) // fresh approval mints v2
	if !HasFlowGrantDual(b) {
		t.Fatal("narrowed label: v2 joint record must satisfy")
	}
	if !ConsumeFlowGrantDual(b) {
		t.Fatal("narrowed label: v2 joint record must consume")
	}
}

// TestS25ExpiredGrantDenied pins TTL: an expired v2 record fails closed
// (caller re-asks).
func TestS25ExpiredGrantDenied(t *testing.T) {
	resetGrantV2Store(t)
	now := time.Now()
	flowGrantV2Now = func() time.Time { return now }
	b := FlowGrantBinding{SessionID: "sess-ttl", Tool: "read", ArgsHash: "h3", LabelVersion: 2}
	GrantFlowBindingV2(b)
	flowGrantV2Now = func() time.Time { return now.Add(flowGrantV2TTL + time.Minute) }
	if HasFlowGrantV2(b) {
		t.Fatal("expired v2 record must report false")
	}
	if ConsumeFlowGrantDual(b) {
		t.Fatal("expired v2 record must not consume")
	}
}

// TestS25CrossSessionDenied pins session binding: a grant minted for one
// session never satisfies another, even with identical tool/hash/version.
func TestS25CrossSessionDenied(t *testing.T) {
	resetGrantV2Store(t)
	mint := FlowGrantBinding{SessionID: "sess-A", Tool: "send_message", ArgsHash: "hx", LabelVersion: 1}
	GrantFlowBindingV2(mint)
	other := mint
	other.SessionID = "sess-B"
	if HasFlowGrantDual(other) || ConsumeFlowGrantDual(other) {
		t.Fatal("cross-session consume must fail")
	}
	if !ConsumeFlowGrantV2(mint) {
		t.Fatal("owning session must still consume")
	}
}

// TestS25HashOnlyNoRaw pins hash-only grants: the joint key carries no
// raw argument bytes (hex hash alphabet only).
func TestS25HashOnlyNoRaw(t *testing.T) {
	k := JointGrantHash("sess", "send_message", "body:AKIAIOSFODNN7EXAMPLE", 1)
	for _, r := range k {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("joint key must be hex hash only, got %q", k)
		}
	}
	if strings.Contains(k, "AKIA") {
		t.Fatal("joint key must not embed raw args")
	}
}
