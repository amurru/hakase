// flow_highs_test.go - S8 push fan-out per-recipient gating (highs
// fix-up lane, epic #124).
package discord

import (
	"strings"
	"testing"

	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// installFlowV2 compiles and installs a v2 policy for the test.
func installFlowV2(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

// bindUserSession binds the user's DM chat to a fresh session.
func bindUserSession(t *testing.T, b *Bot, userID int64) string {
	t.Helper()
	svc := b.sessions
	sess, err := svc.CreateSession("flow test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[state.ChatKey(ChannelName, userID)] = state.Chat{SessionID: sess.ID, Notify: true}
		return nil
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return sess.ID
}

// S8: push fan-out gates per recipient (narrowed users skip, the rest
// still deliver). Read-only: no fold, never sticky.
func TestPushFanoutPerRecipientGating(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100, 101})
	installFlowV2(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	narrowed := bindUserSession(t, b, 100)
	open := bindUserSession(t, b, 101)
	hakasesession.DefaultRegistry.Fold(narrowed,
		permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	hakasesession.DefaultRegistry.Ensure(open)

	b.TaskEvent("", "completed", "t1", "job", "done")
	b.CronEvent("", "completed", "j1", "nightly", "all good", "")
	b.DelegationEvent("", "completed", "task-1", "agent", "result")

	sender.mu.Lock()
	defer sender.mu.Unlock()
	var to100, to101 int
	for _, s := range sender.sends {
		ch, text, _ := strings.Cut(s, "\x00")
		if !strings.Contains(text, "Task completed") &&
			!strings.Contains(text, "Cron") &&
			!strings.Contains(text, "Delegation") {
			continue
		}
		switch ch {
		case "dm-100":
			to100++
		case "dm-101":
			to101++
		}
		if ch == "dm-100" {
			t.Errorf("narrowed user 100 received a push: %q", text)
		}
	}
	if to100 != 0 {
		t.Errorf("narrowed user 100 received %d pushes, want 0 (withheld)", to100)
	}
	if to101 != 2 {
		// Task + cron deliver; delegation needs an active run (none here).
		t.Errorf("open user 101 received %d pushes, want 2 (task+cron)", to101)
	}
	// Read-only: the push evaluated but never folded the labels.
	if l := hakasesession.DefaultRegistry.Get(narrowed); l == nil || l.Audience != permissions.AudienceInternal {
		t.Errorf("push must not fold the narrowed label: %+v", l)
	}
}

// S8: without a v2 policy every push delivers (v1 parity).
func TestPushFanoutV1Parity(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	permissions.Install(nil)
	bindUserSession(t, b, 100)

	b.TaskEvent("", "completed", "t1", "job", "done")
	if n := sender.sendCount(); n != 1 {
		t.Errorf("v1 task pushes = %d, want 1 (no gating without v2)", n)
	}
}

// S8/H1: a narrowed producer withholds the push on every recipient, even
// when all recipient users are open. The payload's taint rides the
// producer session, not the fan-out list.
func TestPushFanoutProducerNarrowedWithholds(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100, 101})
	installFlowV2(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}})
	bindUserSession(t, b, 100)
	bindUserSession(t, b, 101)

	sess, err := b.sessions.CreateSession("flow producer")
	if err != nil {
		t.Fatalf("create producer session: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(sess.ID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "prod-k", "h")

	b.TaskEvent(sess.ID, "completed", "t1", "job", "done")
	b.CronEvent(sess.ID, "completed", "j1", "nightly", "all good", "")

	sender.mu.Lock()
	defer sender.mu.Unlock()
	for _, s := range sender.sends {
		_, text, _ := strings.Cut(s, "\x00")
		if strings.Contains(text, "Task completed") || strings.Contains(text, "Cron") {
			t.Errorf("narrowed producer push delivered: %q", text)
		}
	}
}
