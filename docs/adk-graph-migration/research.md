# Research: Full ADK Graph migration vs. limited adoption

Branch: `research/durable-resume` (extends RQ4 from `docs/durable-resume/research.md`)

This document evaluates whether hakase should migrate its entire agent
orchestration to the ADK workflow graph engine, or keep the classic Runner
with graph adoption limited to specific features. The analysis covers the
full migration surface: all transports, the delegation system, gates,
session management, and the execution canvas.

## Current architecture (what exists today)

Hakase uses the **classic ADK Runner** (`runner.Runner`) for all agent
execution. The runner drives a single root `llmagent.New()` orchestrator
with sub-agents exposed via the `delegate_task` tool. Key components:

| Layer | Implementation | Location |
|-------|---------------|----------|
| Run loop | `agentrun.Driver.RunTurn` | `internal/agentrun/agentrun.go` |
| TUI run loop | `AppModel.runAgentTask` (own copy) | `internal/tui/ui.go:2228` |
| Delegation | `delegateTaskHandler` (custom, tool-based) | `internal/agent/delegate.go` |
| Gates | Approval/Clarify (custom, ctx-blind) | `internal/agent/gate.go`, `approval.go`, `clarify.go` |
| Session | `hakasesession` JSON store (parallel to ADK) | `internal/session/` |
| ADK session | `session.InMemoryService()` (ephemeral) | `agent.go:2612`, `delegate.go:433` |
| Canvas | `graphTracker` + `delegationReporter` | `agentrun/graph.go`, `delegate.go` |
| Context | `HistoryBuilder` + compaction | `internal/context/` |
| Hooks | `BeforeToolCallback`/`AfterToolCallback` | `internal/hooks/` |

Transports consuming the runner: **web** (SSE bridge), **Telegram**,
**Discord**, **TUI** (own loop), **cron** (headless).

## What the ADK graph engine provides

The `workflow` package (`adk/v2@v2.4.0/workflow/`) offers:

- **Multi-node graphs** with explicit edges, branching, join nodes
- **Parallel execution** (`parallel_worker.go`)
- **HITL pause/resume** via `RequestInput` events with `InterruptID`
- **Retry loops** (`retry.go`)
- **Dynamic scheduling** (`dynamic_scheduler.go`)
- **State persistence** via `session.Service` + `ReconstructRunState`
- **Resume** from paused state (`workflow/resume.go`)

## Evaluation dimensions

### 1. Blast radius

**Full graph migration** requires re-platforming every transport:

| Transport | Current | Graph migration cost |
|-----------|---------|-------------------|
| Web (SSE) | `agentrun.Driver` + `graphTracker` | Rewrite event mapping; canvas events now come from graph nodes |
| Telegram | `agentrun.Driver` + `runView` mirror | Same as web |
| Discord | `agentrun.Driver` | Same as web |
| TUI | Own `runAgentTask` loop | **Complete rewrite**; TUI bypasses `Driver` entirely |
| Cron | `agentrun.Driver` (headless) | Straightforward |
| MCP server | `agentrun.Driver` | Straightforward |

The TUI is the hardest: it carries a full copy of the run loop with
tea-program message plumbing, degeneration guard, and interrupt handling.
A graph migration means either (a) rewriting this against the workflow
event stream, or (b) maintaining two parallel run paths indefinitely.

**Limited adoption** (graph for specific features, classic Runner for
the main loop) has near-zero blast radius: the `agentrun.Driver` and
TUI loop stay untouched.

### 2. Gate semantics

The durable-resume research (RQ1) already established that **gate
semantics cannot be expressed in the engine**. The `RequestInput`
primitive carries an opaque payload but has no timeout, no
first-responder-wins, no deny-on-expiry. These are hakase-side
inventions (`web/handlers/approval.go`, `clarify.go`).

A full graph migration does **not** eliminate the custom gate layer.
It would only change the *transport* of the pause signal (from a
blocking channel to a `RequestInput` event). The gate logic (expiry,
routing, first-wins, deny-on-expiry) stays custom regardless.

**Verdict**: Full migration does not simplify gates. It adds a
translation layer between two pause representations.

### 3. Delegation model

Hakase's delegation is **dynamic and tool-driven**: the orchestrator
decides at runtime whether to delegate, to whom, and with what
toolset. The `delegate_task` tool creates a fresh sub-agent per call
with a restricted toolset, isolated session, and watchdog.

The graph engine's model is **static and structure-driven**: nodes are
declared at build time with fixed edges. Parallel execution exists
(`parallel_worker.go`) but requires knowing the fan-out structure
ahead of time.

Mapping dynamic delegation to static graphs would require either:
- (a) A graph per delegation type (loses runtime flexibility), or
- (b) A single "delegate" node that internally runs the current
  `delegateTaskHandler` (graph becomes a pass-through, no benefit).

**Verdict**: The current tool-based delegation is strictly more
flexible than what the graph engine offers. No migration benefit.

### 4. Session and resume

The durable-resume research (RQ3) found that hakase already uses
`session.InMemoryService()` for ADK sessions, meaning all ADK-side
state evaporates on restart. The hakase JSON session store is a
parallel universe the engine never reads.

A full graph migration would require:
- Implementing `session.Service` over a durable backend
- Changing ADK session identity from per-turn to per-conversation
- Handling graph-evolution corruption (explicit TODO in engine)
- Rebuilding context from JSON on every resume

These are **prerequisites for any resume approach**, not unique to
graph migration. The classic Runner already supports resume via
`run_node.go:179-194` (reconstructs state from history, calls
`wf.Resume` when the incoming message answers an open interrupt).

**Verdict**: Resume capability is orthogonal to graph migration.
The classic Runner path (option B from the durable-resume research)
delivers the same resume benefit with less risk.

### 5. Execution canvas

The canvas system (`graphTracker` + `delegationReporter`) already
provides structured `GraphEvent` frames for the web UI. It maps the
ADK event stream onto `agent_start/agent_end/tool_start/tool_end/
transfer` events.

In a full graph migration, the engine produces its own node-level
events. The canvas would need to map graph node events onto the
same `GraphEvent` schema. This is feasible but requires:
- A graph-node-to-canvas-node mapping layer
- Handling of parallel branches (multiple active nodes)
- Transfer/branch semantics that don't exist in the current canvas

**Verdict**: Canvas compatibility is solvable but non-trivial.
Limited adoption keeps the existing canvas untouched.

### 6. Context and hooks

Hakase's context injection is tightly coupled to the runner's
`BeforeModelCallback`/`AfterModelCallback` and the `HistoryBuilder`.
The sidekick watcher, vision injection, knowledge enrichment, and
tool-result guard all ride these callbacks.

The graph engine has its own callback model (node-level
`Run` methods). Migrating context injection to graph nodes would
require re-implementing `HistoryBuilder` as a graph-aware component
or wrapping it in a custom node.

**Verdict**: Context migration is a significant rewrite with no
functional gain. The current system works and is well-tested.

### 7. Extensibility

Hakase's tool-based model allows adding new capabilities by:
1. Writing a new `tool.Tool`
2. Registering it on the orchestrator's tool list
3. The model learns to use it via the tool description

This is **open-ended**: any future capability (new MCP server, new
sandbox mode, new gate type) can be added without structural changes.

A graph migration **constrains** extensibility: new capabilities
require new nodes, new edges, and graph validation. The graph
structure becomes a maintenance burden as features grow.

## Cost-benefit summary

| Dimension | Full graph migration | Limited adoption |
|-----------|---------------------|------------------|
| Blast radius | All 5 transports + TUI rewrite | Near-zero |
| Gate simplification | None (gates stay custom) | N/A |
| Delegation flexibility | Reduced (static graphs) | Preserved |
| Resume capability | Same as option B | Same as option B |
| Canvas compatibility | Needs new mapping layer | Preserved |
| Context/hooks | Major rewrite | Preserved |
| Extensibility | Constrained by graph structure | Open-ended |
| Risk of regression | High (touches everything) | Low |
| Engine upgrade risk | Graph evolution corrupts resumes | Isolated to graph features |

## Decision

**Keep the classic Runner.** No full graph migration. This is the
confirmed direction for the project.

Graph patterns may still be adopted for isolated features where they
provide clear value, but the main orchestration loop stays on
`runner.Runner` + `agentrun.Driver`.

1. **Parallel research**: If a future feature requires fanning out
   to N sub-agents and joining results, implement it as a single
   graph workflow invoked from a tool, not as a rewrite of the
   main loop.

2. **HITL pause primitives**: If the durable-resume work (option B)
   needs engine-visible pauses, use `RequestInput` events within
   the classic Runner's existing `LongRunningToolIDs` mechanism
   rather than migrating to graph nodes.

3. **Retry loops**: If specific tools need retry semantics, wrap
   them at the tool level (as `delegateTaskHandler` already does
   with its watchdog + repair loop) rather than using graph retry
   nodes.

The graph engine is a powerful abstraction for **static, well-understood
multi-agent workflows**. Hakase's orchestration is **dynamic,
tool-driven, and context-heavy** - a poor fit for static graphs and a
good fit for the classic Runner's open-ended tool model.

The durable-resume research (option B) remains the recommended path:
keep the runner, make pauses durable, and add a resume driver. This
delivers the user-facing benefit (survive restarts during gates)
without re-platforming the entire agent core.
