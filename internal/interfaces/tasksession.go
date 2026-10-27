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

// SessionForTask returns the hakase session registered for an ADK task
// id, or "" when unknown. The channel fan-out uses it to resolve the
// producer session of lifecycle pushes (delegation progress is reported
// under the delegation task id, which delegate.go registers against the
// parent run's session). Best-effort: "" keeps the recipient-gated
// fallback (v1 parity).
func SessionForTask(taskID string) string {
	if taskID == "" {
		return ""
	}
	taskSessions.RLock()
	defer taskSessions.RUnlock()
	return taskSessions.m[taskID]
}

// SessionIDFromCtx resolves the hakase session for gate prompts from an ADK
// invocation context (tools receive one as ctx). Returns "" outside a
// registered run (TUI, CLI utilities), which keeps those gates
// transport-agnostic (payload session_id empty → fan-out as before).
// Best-effort by contract: any context whose session cannot be probed yields "".
// The read lock is deferred so a panicking SessionID method cannot leak
// it and wedge every later writer (register/unregister/resume).
func SessionIDFromCtx(ctx context.Context) (id string) {
	if ctx == nil {
		return ""
	}
	defer func() { _ = recover() }() // half-initialized test mocks must not kill a tool run
	if ic, ok := ctx.(interface{ SessionID() string }); ok {
		id = probeRegisteredSession(ic)
		if id != "" {
			return id
		}
	}
	// Inside a delegated sub-agent the child's ADK session id is generated
	// by agenttool and never registered; the planted parent route keeps
	// gate prompts routed to the conversation that started the run.
	if sid, _, ok := delegationRouteFromCtx(ctx); ok {
		return sid
	}
	return ""
}

// probeRegisteredSession resolves the hakase session for a probed ADK
// invocation. The read lock and the recover both defer inside here so a
// panicking SessionID method releases the lock before the outer recover
// swallows the panic - a leaked read lock wedges every later
// register/unregister/resume.
func probeRegisteredSession(ic interface{ SessionID() string }) (id string) {
	taskSessions.RLock()
	defer taskSessions.RUnlock()
	defer func() { _ = recover() }()
	id = taskSessions.m[ic.SessionID()]
	return id
}

// delegationRouteKey carries parent-run routing for tools executing inside
// a delegated sub-agent. The agenttool runner (AD-001) generates the child's
// ADK session id itself, so the taskSessions registry - keyed by the
// delegation task id - is not probeable from child contexts. The delegate
// handler plants the parent session and delegation task id here; the
// FromCtx resolvers fall back to these values when the interface probe
// misses.
type delegationRouteKey struct{}

// WithDelegationRoute plants parent-run routing on a delegated child run's
// context. parentSession is the hakase session gate prompts must route to;
// taskID is the delegation task id that doubles as the canvas node id.
func WithDelegationRoute(ctx context.Context, parentSession, taskID string) context.Context {
	return context.WithValue(ctx, delegationRouteKey{}, [2]string{parentSession, taskID})
}

// delegationRouteFromCtx returns the planted route, if any.
func delegationRouteFromCtx(ctx context.Context) (sessionID, taskID string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	if r, found := ctx.Value(delegationRouteKey{}).([2]string); found {
		return r[0], r[1], true
	}
	return "", "", false
}

// TaskIDFromCtx resolves the ADK task id of the invocation a tool is running
// in from an ADK invocation context. Task ids double as ADK session ids, and
// the canvas uses them as agent node ids, so a delegating tool handler (e.g.
// delegate_task) can link its sub-agent node to the parent run's node without
// a reverse registry. Inside a delegated sub-agent the planted route wins
// (the agenttool child session id is opaque); elsewhere the invocation
// context is probed. Returns "" outside a probed invocation context; the
// same recover guard as SessionIDFromCtx applies.
func TaskIDFromCtx(ctx context.Context) (id string) {
	if ctx == nil {
		return ""
	}
	defer func() { _ = recover() }()
	if _, tid, ok := delegationRouteFromCtx(ctx); ok {
		return tid
	}
	ic, ok := ctx.(interface{ SessionID() string })
	if !ok {
		return ""
	}
	return ic.SessionID()
}
