# Plan: Durable human-in-the-loop resume (option B)

Issue [#74](https://github.com/amurru/hakase/issues/74) — Tier-2 item
from #20. Research: `docs/durable-resume/research.md` (RQ1-RQ4).
Decision: **classic Runner + durable pauses**, no graph migration.

## Goal

When the hakase process restarts (crash, deploy, /restart) while a run
is blocked on a gate (approval or clarify), the user can answer the
pending prompt after restart and the run resumes from where it paused
— without re-running completed tool calls or losing conversation context.

## Deep research findings

### The classic Runner already has resume support

This is the most important finding. The ADK v2.4.0 classic Runner
(`runner.Runner`) **already implements resume** via the node runtime:

- `run_node.go:179-194`: On every `Run` call, the runner calls
  `wf.ReconstructRunState(storedSession, ictx.InvocationID())` to
  rebuild paused state from session history. If the incoming message
  carries `FunctionResponse`s that answer open interrupts, it calls
  `wf.Resume(ictx, state, responses)` instead of `wf.Run(ictx)`.

- `run_node.go:326-358` (`buildResumeResponses`): Maps the incoming
  message's `FunctionResponse`s to `interruptID -> payload`, keeping
  only those that answer a still-pending interrupt. Pending = waiting
  interrupts in `RunState` PLUS any unanswered long-running call open
  in history (`openLongRunningCallIDs`).

- `run_node.go:360-385` (`openLongRunningCallIDs`): Scans session
  history for `LongRunningToolIDs` that have no matching
  `FunctionResponse` — these are the interrupts still awaiting a
  human answer.

**Implication**: The resume mechanism is **already in the classic
Runner path**. We do NOT need to build a resume driver from scratch.
We need to make the pieces work together:

1. **Durable session storage** so history survives restart
2. **Gate-as-pause** so the gate's tool call carries
   `LongRunningToolIDs` and the answer is a `FunctionResponse`
3. **Stable session identity** so the resumed turn finds the paused
   turn's history

### ADK session.Service interface (v2.4.0)

The interface is request/response-based, not the simple 5-method
pattern:

```go
type Service interface {
    Create(context.Context, *CreateRequest) (*CreateResponse, error)
    Get(context.Context, *GetRequest) (*GetResponse, error)
    List(context.Context, *ListRequest) (*ListResponse, error)
    Delete(context.Context, *DeleteRequest) error
    AppendEvent(context.Context, Session, *Event) error
}
```

Key types:
- `CreateRequest{AppName, UserID, SessionID, State}`
- `GetRequest{AppName, UserID, SessionID, NumRecentEvents, After}`
- `DeleteRequest{AppName, UserID, SessionID}`
- `Event` embeds `model.LLMResponse` and adds `ID`, `Timestamp`,
  `InvocationID`, `Branch`, `Author`, `Actions`, `LongRunningToolIDs`,
  `Routes`, `RequestedInput`, `Output`, `NodeInfo`

The `AppendEvent` contract requires:
- Events with no ID must end up with one (assigned in-place or on read)
- `EventActions.Compaction` must survive the round trip

### Gate blocking is ctx-blind in BOTH web and TUI

**Web gates** (`internal/web/handlers/approval.go:76-90`):
```go
select {
case approved := <-resp:
    return approved, nil
case <-time.After(expiry):
    return false, nil
}
```
No `ctx.Done()` — a `/stop` (which cancels the run context) cannot
unblock the gate.

**TUI gates** (`internal/tui/gates.go:29-47, 75-101`):
The TUI's `AskApproval`/`AskClarify` send a tea message to the program
and block on `waitForApproval`/`waitForClarify`
(`internal/tui/ui.go:2843-2850`, `internal/tui/clarify_ui.go:59-66`):
```go
func waitForApproval(resp chan bool, expiry time.Duration) bool {
    select {
    case ok := <-resp:
        return ok
    case <-time.After(expiry):
        return false
    }
}
```
Same pattern — no `ctx.Done()`.

**TUI run loop** (`internal/tui/ui.go:2228-2352`): The TUI has its
own `runAgentTask` that bypasses `agentrun.Driver` entirely. It
creates a `runCtx` with cancel (`m.runCtrl.SetCancel(runCancel)`),
but the gate functions never receive this context. The cancellation
only affects the `m.r.Run()` iterator, not the gate goroutine.

### Session identity

The ADK session key is currently the per-turn `taskID` (a ULID
generated per run). For durable resume, the ADK session key must be
stable across turns within the same hakase session, so that
`ReconstructRunState` can find the paused turn's history.

The mapping is: `taskID` (per-turn) -> `sessionID` (per-conversation).
The durable service stores events under the hakase session ID, and
the runner's `getOrCreateSession` uses the hakase session ID as the
ADK session key.

### The LongRunningToolIDs mechanism

The ADK engine uses `Event.LongRunningToolIDs` to mark tool calls
that are HITL pauses. When a tool call carries this marker:
1. The engine yields the event to the caller
2. The engine waits for a `FunctionResponse` with the matching ID
3. On resume, `buildResumeResponses` matches the response to the
   open interrupt

The gate tool's `FunctionCall` ID becomes the `LongRunningToolID`.
When the user answers, the resume driver injects a synthetic
`FunctionResponse` with that ID and the answer as the response payload.

## Architecture

```
                  ┌─────────────────────────────────────┐
                  │  agentrun.Driver.RunTurn            │
                  │  (classic Runner, unchanged API)    │
                  └──────────────┬──────────────────────┘
                                 │
                  ┌──────────────▼──────────────────────┐
                  │  ADK Runner (classic)               │
                  │  - LLM calls via model.LLM          │
                  │  - Tool calls via tool.Tool         │
                  │  - Session: durable session.Service │
                  │  - Resume: wf.Resume (built-in)     │
                  └──────────────┬──────────────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              │                  │                  │
    ┌─────────▼─────────┐ ┌─────▼──────┐ ┌────────▼────────┐
    │ Gate tool         │ │ Normal     │ │ Delegate tool   │
    │ (approval/clarify)│ │ tool       │ │ (unchanged)     │
    │ → LongRunningTool │ │ → executes │ │ → sub-agent     │
    │   marker + pause  │ │   normally │ │   session       │
    └───────────────────┘ └────────────┘ └─────────────────┘
```

## Phases

### Phase 1: ctx-aware gates (prerequisite)

**Problem**: `AskApproval` selects only on its channel and the expiry
timer. A `/stop` command (which cancels the run context) cannot unblock
a gated run — the goroutine stays blocked until the timer fires.

**Change**: Add `ctx.Done()` to the select in both gate implementations.

The `ctx` comes from the tool handler's `agent.Context` — already
available in the tool handler signature. The gate functions need to
accept and thread the context through.

**Web gates** (`internal/web/handlers/approval.go`):
```go
func (g *WebApprovalGate) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
    // ... existing setup ...
    select {
    case approved := <-resp:
        return approved, nil
    case <-req.Context.Done():  // NEW: ctx-aware
        return false, req.Context.Err()
    case <-time.After(expiry):
        return false, nil
    }
}
```

The `ApprovalRequest` struct needs a `Context` field (or the gate
extracts it from the tool handler's context).

**TUI gates** (`internal/tui/gates.go`):
The TUI's `AskApproval`/`AskClarify` need to accept a `context.Context`
and thread it through to `waitForApproval`/`waitForClarify`:
```go
func (m *AppModel) AskApproval(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) {
    // ... existing setup ...
    return waitForApproval(ctx, resp, expiry), nil
}

func waitForApproval(ctx context.Context, resp chan bool, expiry time.Duration) bool {
    select {
    case ok := <-resp:
        return ok
    case <-ctx.Done():
        return false
    case <-time.After(expiry):
        return false
    }
}
```

**Interface change**: The `ApprovalGate` and `ClarifyGate` interfaces
need to accept `context.Context` as the first parameter. This is a
breaking change to the interface, but it is necessary for ctx-aware
gates.

**Files**: `internal/interfaces/interfaces.go` (interface change),
`internal/web/handlers/approval.go`,
`internal/web/handlers/clarify.go`,
`internal/tui/gates.go`,
`internal/tui/ui.go` (waitForApproval),
`internal/tui/clarify_ui.go` (waitForClarify),
`internal/agent/approval.go` (pass ctx through),
`internal/agent/clarify.go` (pass ctx through).

**Test**: A `/stop` during a pending approval unblocks the gate within
one poll interval (not after the full expiry).

### Phase 2: Durable ADK session.Service

**Problem**: `session.InMemoryService()` evaporates on restart. The
classic Runner's resume path (`run_node.go:179-194`) reconstructs
state from session history — but there is no history to reconstruct
from.

**Change**: Implement `session.Service` over the existing JSON session
store. The durable service wraps the hakase `SessionStore` and stores
ADK events as JSON.

The ADK `session.Service` interface (v2.4.0):
```go
type Service interface {
    Create(context.Context, *CreateRequest) (*CreateResponse, error)
    Get(context.Context, *GetRequest) (*GetResponse, error)
    List(context.Context, *ListRequest) (*ListResponse, error)
    Delete(context.Context, *DeleteRequest) error
    AppendEvent(context.Context, Session, *Event) error
}
```

The durable service:
- Maps ADK session keys to hakase session IDs
- Stores events as JSON with full fidelity (including `RequestedInput`,
  `NodeInfo`, `LongRunningToolIDs`, `InvocationID`)
- Assigns event IDs on `AppendEvent` when missing (in-place)
- Preserves `EventActions.Compaction` across the round trip

**Key design decision**: The durable service is **optional**. When
`config.durable_resume.enabled = false` (default), the system uses
`session.InMemoryService()` as today. When enabled, it uses the
durable service. This keeps the feature opt-in and avoids changing
behavior for users who don't need it.

**Files**: `internal/session/adk_service.go` (new),
`internal/agent/agent.go` (wire the durable service when enabled).

**Test**: Create a session, append events, simulate restart (new
service instance), verify events are recovered with all fields
intact.

### Phase 3: Stable ADK session identity

**Problem**: Hakase mints a fresh ADK session per turn
(`RegisterTaskSession(taskID, sessionID)` where `taskID` is generated
per run). Resume requires reusing the paused turn's ADK
session/invocation.

**Change**: When durable resume is enabled, the ADK session key
becomes the **hakase session ID** (stable across turns) instead of
the per-turn task ID. The task ID still doubles as the ADK session ID
for gate prompt routing, but the durable service maps it to the
hakase session for persistence.

This is a configuration-gated change: when durable resume is off,
behavior is unchanged (fresh ADK session per turn).

**Files**: `internal/agentrun/agentrun.go` (session key strategy),
`internal/agent/delegate.go` (sub-agent session key strategy).

**Test**: With durable resume on, two turns in the same hakase session
share the same ADK session key.

### Phase 4: Gate-as-pause rewiring

**Problem**: Gates block inside tool execution. The ADK runner sees a
tool call that never returns (until the gate resolves). For resume to
work, the pause must be visible in ADK session history as a
long-running tool or `RequestInput` event.

**Change**: When a gate is pending, emit a `LongRunningTool` marker
in the ADK event stream. The ADK runner already supports
`LongRunningToolIDs` — when a tool call carries this marker, the
runner yields the event to the caller and waits for a
`FunctionResponse` with the matching ID.

The gate tool's `FunctionCall` ID becomes the `LongRunningToolID`.
When the user answers, the resume driver injects a synthetic
`FunctionResponse` with that ID and the answer as the response payload.

**Implementation**: The gate tool handler, when it detects that the
run is in "durable pause" mode, returns a special response that the
ADK runner interprets as a long-running tool. This requires either:
- (a) A custom `tool.Tool` wrapper that emits the right event shape, or
- (b) Using the ADK's `RequestInput` mechanism (if the classic
  runner supports it for tool calls)

**Files**: `internal/agent/gate.go` (emit long-running marker),
`internal/agent/approval.go`, `internal/agent/clarify.go`.

**Test**: A pending approval appears in ADK session history as a
long-running tool call with the approval ID.

### Phase 5: Resume driver

**Problem**: After restart, the system must detect that a run was
paused on a gate and allow the user to answer it.

**Change**: A resume driver that:
1. Scans hakase sessions for runs that were interrupted mid-gate
2. Reconstructs the ADK session state from durable storage
3. Re-emits the gate prompt to the transport (SSE, Telegram, etc.)
4. Waits for the user's answer
5. Injects the answer as a `FunctionResponse` against the paused
   ADK session/invocation
6. Re-enters the classic Runner with the resumed state

The resume driver is invoked at startup (for crash recovery) and
when a user answers a stale prompt (for graceful restart).

**Files**: `internal/agent/resume.go` (new),
`internal/web/handlers/approval.go` (hook resume on respond),
`internal/web/handlers/clarify.go` (hook resume on respond).

**Test**: Simulate a restart during a pending approval, answer the
prompt after restart, verify the run resumes without re-executing
prior tool calls.

### Phase 6: Idempotency audit

**Problem**: RQ2 from the research found that resume replays any tool
calls that completed before the pause but whose results were not
persisted. Without idempotency keys, a resumed run may re-execute
side-effecting tools (e.g., `write_file`, `system_exec`).

**Change**: Audit all ~60 tools and classify them as:
- **Safe to retry**: read-only tools (`read_file`, `search_files`,
  `list_skills`, `get_task`, etc.) — no changes needed
- **Idempotent**: tools that produce the same result on retry
  (`write_file` with same content, `patch` with same old/new) —
  mark with idempotency keys
- **Must never replay**: tools with side effects (`system_exec`,
  `delegate_task`, `cronjob`, `download_file`) — these must be
  skipped on resume

The resume driver uses this classification to decide which
completed tool calls to replay vs. skip.

**Files**: `internal/agent/toolcall.go` (add idempotency metadata),
`internal/agent/resume.go` (skip must-never-replay tools).

**Test**: A resumed run does not re-execute `system_exec` or
`delegate_task` calls that completed before the pause.

### Phase 7: Transport run-view resurrection

**Problem**: After restart, the web UI shows a frozen "Working..."
status for runs that were in-flight. The user cannot tell if the
run is still active or died with the process.

**Change**: On startup, the resume driver scans for interrupted runs
and either:
- (a) Resumes them automatically (if the gate answer is still
  available from the transport's perspective), or
- (b) Marks them as "interrupted" in the UI, allowing the user to
  manually resume or discard them

**Files**: `internal/web/handlers/chat.go` (startup scan),
`internal/web/sse/` (resurrection events).

**Test**: After restart, the web UI shows "Interrupted — click to
resume" for runs that were mid-gate when the process died.

## Configuration

```json
{
  "durable_resume": {
    "enabled": false,
    "max_resume_age_minutes": 30,
    "auto_resume_on_startup": true
  }
}
```

- `enabled`: Master switch. Default off.
- `max_resume_age_minutes`: Don't resume gates older than this.
- `auto_resume_on_startup`: Automatically re-emit pending gate
  prompts on startup.

## Risk mitigation

1. **Opt-in**: Feature is disabled by default. Existing behavior is
   unchanged when disabled.
2. **Idempotency audit**: Tools are classified before resume is
   attempted. Must-never-replay tools are skipped.
3. **Max resume age**: Stale gates (e.g., from a crash days ago) are
   not resumed — the user must start a new turn.
4. **Graph evolution guard**: Not applicable (no graph migration).
5. **TUI inclusion**: TUI has its own gate implementations
   (`internal/tui/gates.go`) and its own run loop. Durable resume
   covers TUI gates (Phase 1 ctx-aware fix applies to both web and
   TUI gate implementations). The TUI run loop itself is not
   rewritten — it benefits from durable resume through the same
   gate-as-pause mechanism.

## Testing strategy

- Unit tests for each phase (ctx-aware gates, durable service,
  session identity, resume driver)
- Integration test: full restart-during-gate scenario
- Chaos test: kill -9 during a gate, restart, answer, verify resume
- Regression test: with durable resume disabled, all existing tests
  pass unchanged
- TUI test: `/stop` during a pending TUI approval/clarify unblocks
  the gate within one poll interval
