package interfaces

import (
	"context"
	"testing"
)

type fakeInvocation struct {
	context.Context
	sessionID string
}

func (f fakeInvocation) SessionID() string { return f.sessionID }

// plainCtx is a bare context (no SessionID method): TUI/CLI-style contexts.
type plainCtx struct{ context.Context }

func TestRegisterTaskSessionRoundtrip(t *testing.T) {
	RegisterTaskSession("task_1", "sess_run")
	defer UnregisterTask("task_1")

	if got := SessionIDFromCtx(fakeInvocation{sessionID: "task_1"}); got != "sess_run" {
		t.Errorf("SessionIDFromCtx = %q, want sess_run", got)
	}

	UnregisterTask("task_1")
	if got := SessionIDFromCtx(fakeInvocation{sessionID: "task_1"}); got != "" {
		t.Errorf("after unregister = %q, want empty", got)
	}
}

func TestSessionIDFromCtxUnknownContexts(t *testing.T) {
	if got := SessionIDFromCtx(fakeInvocation{sessionID: "never_registered"}); got != "" {
		t.Errorf("unregistered task = %q, want empty", got)
	}
	if got := SessionIDFromCtx(plainCtx{}); got != "" {
		t.Errorf("non-ADK context = %q, want empty", got)
	}
	if got := SessionIDFromCtx(nil); got != "" {
		t.Errorf("nil context = %q, want empty", got)
	}
}
