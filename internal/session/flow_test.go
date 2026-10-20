package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestFlowKeyRoundTrip pins the canonical lossless encoding incl. empty
// tool-ids, colons inside ids, and the max-uint64 boundary.
func TestFlowKeyRoundTrip(t *testing.T) {
	cases := []struct {
		seq int64
		id  string
	}{
		{0, "call_1"},
		{42, ""},
		{7, "a:b:c:d"},
		{1 << 62, "x"},
		{-1, "neg"}, // encodes seq as max-uint64, decodes back
	}
	for _, tc := range cases {
		key := FlowKey(tc.seq, tc.id)
		seq, id, err := ParseFlowKey(key)
		if err != nil {
			t.Fatalf("ParseFlowKey(%q): %v", key, err)
		}
		if seq != tc.seq || id != tc.id {
			t.Errorf("round-trip %q = (%d,%q), want (%d,%q)", key, seq, id, tc.seq, tc.id)
		}
	}
	// Max-uint64 boundary decodes (int64 -1).
	maxKey := fmt.Sprintf("seq:%d:%d:%s", ^uint64(0), 1, "z")
	if seq, id, err := ParseFlowKey(maxKey); err != nil || seq != -1 || id != "z" {
		t.Errorf("max-uint64 = (%d,%q,%v), want (-1,z,nil)", seq, id, err)
	}
	for _, bad := range []string{"", "seq:1", "seq:x:1:a", "seq:1:5:ab", "nope:1:1:a"} {
		if _, _, err := ParseFlowKey(bad); err == nil {
			t.Errorf("ParseFlowKey(%q) = nil error, want error", bad)
		}
	}
}

// TestRegistryFoldIdempotent pins CAS + no-op re-admit + version discipline.
func TestRegistryFoldIdempotent(t *testing.T) {
	r := NewFlowRegistry()
	l := r.Ensure("s1")
	if l.Trust != permissions.TrustTrusted || l.Audience != permissions.AudiencePublic || l.Version != 0 {
		t.Fatalf("Ensure = %+v, want start v0", l)
	}
	key := FlowKey(3, "call_9")
	hash := permissions.ShortHash("read", "docs/internal/a.md")
	n1, narrowed := r.Fold("s1", permissions.FlowDelta{Trust: "suspicious"}, key, hash)
	if !narrowed || n1.Trust != permissions.TrustSuspicious || n1.Version != 1 {
		t.Fatalf("fold = %+v/%v, want suspicious v1 narrowed", n1, narrowed)
	}
	n2, narrowed2 := r.Fold("s1", permissions.FlowDelta{Trust: "suspicious"}, key, hash)
	if narrowed2 || n2.Version != 1 {
		t.Fatalf("re-admit = %+v/%v, want no-op v1", n2, narrowed2)
	}
	if v, h, ok := permissions.FlowBindingFor("s1", key); !ok || v != 1 || h != hash {
		t.Errorf("binding = %d/%q/%v, want 1/%q/true", v, h, ok, hash)
	}
}

// TestRegistryConcurrent pins race safety: parallel folds of one key
// narrow exactly once under -race.
func TestRegistryConcurrent(t *testing.T) {
	r := NewFlowRegistry()
	const n = 16
	key := FlowKey(1, "call_race")
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Fold("race", permissions.FlowDelta{Audience: []string{"internal"}}, key, "h")
		}()
	}
	wg.Wait()
	l := r.Get("race")
	if l == nil || l.Audience != permissions.AudienceInternal || l.Version != 1 {
		t.Fatalf("concurrent fold = %+v, want internal v1", l)
	}
	if r.Get("missing") != nil {
		t.Error("Get(missing) non-nil, want nil")
	}
}

// TestRecomputeFlow pins old-file conservative backfill, never widest.
func TestRecomputeFlow(t *testing.T) {
	s := NewSession("old")
	if s.Flow != nil {
		t.Fatal("new session Flow non-nil")
	}
	if !s.RecomputeFlow() {
		t.Fatal("RecomputeFlow = false, want true")
	}
	if s.Flow.Trust != permissions.TrustSuspicious || s.Flow.Audience != permissions.AudienceInternal {
		t.Errorf("recomputed = %+v, want suspicious/internal", s.Flow)
	}
	if s.RecomputeFlow() {
		t.Error("second RecomputeFlow = true, want false")
	}
	if got := FlowResumeNotice(true); !strings.Contains(got, "pre-v2") {
		t.Errorf("notice = %q, want pre-v2 text", got)
	}
	if got := FlowResumeNotice(false); got != "" {
		t.Errorf("notice = %q, want empty", got)
	}
}

// TestSessionFlowPersist pins Flow/FlowApplied JSON round-trip and
// omitempty (nil Flow omitted so old files stay small).
func TestSessionFlowPersist(t *testing.T) {
	s := NewSession("plain")
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"flow"`) {
		t.Error("nil Flow serialized, want omitted")
	}
	s.EnsureFlow()
	s.MarkFlowApplied(FlowKey(0, "c1"))
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Session
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Flow == nil || !permissions.IsStart(*back.Flow) {
		t.Errorf("round-trip Flow = %+v, want start", back.Flow)
	}
	if !back.FlowApplied[FlowKey(0, "c1")] {
		t.Error("round-trip FlowApplied lost key")
	}
	if s.MarkFlowApplied(FlowKey(0, "c1")) {
		t.Error("double MarkFlowApplied = true, want false")
	}
}

// TestRegisterLoaded pins resume registration: persisted labels import
// verbatim, absent flow recomputes conservative, and Sync round-trips
// folds back into the session for Save.
func TestRegisterLoaded(t *testing.T) {
	r := NewFlowRegistry()
	s := NewSession("resume")
	s.Flow = &permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 3}
	s.FlowApplied = map[string]bool{FlowKey(9, "c9"): true}
	if recomputed := RegisterLoadedWith(r, s); recomputed {
		t.Error("RegisterLoadedWith recomputed persisted flow, want false")
	}
	got := r.Get(s.ID)
	if got == nil || got.Version != 3 || got.Audience != permissions.AudienceInternal {
		t.Fatalf("registry = %+v, want imported suspicious/internal v3", got)
	}
	// Fold then Sync: session carries the narrowing for Save.
	key := FlowKey(10, "c10")
	next, narrowed := r.Fold(s.ID, permissions.FlowDelta{Trust: "suspicious"}, key, "h10")
	if narrowed {
		t.Error("already-suspicious fold narrowed, want no-op version")
	}
	r.Sync(s)
	if s.Flow.Version != next.Version || !s.FlowApplied[key] || !s.FlowApplied[FlowKey(9, "c9")] {
		t.Errorf("synced session = %+v/%v, want v%d with both keys", s.Flow, s.FlowApplied, next.Version)
	}
	// Absent flow recomputes conservative, never widest.
	old := NewSession("old")
	if recomputed := RegisterLoadedWith(r, old); !recomputed {
		t.Error("RegisterLoadedWith old = false, want true")
	}
	if old.Flow.Trust != permissions.TrustSuspicious || old.Flow.Audience != permissions.AudienceInternal {
		t.Errorf("recomputed = %+v, want suspicious/internal", old.Flow)
	}
}

// TestStoreLoadRegistersFlow pins the Store.Load resume path: a narrowed
// persisted session is visible to the sandbox resolvers (DefaultRegistry)
// instead of falling back to StartLabel.
func TestStoreLoadRegistersFlow(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	s := NewSession("narrowed")
	s.Flow = &permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceSelf, Version: 2}
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := DefaultRegistry.Get(s.ID)
	t.Cleanup(func() {
		DefaultRegistry = NewFlowRegistry()
		_ = loaded
	})
	if got == nil || got.Version != 2 || got.Audience != permissions.AudienceSelf {
		t.Errorf("DefaultRegistry after Load = %+v, want suspicious/self v2", got)
	}
}

// TestRegistryMergeAtomic pins atomic delegate merges (H2/H4): a Merge
// racing a Fold must keep both narrowings, never overwrite one with a
// stale snapshot (no widening under -race).
func TestRegistryMergeAtomic(t *testing.T) {
	r := NewFlowRegistry()
	r.Ensure("p")
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				r.Fold("p", permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
			} else {
				r.Merge("p", permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudiencePublic})
			}
		}(i)
	}
	wg.Wait()
	got := r.Get("p")
	if got == nil || got.Trust != permissions.TrustSuspicious || got.Audience != permissions.AudienceInternal {
		t.Fatalf("merged = %+v, want suspicious/internal (both narrowings kept)", got)
	}
	// Merge into an uninitialized session starts from StartLabel.
	m := r.Merge("fresh", permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal})
	if m.Trust != permissions.TrustSuspicious || m.Audience != permissions.AudienceInternal {
		t.Errorf("fresh merge = %+v, want suspicious/internal", m)
	}
}

// TestFlowCtx pins ctx carry for delegate inheritance (Phase 2 reads it).
func TestFlowCtx(t *testing.T) {
	if FlowLabelFromCtx(nil) != nil {
		t.Error("nil ctx label non-nil")
	}
	l := permissions.StartLabel()
	ctx := WithFlowLabel(context.Background(), &l)
	got := FlowLabelFromCtx(ctx)
	if got == nil || !permissions.IsStart(*got) {
		t.Errorf("ctx label = %+v, want start", got)
	}
}
