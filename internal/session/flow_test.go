package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	// A pre-v2 session file carries no flow field at all: the label is
	// nil until the recompute backfills it conservatively.
	s := NewSession("old")
	s.Flow = nil
	if s.Flow != nil {
		t.Fatal("pre-v2 session Flow non-nil")
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

// TestNewSessionStartsWidest pins that a new session starts at the start
// label: only a pre-v2 file (no flow field) resumes conservative.
func TestNewSessionStartsWidest(t *testing.T) {
	s := NewSession("new")
	if s.Flow == nil {
		t.Fatal("new session Flow nil")
	}
	if !permissions.IsStart(*s.Flow) {
		t.Errorf("new session label = %+v, want start", s.Flow)
	}
	r := NewFlowRegistry()
	if recomputed := RegisterLoadedWith(r, s); recomputed {
		t.Error("RegisterLoadedWith new session recomputed, want false")
	}
	if r.TakeFlowNotice(s.ID) != "" {
		t.Error("new session carries a resume notice, want none")
	}
}

// TestSessionFlowPersist pins Flow/FlowApplied JSON round-trip and
// omitempty (nil Flow omitted so old files stay small).
func TestSessionFlowPersist(t *testing.T) {
	s := NewSession("plain")
	// A pre-v2 session carries no flow field: the pointer stays nil so
	// omitempty drops it.
	s.Flow = nil
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
	old.Flow = nil
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

// TestFlowResumeCompactionVersionUnchanged pins H3: a fold survives a
// compaction and a resume with the same version - the label never
// widens and never double-folds on the way back.
func TestFlowResumeCompactionVersionUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	sess := NewSession("compaction")
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Fold once (the source read taints the audience).
	key := FlowKey(1, "call_1")
	before, narrowed := DefaultRegistry.Fold(sess.ID, permissions.FlowDelta{Audience: []string{"internal"}}, key, "h1")
	if !narrowed || before.Version != 1 {
		t.Fatalf("fold = %+v/%v, want internal v1 narrowed", before, narrowed)
	}
	DefaultRegistry.Sync(sess)
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save after fold: %v", err)
	}
	// Compaction: the summary inherits the session (the label travels
	// with the session file, not with the transcript).
	if err := svc.AppendSummary(sess.ID, "summary of the private doc"); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	// Resume: the persisted label is imported verbatim.
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Flow == nil {
		t.Fatal("resumed session has no flow label")
	}
	if loaded.Flow.Version != before.Version {
		t.Errorf("resumed version = %d, want %d (unchanged by compaction)", loaded.Flow.Version, before.Version)
	}
	if loaded.Flow.Audience != permissions.AudienceInternal || loaded.Flow.Trust != permissions.TrustTrusted {
		t.Errorf("resumed label = %+v, want trusted/internal", loaded.Flow)
	}
	// Re-admitting the same fold key after the resume is a no-op: the
	// applied set survived the round trip. The check runs against a
	// FRESH registry seeded from the loaded session - the old registry
	// still holds the in-memory applied set, so folding there would
	// pass even if the persisted set was lost.
	if !loaded.FlowApplied[key] {
		t.Fatalf("resumed session lost the applied key %q", key)
	}
	r := NewFlowRegistry()
	if recomputed := RegisterLoadedWith(r, loaded); recomputed {
		t.Fatal("resumed v2 session recomputed, want imported")
	}
	after, narrowed2 := r.Fold(sess.ID, permissions.FlowDelta{Audience: []string{"internal"}}, key, "h1")
	if narrowed2 || after.Version != before.Version {
		t.Errorf("re-admit after resume = %+v/%v, want no-op v%d", after, narrowed2, before.Version)
	}
}

// TestFlowRecomputedNoticeOnce pins the one-time resume notice: it rides
// the first denial and never repeats.
func TestFlowRecomputedNoticeOnce(t *testing.T) {
	r := NewFlowRegistry()
	old := NewSession("pre-v2")
	old.Flow = nil
	if recomputed := RegisterLoadedWith(r, old); !recomputed {
		t.Fatal("RegisterLoadedWith old = false, want true")
	}
	if !r.TakeFlowRecomputed(old.ID) {
		t.Error("TakeFlowRecomputed = false, want true")
	}
	if r.TakeFlowRecomputed(old.ID) {
		t.Error("second TakeFlowRecomputed = true, want false")
	}
	notice := r.TakeFlowNotice(old.ID)
	if !strings.Contains(notice, "pre-v2") {
		t.Errorf("notice = %q, want the pre-v2 text", notice)
	}
	if r.TakeFlowNotice(old.ID) != "" {
		t.Error("second TakeFlowNotice non-empty, want empty (one-time)")
	}
	// A session with a persisted label never recomputes.
	fresh := NewSession("v2")
	if recomputed := RegisterLoadedWith(r, fresh); recomputed {
		t.Error("RegisterLoadedWith fresh = true, want false")
	}
	if r.TakeFlowNotice(fresh.ID) != "" {
		t.Error("notice for a persisted label, want empty")
	}
}

// TestImportMeetVerbatimThenMerge pins the F1 atomic primitive: a fresh
// registry imports the persisted label verbatim (no version bump, so the
// H3 resume/compaction version-unchanged invariant holds), while a live
// entry merges with Meet (narrowing only, never wider) and unions the
// applied sets.
func TestImportMeetVerbatimThenMerge(t *testing.T) {
	r := NewFlowRegistry()
	persisted := permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal, Version: 3}
	diskKey := FlowKey(9, "disk_call")
	merged, fresh := r.ImportMeet("s", persisted, map[string]bool{diskKey: true})
	if !fresh {
		t.Error("fresh ImportMeet imported=false, want true")
	}
	if merged != persisted {
		t.Errorf("fresh ImportMeet = %+v, want verbatim %+v", merged, persisted)
	}
	if got := r.Get("s"); got == nil || *got != persisted {
		t.Fatalf("registry after fresh import = %+v, want %+v", got, persisted)
	}
	// Live fold, then a WIDER disk state merges in: the registry must
	// not widen, and the applied set must union both sides.
	liveKey := FlowKey(10, "live_call")
	if _, narrowed := r.Fold("s", permissions.FlowDelta{Audience: []string{"self"}}, liveKey, "h"); !narrowed {
		t.Fatal("live fold did not narrow")
	}
	before := *r.Get("s")
	wider := permissions.StartLabel()
	merged2, fresh2 := r.ImportMeet("s", wider, map[string]bool{FlowKey(11, "stale_call"): true})
	if fresh2 {
		t.Error("merge into live ImportMeet imported=true, want false")
	}
	if merged2 != before {
		t.Errorf("merge with wider disk = %+v, want live %+v (no widen)", merged2, before)
	}
	for _, k := range []string{diskKey, liveKey, FlowKey(11, "stale_call")} {
		if got := r.appliedCopy("s"); !got[k] {
			t.Errorf("applied set lost key %q after merge", k)
		}
	}
}

// TestRegisterLoadedVsFoldConcurrent pins F1 under -race: concurrent Loads
// (fresh Session per Load, the production shape) racing mid-turn folds
// merge atomically, so the final label keeps both narrowings and the
// applied set is the union. Deterministic end state: the persisted
// audience narrowing plus the folded trust narrowing, version bumped
// exactly once past the persisted version by the single effective merge.
func TestRegisterLoadedVsFoldConcurrent(t *testing.T) {
	r := NewFlowRegistry()
	const id = "load-race"
	persisted := permissions.SessionLabel{Trust: permissions.TrustTrusted, Audience: permissions.AudienceInternal, Version: 5}
	diskKey := FlowKey(2, "disk_call")
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				s := NewSession(id)
				s.ID = id
				l := persisted
				s.Flow = &l
				s.FlowApplied = map[string]bool{diskKey: true}
				RegisterLoadedWith(r, s)
				return
			}
			r.Fold(id, permissions.FlowDelta{Trust: "suspicious"}, FlowKey(1000+int64(i), "fold_call"), "h")
		}(i)
	}
	wg.Wait()
	got := r.Get(id)
	if got == nil {
		t.Fatal("registry entry missing after concurrent load/fold")
	}
	if got.Trust != permissions.TrustSuspicious || got.Audience != permissions.AudienceInternal {
		t.Errorf("merged label = %+v, want suspicious/internal (both narrowings kept)", got)
	}
	if got.Version != 6 {
		t.Errorf("merged version = %d, want 6 (persisted v5 + one narrowing step)", got.Version)
	}
	applied := r.appliedCopy(id)
	if !applied[diskKey] {
		t.Errorf("applied set lost disk key %q", diskKey)
	}
	for i := 1; i < n; i += 2 {
		if k := FlowKey(1000+int64(i), "fold_call"); !applied[k] {
			t.Errorf("applied set lost fold key %q", k)
		}
	}
}

// TestSyncUnionsApplied pins F2: Sync unions the registry live keys with
// whatever the loaded object already carries, so a Load-then-Save round
// trip after a racing turn preserves both sides' idempotency keys and
// never double-folds (H3).
func TestSyncUnionsApplied(t *testing.T) {
	r := NewFlowRegistry()
	r.Ensure("u")
	regKey := FlowKey(1, "reg_call")
	if _, narrowed := r.Fold("u", permissions.FlowDelta{Audience: []string{"internal"}}, regKey, "h"); !narrowed {
		t.Fatal("registry fold did not narrow")
	}
	s := NewSession("u")
	s.ID = "u"
	diskKey := FlowKey(2, "disk_call")
	s.Flow = nil
	s.FlowApplied = map[string]bool{diskKey: true}
	r.Sync(s)
	if s.Flow == nil || s.Flow.Audience != permissions.AudienceInternal {
		t.Fatalf("synced Flow = %+v, want live internal", s.Flow)
	}
	if !s.FlowApplied[regKey] || !s.FlowApplied[diskKey] {
		t.Errorf("synced FlowApplied = %v, want union of %q and %q", s.FlowApplied, regKey, diskKey)
	}
	// Unknown sessions stay untouched (nil Flow preserved, no keys).
	unknown := NewSession("unknown")
	unknown.ID = "unknown-" + t.Name()
	unknown.Flow = nil
	unknown.FlowApplied = nil
	r.Sync(unknown)
	if unknown.Flow != nil || unknown.FlowApplied != nil {
		t.Errorf("Sync touched unknown session: Flow=%+v Applied=%v", unknown.Flow, unknown.FlowApplied)
	}
	// Nil session is a no-op, never a panic.
	r.Sync(nil)
}

// TestStoreLoadPreservesLiveFold pins C1 at the store layer: a Load racing
// a turn (turn-end persistence, compaction reads) must not overwrite live
// registry folds with stale disk state, and the returned session must carry
// the live label so a Load-then-Save round trip preserves the narrowing.
func TestStoreLoadPreservesLiveFold(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	s := NewSession("live-fold")
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	orig := DefaultRegistry
	DefaultRegistry = NewFlowRegistry()
	t.Cleanup(func() { DefaultRegistry = orig })

	// Turn start: resume imports the persisted (start) label.
	if _, err := store.Load(s.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A source tool folds mid-turn.
	key := FlowKey(4, "call_live")
	if _, narrowed := DefaultRegistry.Fold(s.ID, permissions.FlowDelta{Trust: "suspicious"}, key, "h"); !narrowed {
		t.Fatal("fold did not narrow")
	}
	// Turn-end read path: Load must not clobber the live fold with the
	// stale on-disk (start) label...
	reloaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got := DefaultRegistry.Get(s.ID); got == nil || got.Trust != permissions.TrustSuspicious || got.Version != 1 {
		t.Fatalf("registry after Load = %+v, want live suspicious v1", got)
	}
	// ...and the loaded object carries the live label, so saving it back
	// (the persistAgentResponse shape) preserves the narrowing.
	if reloaded.Flow == nil || reloaded.Flow.Trust != permissions.TrustSuspicious {
		t.Fatalf("reloaded Flow = %+v, want live suspicious", reloaded.Flow)
	}
	if err := store.Save(reloaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Resume into a fresh registry: narrowing + applied set survive, and
	// re-admitting the fold key is a no-op (no double-fold, H3).
	raw, err := store.loadUnlocked(s.ID)
	if err != nil {
		t.Fatalf("loadUnlocked: %v", err)
	}
	if raw.Flow == nil || raw.Flow.Trust != permissions.TrustSuspicious {
		t.Fatalf("persisted Flow = %+v, want suspicious", raw.Flow)
	}
	if !raw.FlowApplied[key] {
		t.Fatalf("persisted FlowApplied lost key %q", key)
	}
	r2 := NewFlowRegistry()
	if recomputed := RegisterLoadedWith(r2, raw); recomputed {
		t.Fatal("resumed v2 session recomputed, want imported")
	}
	after, narrowed := r2.Fold(s.ID, permissions.FlowDelta{Trust: "suspicious"}, key, "h")
	if narrowed || after.Version != 1 {
		t.Errorf("re-admit after resume = %+v/%v, want no-op v1", after, narrowed)
	}
}
