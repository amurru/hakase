package channel

import (
	"context"
	"testing"
)

func TestRunManagerLifecycle(t *testing.T) {
	m := NewRunManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !m.TryStart("telegram:1", "sess_a", cancel) {
		t.Fatal("first start rejected")
	}
	if m.TryStart("telegram:1", "sess_b", cancel) {
		t.Fatal("second concurrent start on the same chat accepted")
	}
	if !m.TryStart("telegram:2", "sess_c", cancel) {
		t.Fatal("other chat start rejected")
	}

	run, ok := m.Running("telegram:1")
	if !ok || run.SessionID != "sess_a" {
		t.Fatalf("Running = %+v ok=%v", run, ok)
	}

	keys := m.ActiveChatKeys()
	if len(keys) != 2 {
		t.Fatalf("ActiveChatKeys = %v, want 2", keys)
	}

	if !m.Cancel("telegram:1") {
		t.Fatal("Cancel reported nothing to cancel")
	}
	if ctx.Err() == nil {
		t.Fatal("Cancel did not invoke the cancel func")
	}
	// The run goroutine would call Finish; simulate it.
	m.Finish("telegram:1")
	if _, ok := m.Running("telegram:1"); ok {
		t.Fatal("run still active after Finish")
	}
	if m.Cancel("telegram:1") {
		t.Fatal("Cancel after Finish must report false")
	}
}

func TestRunManagerFinishIdempotent(t *testing.T) {
	m := NewRunManager()
	m.Finish("missing") // must not panic
	_, ok := m.Running("missing")
	if ok {
		t.Fatal("unknown chat reported running")
	}
}
