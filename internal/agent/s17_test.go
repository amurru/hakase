// s17_test.go - pre-grant session scope, TTL and cap tests (S17).
package agent

import (
	"strconv"
	"testing"
	"time"

	"amurru/hakase/internal/permissions"
)

// itoa is a tiny int formatter for distinct grant keys.
func itoa(i int) string { return strconv.Itoa(i) }

// resetPreGrants clears the one-shot store; tests never run in parallel so
// no inter-test leak is possible.
func resetPreGrants(t *testing.T) {
	t.Helper()
	preGrants.Lock()
	preGrants.m = make(map[string]preGrantEntry)
	preGrants.order = nil
	preGrants.Unlock()
	savedNow := preGrantNow
	t.Cleanup(func() { preGrantNow = savedNow })
}

// TestS17PreGrantSessionScoped pins session scoping + one-shot: a grant
// minted for session A never satisfies session B, and the owning session
// consumes it exactly once.
func TestS17PreGrantSessionScoped(t *testing.T) {
	resetPreGrants(t)
	grantApproval("system_exec", "echo scoped", "sess-A")
	if consumePreGrant("system_exec", "echo scoped", "sess-B") {
		t.Fatal("cross-session consume: want false")
	}
	if !consumePreGrant("system_exec", "echo scoped", "sess-A") {
		t.Fatal("owning-session consume: want true")
	}
	if consumePreGrant("system_exec", "echo scoped", "sess-A") {
		t.Fatal("second consume: grant must be one-shot")
	}
}

// TestS17PreGrantTTLExpiry pins the 15-minute TTL with a fake clock: a
// grant consumed past expiry reports false (lazy expiry, no refresh).
func TestS17PreGrantTTLExpiry(t *testing.T) {
	resetPreGrants(t)
	now := time.Now()
	preGrantNow = func() time.Time { return now }
	grantApproval("system_exec", "echo ttl", "sess-ttl")
	now = now.Add(preGrantTTL + time.Minute)
	if consumePreGrant("system_exec", "echo ttl", "sess-ttl") {
		t.Fatal("expired grant: want false")
	}
	// No sliding window: a fresh grant at the new time works once.
	grantApproval("system_exec", "echo ttl2", "sess-ttl")
	if !consumePreGrant("system_exec", "echo ttl2", "sess-ttl") {
		t.Fatal("fresh grant after expiry: want true")
	}
}

// TestS17PreGrantCapEvictsOldest pins the process-wide cap: filling past
// maxPreGrants evicts the oldest grant (fail-closed direction: the evicted
// grant re-asks, never allows) while the newest stays consumable.
func TestS17PreGrantCapEvictsOldest(t *testing.T) {
	resetPreGrants(t)
	sess := "sess-cap"
	for i := 0; i <= maxPreGrants; i++ {
		grantApproval("system_exec", "cmd-cap-"+itoa(i), sess)
	}
	// maxPreGrants+1 distinct keys under a maxPreGrants cap: the first
	// insert must be evicted (fail-closed), the last must be live.
	if consumePreGrant("system_exec", "cmd-cap-0", sess) {
		t.Fatal("oldest grant past cap: want evicted (false)")
	}
	if !consumePreGrant("system_exec", "cmd-cap-"+itoa(maxPreGrants), sess) {
		t.Fatal("newest grant: want true")
	}
}

// TestS17PreGrantEmptySessionFailClosed pins fail-closed minting: an empty
// session never installs a grant, by tool+command or by hash.
func TestS17PreGrantEmptySessionFailClosed(t *testing.T) {
	resetPreGrants(t)
	grantApproval("system_exec", "echo empty", "")
	if consumePreGrant("system_exec", "echo empty", "") {
		t.Fatal("empty-session grant: want never installed")
	}
	if consumePreGrant("system_exec", "echo empty", "sess-any") {
		t.Fatal("empty-session mint must not satisfy any session")
	}
	h := permissions.ArgsHash("system_exec", "echo empty-hash")
	grantPreApprovalByHash(h, "")
	if consumePreGrant("system_exec", "echo empty-hash", "sess-any") {
		t.Fatal("empty-session hash grant: want never installed")
	}
	// The hash path works with a real session (same domain, same key).
	grantPreApprovalByHash(h, "sess-hash")
	if !consumePreGrant("system_exec", "echo empty-hash", "sess-hash") {
		t.Fatal("session hash grant: want true")
	}
}

// TestS17PreGrantDomainSeparated pins ADR condition 3: legacy and flow
// grants share TTL/cap behavior but never satisfy each other's consume -
// a legacy key cannot cover a flow ask and vice versa.
func TestS17PreGrantDomainSeparated(t *testing.T) {
	resetPreGrants(t)
	sess := "sess-dom"
	grantApproval("send_message", "hello", sess)
	flowKey := permissions.FlowGrantBinding{
		SessionID: sess, Tool: "send_message",
		ArgsHash: permissions.ArgsHash("send_message", "hello"),
	}
	if permissions.ConsumeFlowBinding(flowKey) {
		t.Fatal("legacy grant must not satisfy a flow consume")
	}
	grantFlowCall(flowAskBinding{sessionID: sess, tool: "send_message", argsHash: "abc-dom", labelVersion: 1})
	if !consumePreGrant("send_message", "hello", sess) {
		t.Fatal("legacy consume must still hold its own grant")
	}
	if !consumeFlowGrant(flowAskBinding{sessionID: sess, tool: "send_message", argsHash: "abc-dom", labelVersion: 1}) {
		t.Fatal("flow grant must remain consumable in its own domain")
	}
}
