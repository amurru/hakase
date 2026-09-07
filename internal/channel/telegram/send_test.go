package telegram

import (
	"context"
	"testing"
	"time"
)

// TestWaitTurnEventuallyReturns guards the send pacing loop against the
// receding-slot livelock found in the field (2026-09-06): a caller that found
// the chat's slot in the future used to push the slot another full interval
// forward on EVERY wake-up, then sleep only the remaining delta — so it (and
// every later sender for that chat) chased the slot forever, silently: no
// Done edit, no reply chunks, no /help answer, and zero log output. The fix
// reserves the slot exactly once per call and sleeps to it.
func TestWaitTurnEventuallyReturns(t *testing.T) {
	old := perChatSendInterval
	perChatSendInterval = 100 * time.Millisecond
	t.Cleanup(func() { perChatSendInterval = old })

	b := &Bot{nextSend: map[conv]time.Time{}}

	// One caller takes the slot instantly; the others arrive while it is in
	// the future — the shape that used to trigger the livelock.
	const callers = 3
	done := make(chan struct{}, callers)
	for i := 0; i < callers; i++ {
		go func() {
			b.waitTurn(context.Background(), rootConv(1))
			done <- struct{}{}
		}()
	}
	// Healthy pacing: 3 callers clear within ~callers*interval.
	for i := 0; i < callers; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("waitTurn did not return within 5s — send limiter livelock")
		}
	}
}
