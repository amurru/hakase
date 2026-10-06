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

### Phase 3: Pause registry (revised — no identity change)

**Problem**: Resume requires reusing the paused turn's ADK
session/invocation, but hakase mints a fresh ADK session per turn
(`RegisterTaskSession(taskID, sessionID)` where `taskID` is generated
per run) and the in-memory task→session map is dropped at turn end
(and lost entirely on restart).

**Design correction (verified against
`internal/context/context.go:261-265`)**: the ADK session key must
NOT become the hakase session ID. `HistoryBuilder.BeforeModelCallback`
prepends file-backed history on every model call under the documented
invariant that "ADK session events never contain our file-backed
history" — stable cross-turn ADK sessions would break that invariant
and duplicate the full transcript into every prompt. Per-turn ADK
sessions stay exactly as they are.

Instead, resume re-enters the runner against the *paused turn's* ADK
session ID (which persists in the durable store from Phase 2). The
missing link is knowing *which* ADK session holds the pause after a
restart. That is a durable **pause registry**:

- When a gate blocks, `ApproveExec`/`askClarify` (the single
  transport-agnostic choke points) record `{pauseID, hakaseSessionID,
  adkSessionID (TaskIDFromCtx), gate type, prompt summary/detail,
  createdAt}` to `<sessionsDir>/adk/pauses.json`.
- When the gate resolves (answer, timeout, cancel), the record is
  removed. Leftover records after a restart = interrupted pauses =
  resumable (Phase 5) or resurrectable in the UI (Phase 7).
- Recording is best-effort and gated on `durable_resume.enabled`:
  registry unavailable → gates work exactly as before, durability
  silently degrades (never fail the gate on a bookkeeping error).

No changes to `agentrun.go`, the TUI loop, or `delegate.go`: normal
turns keep fresh per-turn ADK sessions; only resume turns (Phase 5)
address old sessions.

**Files**: `internal/session/pauses.go` (new) + tests,
`internal/agent/deps.go` (PauseRegistry field),
`internal/agent/approval.go`, `internal/agent/clarify.go`
(record/unrecord wrappers), `internal/agent/agent.go` (build
registry in SetupRunner when enabled).

**Test**: record exists while a gate is blocked, removed after
resolve; records survive a simulated restart; nil registry =
gates unaffected.

### Phase 4: Gate-as-pause rewiring (done — IsLongRunning flags)

**Problem**: Gates block inside tool execution. The ADK runner sees a
tool call that never returns (until the gate resolves). For resume to
work, the pause must be visible in ADK session history so a later
answer matches the open call.

**Investigation outcome**: the engine's full long-running protocol
(handler returns nil, engine parks, reply re-dispatches the tool)
does NOT fit synchronous gates — our handlers block-then-return and
must keep doing so (gate UX, expiry, first-wins all live there).
What DOES fit is the static `tool.IsLongRunning` flag alone:
`base_flow.go:1040` marks the FunctionCall event with
`LongRunningToolIDs=[callID]` whenever the called tool reports it,
with zero change to synchronous behavior. Verified by spike tests
(`internal/agent/gate_pause_engine_test.go`, scripted model, durable
service, simulated restart across service instances):

1. A completed turn through a long-running tool finishes normally;
   follow-up turns are unaffected (markers don't poison sessions).
2. Crash shape (call + marker persisted, no response) + later
   `FunctionResponse` with the call ID resumes WITHOUT re-executing
   the tool.
3. Control without the marker also consumes the answer, but via the
   accidental fresh-run path — no `wf.Resume` validation, duplicate
   suppression, or waiting-node bookkeeping. Markers remain required:
   they select the designed resume path and make open pauses
   detectable (`openLongRunningCallIDs`).

**Change**: `IsLongRunning: true` on the four gate-hosting tools —
`clarify`, `system_exec`, `system_exec_start`, `python_interpreter`.
Read-only companions (`system_exec_status/kill/list`) stay unmarked;
`delegate_task` stays unmarked (its result can't be supplied as an
answer). Git tools (approval possible inside the git engine) are a
documented limitation: Phase 5 falls back to re-drive for those.

**Resume-semantics note for Phase 5**: for `clarify` the answer IS
the tool result, so direct `FunctionResponse` resume is correct. For
approval-inside-a-tool the answer is NOT the tool result (the command
never ran) — Phase 5 must re-drive the turn with approval
pre-granted; there the marker serves pause *detection*, paired with
the Phase 3 pause record to distinguish "died waiting for approval"
(safe to re-drive) from "died mid-execution" (never auto-resume).

### Phase 5: Resume driver (done)

**Problem**: After restart, the system must detect runs paused on a
gate and allow the user to answer them.

**Change** (`internal/agent/resume.go`): `ListResumablePauses` scans
the Phase 3 registry and returns records that are fresh (within
`ResumeMaxAge`), addressed to a past ADK session, and whose durable
history still holds open long-running calls (the engine's
`openLongRunningCallIDs` shape, reimplemented in
`scanPausedSession`). Stale, unaddressed, settled, and
history-pruned records are skipped.

Two settle paths, split by resume semantics (see Phase 4 note):

- **Clarify** — `ResumeClarify(ctx, runner, pauseID, answer)`:
  the answer IS the tool result, so it is injected as a
  `FunctionResponse` per open call against the paused ADK session.
  The engine matches the open interrupt and continues WITHOUT
  re-executing any tool (proven end-to-end: scripted model, durable
  service, simulated restart; the clarify gate is never
  re-consulted). Nested gates raised by the resumed turn route back
  to the asking conversation via `RegisterTaskSession`. The pause
  is unrecorded only on successful completion, so a run error
  keeps the record for retry.
- **Approval** — `ResolveApprovalPause(ctx, pauseID, approved)`:
  the answer is NOT the tool result (the command never ran), so
  response injection would lie to the model. Denial unrecords and
  returns false (the command never runs). Approval installs a
  one-shot exact tool+command pre-grant (consumed by `ApproveExec`
  without blocking, never recorded as a pause) and returns true:
  the caller re-drives a FRESH turn (normal `RunTurn`), during
  which the re-issued call passes its gate. Approval re-drive
  refuses when history holds completed never-replay calls (Phase 6
  guard) — the record is kept for manual handling.

Shared `ResumeAppName`/`ResumeUserID` constants pin the ADK session
coordinates across `SetupRunner`, `agentrun`, and the driver.

**Files**: `internal/agent/resume.go` (driver), `internal/agent/approval.go`
(pre-grant check), `internal/agent/agent.go` + `internal/agentrun/agentrun.go`
(constants wiring).

**Test**: `internal/agent/resume_driver_test.go` — listing filters,
clarify end-to-end with zero tool re-execution, pre-grant one-shot
and scoping, risky-history refusal.

### Phase 6: Idempotency audit (done — table + enforcement)

**Problem**: RQ2 from the research found that resume replays any tool
calls that completed before the pause but whose results were not
persisted. Without idempotency keys, a resumed run may re-execute
side-effecting tools (e.g., `write_file`, `system_exec`).

**Outcome**: the audit (`internal/agent/replay_policy.go`,
`ReplayPolicyFor`) classifies every known tool as safe (read-only),
idempotent (same-input overwrite/upsert), or never-replay (exec,
delegation, scheduling, network, mutation, per-call identity,
gates). Unknown names default to never (fail-closed) until
explicitly audited.

Enforcement falls out of the Phase 5 split, and is pinned by tests:

- Clarify resume needs NO guard: the engine reuses settled history
  and never re-executes (spike + end-to-end proof). Completed
  side-effecting calls before the question stay settled.
- Approval re-drive IS a fresh turn, so `ResolveApprovalPause`
  refuses when the paused history holds completed never-replay
  calls (naming them in the error) instead of risking double
  execution. `write_file`/`update_task`-class completions do not
  block re-drive.

**Files**: `internal/agent/replay_policy.go` (new),
`internal/agent/resume.go` (guard in `ResolveApprovalPause`).

**Test**: `internal/agent/replay_policy_test.go` pins the
load-bearing never classifications (`system_exec`,
`delegate_task`, `cron`/`cronjob`, `download_file`, ...), both
permissive tiers, and the fail-closed default;
`TestResolveApprovalPauseRefusesRiskyHistory` pins the refusal.

### Phase 7: Transport run-view resurrection (done — web)

**Problem**: After restart, the web UI shows a frozen "Working..."
status for runs that were in-flight. The user cannot tell if the
run is still active or died with the process.

**Change** (`internal/web/handlers/resume.go`, gate tracking,
`internal/web/spa.go` wiring):

- `GET /api/resumable` lists resumable pauses (pause/session/gate/
  summary/detail/age/open calls) for "interrupted — click to
  resume" display. Empty (not an error) when the feature is off.
- Startup re-emit (`ResurrectInterruptedPrompts`, behind
  `AutoResumeOnStartup`): every resumable pause is re-emitted as a
  native SSE gate prompt with a resurrected ID (`rsm_<pauseID>`).
  The UI answers through the UNCHANGED respond endpoints: unknown
  live IDs fall through to the resume backend (`*ChatAPI`), so no
  client changes are needed.
- Approval answers settle via `ResolveApprovalPause`; an approval
  re-drives a fresh turn in the background with the paused turn's
  original input (`PausedTurnInput`, new in `internal/agent`).
  Clarify answers resume the paused turn in the background,
  streaming + persisting like any run. Statuses: 200 settled/denied,
  202 background resume, 404 unknown, 409 unsafe history, 410
  expired, 429 session busy.
- Mappings drop on terminal states; unsafe-history pauses keep both
  record and mapping for manual handling.

Out of scope (documented follow-ups): TUI and channel transports
share the agent driver but need their own prompt re-emission.

**Files**: `internal/web/handlers/resume.go` (new),
`internal/web/handlers/approval.go` + `clarify.go` (tracking +
fallthrough), `internal/web/handlers/chat.go` (route + return
ChatAPI), `internal/web/spa.go` (backend wiring + startup re-emit),
`internal/agent/resume.go` (`FindResumablePause`,
`PausedTurnInput`).

**Test**: `internal/web/handlers/resume_test.go` (tracking,
fallthrough both gates, off-shape listing, status mapping, detail
converters); driver settle paths already pinned in
`internal/agent`.

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
