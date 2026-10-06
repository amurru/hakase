# Research: durable human-in-the-loop resume (DR)

Issue [#74](https://github.com/amurru/hakase/issues/74) — last Tier-2
item from #20. This document answers the four research questions. No
plan yet: the approach is chosen from these findings.

Sources: ADK Go v2.4.0 module source (`workflow/`, `runner/`,
`session/`), plus the hakase run/gate/session architecture. All ADK
paths below are absolute under
`/home/amurru/go/pkg/mod/google.golang.org/adk/v2@v2.4.0/`;
all hakase paths under the repo root.

## RQ1 — Can the engine express our gates?

Mostly no. The pause primitive (`RequestInput`,
`session/session.go:184-212`) carries an opaque `Payload` + `Message`
+ optional response schema — our tool/risk/command data fits there —
but the wait itself has **no timeout/expiry field, no
first-responder-wins, no deny-on-expiry**. Expiry (60s/120s/300s),
first-wins (cap-1 channel + delete-on-receive), and fail-closed deny
are all hakase-side inventions (`web/handlers/approval.go`,
`clarify.go`) that the engine cannot see. Concurrent duplicate
`Resume` calls are explicit no-ops; "last response wins" in history.
Verdict: whatever we adopt, **our gate layer stays** — the engine
would supply pause *durability*, never gate *semantics*.

## RQ2 — What does resume replay?

Completed nodes are skipped, never replayed (`resume.go:211-217`,
"keeps Resume idempotent"). But a node that dies **before emitting**
its pause event leaves nothing in history → next turn re-runs it from
scratch, **including re-executing its tool calls** — no idempotency
keys, no dedup, no transactional tools anywhere in the engine. Our
~60 tools have no idempotency story either. Consequence: any resume
design must either (a) guarantee the pause event is persisted *before*
any side effect can be lost (engine gives this for its own pauses:
non-partial events persist before yield, `runner/runner.go:764-770`),
or (b) audit tools for safe retry. There is no free exactly-once.

## RQ3 — Session-format compatibility

There is **no workflow blob**: persistence is the 5-method
`session.Service` (`Create/Get/List/Delete/AppendEvent`) plus event
history that `ReconstructRunState` scans (`workflow/persistence.go:80`).
Hakase passes `session.InMemoryService()` to its runners
(`agent.go:2612`, `delegate.go:433`) — everything ADK-side evaporates
on restart by construction; hakase's own JSON sessions are a parallel
universe the engine never reads. Adopting resume means implementing
`session.Service` over something durable (our JSON store with faithful
`Event` JSON incl. `RequestedInput`/`NodeInfo`/`LongRunningToolIDs`/
`InvocationID`, or a DB backend like the engine's GORM one) — and
**never dropping those fields**, silently fatal to rehydration. Doc
comments in the engine still describe a `RunStateSessionKey` blob that
does not exist in v2.4.0: trust code, not comments. No cross-process
resume example exists upstream; we would be the first to prove it.
Graph evolution silently corrupts resume (explicit TODO, unguarded) —
any orchestrator change needs a version guard for in-flight resumes.

## RQ4 — Full migration vs narrow slice (verified, not assumed)

The surprise finding: **we are already on the resume-capable path.**
`runner/run_node.go:179-194` reconstructs state from history and
calls `wf.Resume` when the incoming message answers an open interrupt —
in the *classic* Runner hakase uses today, not only in graph
workflows. Full graph migration is therefore not a prerequisite for
anything. Three architectures priced:

- **A. Graph workflow migration.** Rewrite turns as graph nodes with
  RequestInput gate nodes. Gets engine-native pause bookkeeping, but
  re-platforms every surface (TUI carries its own run copy,
  `tui/ui.go:2228-2352`, and would need the same migration), fights
  RQ1 (gates stay custom anyway), and inherits graph-evolution risk
  across the whole run core. Biggest blast radius, least incremental.
- **B. Classic Runner + durable pauses (recommended).** Keep the
  runner, the driver loop, and our gate UX. Change three things:
  (1) durable `session.Service` so ADK history survives;
  (2) gates re-expressed as ADK-visible pauses (long-running tool
  responses / RequestInput events) so history shows the wait instead
  of an unanswered tool call; (3) a resume driver translating
  responder answers into `Runner.Run` calls carrying synthetic
  `FunctionResponse`s against the *same* ADK session/invocation.
  Streaming (`iter.Seq2` events incl. partial deltas) maps onto
  `EventSink` directly.
- **C. Narrow durable gates, no ADK resume.** Persist pending gate +
  turn context in hakase JSON; on restart re-drive the turn with the
  answer pre-injected. Cheapest, but re-runs the model prefix every
  resume (cost) and replays pre-gate tools with no skip-completed
  protection (worst replay hazard of the three).

Two structural blockers apply to A and B equally, found by reading
our code (not the engine): (1) **ADK session identity** — hakase mints
a fresh ADK session per turn (`RegisterTaskSession(taskID,…)`,
taskID-as-session-id), rebuilding context from JSON via
HistoryBuilder; resume requires reusing the paused turn's ADK
session/invocation, so the identity strategy must change.
(2) **Gate blocking is ctx-blind** — `AskApproval` selects only on
its channel and the expiry timer, so `/stop` cannot unblock a gated
run today; any resume/cancel design needs ctx-aware gates first.
Plus a prerequisite for all options: a tool idempotency audit
(separate the safe-to-retry from the must-never-replay).

Streaming compatibility is confirmed (engine yields text deltas,
tool notifications, and the prompt event live; caller cancels by
breaking the range) and TUI exclusion must be explicit (it bypasses
`Driver` entirely).

## Recommendation for planning

Price option B as the primary with C as the fallback if the
gate-to-pause rewiring proves too invasive; drop A (cost without
commensurate gain — RQ1 removes its main selling point). Planning
prerequisites in order: ctx-aware gates → durable session.Service →
stable ADK session identity → gate-as-pause rewiring → resume driver
→ idempotency audit → transport run-view resurrection (frozen
`Working…` status must detect a resumable death vs a live run).
