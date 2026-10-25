// flow_persist_test.go - Flow Guard fix-up loop 1/2 (C1, epic #124).
//
// Turn-end persistence (Driver.persistAgentResponse) must not clobber live
// registry folds: the narrowed label survives the turn-end Load/Save cycle
// on disk and in the registry.
package agentrun

import (
	"testing"

	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// TestPersistAgentResponsePreservesFlowLabel pins C1 at the turn boundary:
// a mid-turn fold is still the persisted and live label after the agent
// reply is saved, so the next resume keeps the restriction.
func TestPersistAgentResponsePreservesFlowLabel(t *testing.T) {
	dir := t.TempDir()
	store, err := hakasesession.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })

	sess, err := svc.CreateSession("turn")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Turn start: resume imports the persisted (start) label.
	if _, err := store.Load(sess.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A source tool folds mid-turn.
	key := hakasesession.FlowKey(7, "call_live")
	if _, narrowed := hakasesession.DefaultRegistry.Fold(
		sess.ID, permissions.FlowDelta{Trust: "suspicious"}, key, "h"); !narrowed {
		t.Fatal("fold did not narrow")
	}

	// Turn end: the driver persists the reply through the real path.
	d := &Driver{Sessions: svc}
	d.persistAgentResponse(sess.ID, "done", "", nil, nil)

	// The live registry still holds the narrowing...
	if got := hakasesession.DefaultRegistry.Get(sess.ID); got == nil ||
		got.Trust != permissions.TrustSuspicious || got.Version != 1 {
		t.Fatalf("registry after persist = %+v, want live suspicious v1", got)
	}
	// ...and the disk carries it with the applied key, so a resume keeps
	// the restriction with no double-fold.
	raw, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("resume Load: %v", err)
	}
	if raw.Flow == nil || raw.Flow.Trust != permissions.TrustSuspicious || raw.Flow.Version != 1 {
		t.Fatalf("persisted Flow = %+v, want suspicious v1", raw.Flow)
	}
	if !raw.FlowApplied[key] {
		t.Fatalf("persisted FlowApplied lost key %q", key)
	}
	r2 := hakasesession.NewFlowRegistry()
	if recomputed := hakasesession.RegisterLoadedWith(r2, raw); recomputed {
		t.Fatal("resumed v2 session recomputed, want imported")
	}
	if after, narrowed := r2.Fold(sess.ID, permissions.FlowDelta{Trust: "suspicious"}, key, "h"); narrowed || after.Version != 1 {
		t.Errorf("re-admit after resume = %+v/%v, want no-op v1", after, narrowed)
	}
	// The reply itself was still persisted.
	if len(raw.Messages) == 0 || raw.Messages[len(raw.Messages)-1].Content != "done" {
		t.Errorf("reply not persisted: %+v", raw.Messages)
	}
}
