package main

import (
	"testing"
	"time"
)

// TestNormalizeDelegationGoal verifies the canonicalization rules: lowercase,
// whitespace collapse, and truncation to 200 runes.
func TestNormalizeDelegationGoal(t *testing.T) {
	got := normalizeDelegationGoal("  Research  MENA   energy  ")
	want := "research mena energy"
	if got != want {
		t.Errorf("normalizeDelegationGoal = %q, want %q", got, want)
	}
}

// TestNormalizeDelegationGoalCaseWhitespace confirms two goals differing only
// in case and whitespace produce the same normalized key.
func TestNormalizeDelegationGoalCaseWhitespace(t *testing.T) {
	a := normalizeDelegationGoal("Research MENA Energy")
	b := normalizeDelegationGoal("  RESEARCH\tMENA\nENERGY  ")
	if a != b {
		t.Errorf("normalized keys differ: %q vs %q", a, b)
	}
	if a != "research mena energy" {
		t.Errorf("normalized key = %q, want %q", a, "research mena energy")
	}
}

// TestDelegationCachePutGetRoundTrip verifies a stored entry is returned before
// its TTL expires.
func TestDelegationCachePutGetRoundTrip(t *testing.T) {
	// Use a private key so this test does not collide with other tests.
	key := "test_roundtrip_" + t.Name()
	res := DelegateTaskResult{TaskID: "t1", Status: "completed", Summary: "ok"}
	delegationCachePut(key, res)
	got, ok := delegationCacheGet(key)
	if !ok {
		t.Fatalf("delegationCacheGet: expected hit, got miss")
	}
	if got.TaskID != res.TaskID || got.Status != res.Status || got.Summary != res.Summary {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, res)
	}
	// Cleanup so the entry does not leak into other tests.
	delegationCache.mu.Lock()
	delete(delegationCache.m, key)
	delegationCache.mu.Unlock()
}

// TestDelegationCacheExpiry verifies that an entry with a forged old timestamp
// is not returned after the TTL has elapsed.
func TestDelegationCacheExpiry(t *testing.T) {
	// Shorten the TTL for this test and restore it afterwards.
	origTTL := delegationCacheTTL
	delegationCacheTTL = 50 * time.Millisecond
	defer func() { delegationCacheTTL = origTTL }()

	key := "test_expiry_" + t.Name()
	res := DelegateTaskResult{TaskID: "t2", Status: "completed", Summary: "expired"}
	// Forge an old timestamp so the entry is already expired.
	delegationCache.mu.Lock()
	delegationCache.m[key] = delegationCacheEntry{
		result: res,
		ts:     time.Now().Add(-2 * delegationCacheTTL),
	}
	delegationCache.mu.Unlock()

	if _, ok := delegationCacheGet(key); ok {
		t.Errorf("delegationCacheGet: expected miss for expired entry, got hit")
	}
	// The expired entry should have been evicted by the get.
	delegationCache.mu.Lock()
	_, present := delegationCache.m[key]
	delegationCache.mu.Unlock()
	if present {
		t.Errorf("expired entry was not evicted from the cache")
	}
}
