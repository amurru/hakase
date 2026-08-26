# Research: Sidekick Mode

Feature: `sidekick-agent`. Companion documents: `spec.md` (atomic specs), `plan.md` (phases).
Date: 2026-08-24. Status: research complete, decisions recorded.

## 1. What the feature is

A **sidekick** is a second, independently configured LLM that runs alongside the
primary orchestrator model with two capabilities:

1. **Side-process** - answer a user request independently of the main agent
   (a "second opinion" channel: fast, cheap, no tools, no session side effects).
2. **Consult / watchdog** - while the primary agent works, the sidekick observes
   the conversation trajectory and flags when the primary **missed something**
   (unaddressed sub-questions, contradictions, skipped constraints). Its
   observations are injected back into the running session as advisory notes.

The primary model can also deliberately consult the sidekick mid-task via a
tool (`ask_sidekick`), mirroring how it delegates to sub-agents today.

## 2. Prior art surveyed

| Pattern | Where seen | Takeaway for hakase |
| --- | --- | --- |
| Critic / LLM-as-judge agent | Multi-agent literature (actor-critic loops, multi-agent debate) | Sidekick output must be structured ("gap found: yes/no" + note) so injection decisions are mechanical, not vibes. |
| Second-opinion skill (`second-opinion`, gopherguides) | agentskills.so | Trigger discipline matters: suggest/consult on complex or high-stakes turns, never on trivial ones; explicit user request always wins. |
| GitHub Copilot CLI "Rubber Duck" (Apr 2026) | 4sysops article | A watcher model reviewing an agent mid-run is a shipped product pattern; it reviews *trajectory*, not just final text. |
| Aider architect/editor mode | aider.chat | Two-model split where one proposes and another executes; hakase's analog is sidekick-advises / orchestrator-executes - the sidekick must never hold write tools in v1. |
| hakase's own USER INTERJECTION steering | internal/util/queue.go + internal/context HistoryBuilder | The exact mechanism needed to push sidekick notes into a running session already exists; reuse it rather than inventing a new channel. |

## 3. Codebase integration points (verified)

### 3.1 Secondary-model precedent: vision

`internal/config/config.go` already carries a full secondary-model pattern:
`VisionModel` / `VisionBaseURL` / `VisionAPIKey` / `VisionProvider` with env
overrides (`HAKASE_VISION_*`). `SetupRunner` creates the secondary LLM via a
provider resolver (`deps.ResolveVisionProviderFn`, implemented in both
bootstraps as `resolveVisionProvider`) and stores it in a package global
(`vision.VisionModelLLM`). Failed secondary-model creation is non-fatal.

**Decision**: mirror this shape exactly for `sidekick.*` config +
`internal/sidekick` state holder. No new provider machinery needed -
`LLMProvider.CreateModel` covers gemini/openai/openai-compatible.

### 3.2 Injection into a running turn

`HistoryBuilder.BeforeModelCallback` (internal/context/context.go) fires on
every orchestrator model call and already appends:

- persisted history,
- one-shot notices (context-file reconcile, environment staleness),
- queued user prompts (`util.PendingQueue.Snapshot()` rendered via
  `util.SteeringContent` as `USER INTERJECTION (...)`).

**Decision**: add a sidekick-note queue drained in the same callback, framed as
`SIDEKICK NOTE (advisory)` user-role content, sanitized with
`hctx.WrapUntrustedData`/`hctx.SanitizeContextContent`, capped per turn.
Re-injection every call (the established pattern) keeps notes alive across the
run; notes are cleared at run end.

### 3.3 Observation hook

ADK v2 (`google.golang.org/adk/v2@v2.1.0`) `llmagent.Config` supports:

```go
BeforeModelCallback func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error)
AfterModelCallback  func(agent.Context, *model.LLMResponse, error) (*model.LLMResponse, error)
// returning non-nil replaces the real response/error (short-circuit power)
OnModelErrorCallback, BeforeToolCallback, AfterToolCallback,
BeforeAgentCallback, AfterAgentCallback ...
```

Callbacks are registered per-agent in `llmagent.Config`; hakase registers them
only on the root orchestrator (sub-agents get their own list), and
`delegate_task` builds fresh `llmagent.New`s without them.

**Decisions**:

- Watch mode uses `AfterModelCallback` on the **root orchestrator only**. The
  callback itself never blocks: it appends the response text to a per-run
  transcript buffer and pokes an async evaluator goroutine (debounced,
  rate-limited, ctx-cancelled with the run). It never returns a replacement
  response in v1.
- `ask_sidekick` is a plain function tool on the orchestrator (synchronous but
  hard-timeout-capped) for deliberate consultations.

### 3.4 Eventing to UIs

`interfaces.EventNotifier` (TaskUpdate, DelegationProgress, CronJobEvent) is
implemented by both `sse.EventBridge` (web) and the TUI model. Adding a
`SidekickEvent(status, message string)` method touches exactly two in-repo
implementers plus the interface. SSE gets a new `sidekick` event type; TUI gets
log-pane lines (v1) with a badge later.

### 3.5 Session persistence

`internal/session.MessageKind*` constants: text, tool_call, tool_result,
summary. A new `MessageKindSidekick = "sidekick"` renders distinctly in both
UIs and survives resume; history rebuild must skip it by default (it is
advisory, not ground truth) unless config opts it into context.

### 3.6 Bootstrap wiring constraint

`cmd/hakase/main.go` (TUI) and `cmd/hakase/web.go` (web/serve) each build
`agent.Deps` with bridge factories because `internal/web/handlers` imports
`internal/cli` (import-cycle rule from AGENTS.md). Any new cross-package
capability needs a factory field on `Deps` - hence
`ResolveSidekickProviderFn` mirroring `ResolveVisionProviderFn`.

## 4. Key design decisions (closed)

- **D1 Modes**: `off` | `on_demand` | `watch` | `full`.
  `on_demand` enables `/sidekick` + `ask_sidekick`; `watch` adds the
  AfterModelCallback monitor; `full` = both.
  Default resolution (user decision Q1-B): block absent or disabled -> `off`;
  enabled with a model but empty `mode` -> `on_demand`. Configuring a model
  signals intent for direct consultation, but a background watcher that bills
  per call is never started implicitly.
- **D2 Fail-open**: every sidekick failure (no key, timeout, HTTP error, parse
  error) degrades to a log line. It can never fail or stall the primary run.
- **D3 Async-only watching**: watch evaluations are debounced and rate-limited;
  at most one in-flight evaluation per run; max N injected notes per turn
  (default 2); each note size-capped (default 1200 chars).
- **D4 Advisory framing**: notes are data, framed `SIDEKICK NOTE`, wrapped as
  untrusted content; the primary weighs them like any system reminder.
- **D5 Toolless sidekick in v1**: text-only prompts (transcript snapshot in,
  structured verdict out). Read-only tools deferred to v2.
- **D6 Same-provider warning**: sidekick == primary provider+model is allowed
  but logged as a cost warning (self-review still has value; user decides).
- **D7 Transcript budget**: evaluator sees the original request + last K
  messages/tool events (default ~6k chars window), never the whole history.
  Scope is the CURRENT RUN only - persisted session history is excluded
  (user decision Q2-A). The primary already receives history via the
  HistoryBuilder; the sidekick judges only the active trajectory.
- **D8 Severity rendering (Q3)**: all severities render as quiet inline chips;
  info/warning/critical differ by color and icon only. No notifications,
  pings, or toasts for critical notes in v1 - a false-positive interruption
  costs more than a glanceable chip.
- **D9 Privacy posture (Q4)**: sending conversation excerpts (including tool
  output such as web page contents) to the configured sidekick endpoint is
  accepted by design. Documented, not blocked: README feature section carries
  a privacy note, and the Settings UI shows a footnote pointing at local
  models via `openai-compatible` as the data-leaves-the-machine escape hatch.
- **D10 Naming locked (Q5)**: feature "sidekick"; tool `ask_sidekick`;
  command `/sidekick`; SSE event `sidekick`; config block `sidekick`.
  Renaming after SK-005 lands would be model-visible; treat as frozen.

## 5. Risks

| Risk | Mitigation |
| --- | --- |
| Note arrives after run finished | Queue bound to run lifetime; late notes are dropped with a debug log. |
| Runaway eval cost on long runs | Debounce (default 20s), per-run eval cap, transcript window cap, mode off by default. |
| Prompt-injection laundering through sidekick output | Sanitize + wrap + cap; primary instructed notes are advisory data (UntrustedContentPolicy already covers tool-shaped input). |
| Interface change ripples | EventNotifier extension is compile-time enforced across the two implementers; both updated in one spec. |
| Web/TUI divergence | Shared core in `internal/sidekick`; UIs only consume events + endpoints. |

## 6. Out of scope (v1)

- Sidekick tools / sandbox access (v2 candidate).
- Sidekick-driven auto-correction (replacing model responses via
  AfterModelCallback short-circuit) - observation only.
- Parallel dual-draft answers merged automatically (user-visible second draft
  comes from `/sidekick`, not auto-fused).
- Watching delegated sub-agents (`delegate_task` runs stay unwatched in v1).
