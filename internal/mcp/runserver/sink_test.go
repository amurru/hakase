// sink_test.go - progress-notification binding for the `run` tool.
//
// MCP only accepts a progress notification while the request carrying its
// token is in flight. A run that hits a gate spans several calls (each round
// trip returns an input-required result and the client re-enters with a fresh
// token), so progress has to follow the call being served rather than the one
// that started the run. These tests pin that binding.
package runserver

import (
	"context"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeNotifier records the tokens it was asked to notify.
type fakeNotifier struct {
	mu     sync.Mutex
	tokens []any
}

func (f *fakeNotifier) NotifyProgress(_ context.Context, p *mcp.ProgressNotificationParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, p.ProgressToken)
	return nil
}

func (f *fakeNotifier) seen() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.tokens))
	copy(out, f.tokens)
	return out
}

func newTestSink(n *fakeNotifier, token any) (*runSink, uint64) {
	s := &runSink{}
	return s, s.bindToken(context.Background(), n, token)
}

// An unbound sink emits nothing: the run outlives the call that started it,
// and a notification naming a completed request is protocol-invalid.
func TestRunSink_UnboundEmitsNothing(t *testing.T) {
	t.Parallel()

	n := &fakeNotifier{}
	s, gen := newTestSink(n, "tok-1")
	s.unbind(gen)

	s.OnStream("sess", "hello", "")
	s.OnLog("sess", "some activity")

	if got := n.seen(); len(got) != 0 {
		t.Fatalf("unbound sink emitted %d notifications, want 0: %v", len(got), got)
	}
	// The text is still accumulated: an unbound window must not lose output.
	if s.answer() != "hello" {
		t.Fatalf("answer = %q, want the delta to still be buffered", s.answer())
	}
	if len(s.activity()) != 1 {
		t.Fatalf("activity = %v, want the line buffered while unbound", s.activity())
	}
}

// A bound sink emits with the token of the call being served.
func TestRunSink_BoundEmitsWithToken(t *testing.T) {
	t.Parallel()

	n := &fakeNotifier{}
	s, _ := newTestSink(n, "tok-1")

	s.OnStream("sess", "hello", "")

	got := n.seen()
	if len(got) != 1 || got[0] != "tok-1" {
		t.Fatalf("tokens = %v, want [tok-1]", got)
	}
}

// The gate round trip: the call that started the run completes, the client
// re-enters with a new token, and later deltas must carry the new one rather
// than the stale token of the completed call.
func TestRunSink_RebindsToNewTokenAfterGateRoundTrip(t *testing.T) {
	t.Parallel()

	n := &fakeNotifier{}
	s, gen := newTestSink(n, "tok-1")

	s.OnStream("sess", "before", "")
	// The handler returns an input-required result: the first call is done.
	s.unbind(gen)
	s.OnStream("sess", "while-suspended", "")
	// The client re-enters with a fresh token.
	s.bindToken(context.Background(), n, "tok-2")
	s.OnStream("sess", "after", "")

	got := n.seen()
	want := []any{"tok-1", "tok-2"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokens = %v, want %v", got, want)
		}
	}
}

// A request with no progress token (clients are not required to send one)
// must stay silent rather than emit an empty token.
func TestRunSink_NoTokenEmitsNothing(t *testing.T) {
	t.Parallel()

	n := &fakeNotifier{}
	s, _ := newTestSink(n, nil)

	s.OnStream("sess", "hello", "")

	if got := n.seen(); len(got) != 0 {
		t.Fatalf("emitted %d notifications without a progress token: %v", len(got), got)
	}
}

// Two calls can briefly overlap on one run (a client retrying the same
// request state before the earlier call returned). The older call's deferred
// teardown must not silence the newer call's binding, or the live call would
// silently stop receiving progress.
func TestRunSink_StaleUnbindDoesNotClobberNewerBinding(t *testing.T) {
	t.Parallel()

	n := &fakeNotifier{}
	s, firstGen := newTestSink(n, "tok-1")

	// The newer call arrives and takes over the binding.
	newGen := s.bindToken(context.Background(), n, "tok-2")
	// The older call now returns and runs its deferred unbind.
	s.unbind(firstGen)

	s.OnStream("sess", "still-live", "")

	got := n.seen()
	if len(got) != 1 || got[0] != "tok-2" {
		t.Fatalf("tokens = %v, want [tok-2] (a stale unbind must not silence the live call)", got)
	}

	// The newer call's own teardown still works.
	s.unbind(newGen)
	s.OnStream("sess", "after-teardown", "")
	if got := n.seen(); len(got) != 1 {
		t.Fatalf("tokens = %v, want the current binding released", got)
	}
}
