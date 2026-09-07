package interfaces

import (
	"context"
	"sync"
)

// taskSessions maps ADK task ids (which double as the ADK session id) to the
// hakase session a turn serves. Tool execution deep under the ADK runner has
// no other path to the hakase session, and gate prompts (approval/clarify)
// need it so transports can route the prompt to the conversation that started
// the run. The shared driver (internal/agentrun) registers every turn it
// drives; gate raise sites resolve through SessionIDFromCtx. This lives in
// the leaf contract package because both the driver and the sandbox gate
// need it without an import cycle.
var taskSessions = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

// RegisterTaskSession associates an ADK task id with the hakase session of
// the turn driving it. Safe for concurrent turns.
func RegisterTaskSession(taskID, sessionID string) {
	taskSessions.Lock()
	taskSessions.m[taskID] = sessionID
	taskSessions.Unlock()
}

// UnregisterTask drops a finished turn's association.
func UnregisterTask(taskID string) {
	taskSessions.Lock()
	delete(taskSessions.m, taskID)
	taskSessions.Unlock()
}

// SessionIDFromCtx resolves the hakase session for gate prompts from an ADK
// invocation context (tools receive one as ctx). Returns "" outside a
// registered run (TUI, CLI utilities), which keeps those gates
// transport-agnostic (payload session_id empty → fan-out as before).
// Best-effort by contract: any context whose session cannot be probed yields "".
func SessionIDFromCtx(ctx context.Context) (id string) {
	if ctx == nil {
		return ""
	}
	defer func() { _ = recover() }() // half-initialized test mocks must not kill a tool run
	ic, ok := ctx.(interface{ SessionID() string })
	if !ok {
		return ""
	}
	taskSessions.RLock()
	id = taskSessions.m[ic.SessionID()]
	taskSessions.RUnlock()
	return id
}
