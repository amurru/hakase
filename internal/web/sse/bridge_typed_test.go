package sse

import (
	"encoding/json"
	"testing"
	"time"
)

// TestTypedSubscription mirrors the raw-frame stream: every Send* producer
// must fan out to both subscriber kinds with consistent payloads.
func TestTypedSubscription(t *testing.T) {
	b := NewEventBridge()

	gSub, gEvents := b.SubscribeEvents("")
	defer b.UnsubscribeEvents("", gSub)
	gRaw, gRawCh := b.Subscribe("")
	defer b.Unsubscribe("", gRaw)
	_ = gRaw

	sSub, sEvents := b.SubscribeEvents("sess1")
	defer b.UnsubscribeEvents("sess1", sSub)
	sRaw, sRawCh := b.Subscribe("sess1")
	defer b.Unsubscribe("sess1", sRaw)
	_ = sRaw

	b.SendApprovalPrompt("", "", "appr_1", "system_exec", "high", "because", "rm -rf x")
	b.SendLog("sess1", "Call: read_file(path)")
	b.SendDone("sess1")

	expect := []struct {
		name   string
		events <-chan Event
		raw    <-chan []byte
		keys   []string
	}{
		{"approval", gEvents, gRawCh, []string{"id", "tool", "risk", "reason", "command"}},
		{"log", sEvents, sRawCh, []string{"line"}},
		{"done", sEvents, sRawCh, nil},
	}

	for _, want := range expect {
		select {
		case ev, ok := <-want.events:
			if !ok {
				t.Fatalf("typed channel closed early")
			}
			if ev.Name != want.name {
				t.Fatalf("event = %q, want %q", ev.Name, want.name)
			}
			var m map[string]any
			if err := json.Unmarshal(ev.Data, &m); err != nil {
				t.Fatalf("%s: payload not JSON: %v", want.name, err)
			}
			for _, k := range want.keys {
				if _, ok := m[k]; !ok {
					t.Errorf("%s: missing key %q in %v", want.name, k, m)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", want.name)
		}

		select {
		case frame := <-want.raw:
			if len(frame) == 0 || frame[0] != 'e' {
				t.Fatalf("raw frame not SSE-formatted: %q", frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s raw frame", want.name)
		}
	}
}

// TestSlowTypedConsumerDropped ensures a stuck consumer cannot wedge
// publishes (the bridge's non-blocking contract).
func TestSlowTypedConsumerDropped(t *testing.T) {
	b := NewEventBridge()
	_, events := b.SubscribeEvents("") // nobody drains
	for i := 0; i < 300; i++ {
		b.SendLog("s", "line") // channel cap is 256; extras must drop silently
	}
	done := make(chan struct{})
	go func() {
		b.SendLog("s", "final")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow typed consumer")
	}
	b.UnsubscribeEvents("", 1) // just verifying idempotence is safe
	_ = events
}
