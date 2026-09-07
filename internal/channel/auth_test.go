package channel

import (
	"path/filepath"
	"testing"
	"time"

	"amurru/hakase/internal/channel/state"
)

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "channels.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}

func TestIsAllowedStatic(t *testing.T) {
	a := NewAuthenticator(newTestStore(t), "telegram", []int64{1, 2}, "")
	if !a.IsAllowed(1) || !a.IsAllowed(2) {
		t.Error("static allowlist IDs should be allowed")
	}
	if a.IsAllowed(3) {
		t.Error("unknown ID must be denied (deny-by-default)")
	}
	if !a.HasAnyUser() {
		t.Error("HasAnyUser should see the static allowlist")
	}
}

func TestPairingFlow(t *testing.T) {
	store := newTestStore(t)
	a := NewAuthenticator(store, "telegram", nil, "")
	if a.HasAnyUser() {
		t.Fatal("no users configured yet")
	}

	code, err := a.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code %q not 6 digits", code)
	}

	// Wrong code must not pair.
	if err := a.TryPair(7, "alice", "000000"); err == nil {
		t.Fatal("wrong code accepted")
	}
	if a.IsAllowed(7) {
		t.Fatal("user paired with a wrong code")
	}

	// Right code pairs and clears the pending code.
	if err := a.TryPair(7, "alice", code); err != nil {
		t.Fatalf("TryPair: %v", err)
	}
	if !a.IsAllowed(7) || !a.HasAnyUser() {
		t.Fatal("paired user should be allowed")
	}
	if err := a.TryPair(8, "mallory", code); err == nil {
		t.Fatal("reuse of the consumed code must fail")
	}
}

func TestPairingCodeExpiry(t *testing.T) {
	store := newTestStore(t)
	a := NewAuthenticator(store, "telegram", nil, "")
	code, _ := a.EnsurePairingCode()
	// Age the code past its TTL directly in the store.
	_ = store.Update(func(st *state.State) error {
		st.PendingPairing.ExpiresAt = time.Now().Add(-time.Minute)
		return nil
	})
	if err := a.TryPair(9, "bob", code); err == nil {
		t.Fatal("expired code accepted")
	}
	// EnsurePairingCode must rotate the expired code.
	fresh, err := a.EnsurePairingCode()
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if fresh == code {
		t.Fatal("expired code was not rotated")
	}
	if err := a.TryPair(9, "bob", fresh); err != nil {
		t.Fatalf("fresh code rejected: %v", err)
	}
}

func TestStaticPairingCode(t *testing.T) {
	store := newTestStore(t)
	a := NewAuthenticator(store, "telegram", nil, "123456")
	code, err := a.EnsurePairingCode()
	if err != nil || code != "123456" {
		t.Fatalf("static code = %q, %v", code, err)
	}
	if err := a.TryPair(5, "carol", "123456"); err != nil {
		t.Fatalf("static code pairing failed: %v", err)
	}
}

func TestDenyReplyCooldown(t *testing.T) {
	a := NewAuthenticator(newTestStore(t), "telegram", nil, "")
	if !a.DenyReplyAllowed(1) {
		t.Fatal("first deny reply must be allowed")
	}
	if a.DenyReplyAllowed(1) {
		t.Fatal("second deny reply within the cooldown must be suppressed")
	}
	if !a.DenyReplyAllowed(2) {
		t.Fatal("cooldown must be per-user")
	}
}

func TestAllowedIDs(t *testing.T) {
	a := NewAuthenticator(newTestStore(t), "telegram", []int64{30, 10, 20}, "")
	ids := a.AllowedIDs()
	if len(ids) != 3 || ids[0] != 10 || ids[1] != 20 || ids[2] != 30 {
		t.Errorf("AllowedIDs = %v, want sorted [10 20 30]", ids)
	}
}
