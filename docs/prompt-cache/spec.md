# Spec: Prompt-Cache Stability

Governing issues: #106 (PC-001..PC-003), #107 (PC-006/PC-007),
#108 (PC-004/PC-005).
Reference: 2026-10-10 harness audit - Claude Code prompt-caching docs (prefix
invalidation list), Codex (`prompt_cache_key`, `x-codex-turn-state`,
`store: false` full replay), OpenCode (`promptCacheKey` = session id), Aider
(`--cache-keepalive-pings`) - plus the internal deviations below.
Design review: pro critique rounds 1-2 (frozen-insert lock, render-from-store
rule, invocation-state lifetime).

**Scope:** make every model request prefix-cache optimal. One canonical
renderer; harness-injected context frozen per turn and assembled at the end of
the rendered store; history append-only between compactions; cache health
observable. **Non-goals:** tool-transcript persistence, session-handle/delta
transport, new model providers, session-store schema changes.

## Problem

A request's reusable prefix ends at the first byte that differs from the
previous request, so any harness-side rewrite of earlier content invalidates
everything after it. Six current deviations:

| # | Deviation | Evidence |
|---|---|---|
| V1 | Memory / SessionStart / UserPromptSubmit blocks are prepended at the head, rendered once, then disappear | `internal/context/context.go:363-373`; `internal/context/memory_test.go` |
| V2 | Live text parts are raw; replayed text is wrapped in `<UNTRUSTED_DATA>` | `internal/context/context.go:507-546` vs the live paths |
| V3 | Project snapshot mutates the live user content but is never persisted, breaking dedup and duplicating the prompt | `internal/agentrun/agentrun.go:246-252`, `internal/context/context.go:481-497` |
| V4 | Context-update / env-staleness notices are inserted between history and current content | `internal/context/context.go:406-420` |
| V5 | Every old summary is kept and replayed ("summaries first"), so the head grows redundant content | `internal/session/session_service.go:212-227`, `internal/context/context.go:456-476` |
| V6 | Sidekick notes are persisted mid-turn and appended at the tail, flipping order between calls | `internal/context/context.go:436-449` |

Steering drifts too: appended after tool events on every call
(`internal/context/context.go:427-431`), so each tool round pushes it down.

## Decisions

- **D1 - Invariant P.** A request is `[rendered store][frozen turn blocks]
  [steering][un-persisted run tail]`. The rendered store is append-only between
  compactions; the only sanctioned prefix rewrites are compaction, logged once
  per occurrence, and a steering arrival, logged.
- **D2 - Render from the store.** Anything with a persisted counterpart is
  rendered from the persisted message, never from an independently assembled
  live copy. Ingestion normalizes once (LF newlines, NFC); persist exactly
  what is sent. The renderer is a pure function of stored bytes.
- **D3 - Frozen turn context.** Turn-injected content is rendered exactly once
  per turn on the first model call and stored in ADK invocation state under a
  `temp:` key (`agent.Context.State()`, ADK `adk/v2 agent/context.go:148`;
  `temp:` values are dropped at invocation end and never persisted,
  `internal/session/adk_service.go:222`). The assembly path can only read the
  frozen payload; render functions are unreachable from later calls, so
  mid-turn mutation is impossible by construction.
- **D4 - One assembly point.** `BeforeModelCallback` replaces `req.Contents`
  with the assembled list: every in-context store message in sequence order
  (summaries first, current behavior), the live leading user content dropped
  when it matches the last persisted user message, then the frozen blocks,
  steering, and finally ADK's un-persisted tail (tool calls/results). No
  harness content is ever appended at the end.
- **D5 - Context classes.** Persist-once: SessionStart hook context (inserted
  at the store head before the first request of a session; zero cache cost at
  insertion, cached reads afterwards). Turn-frozen: memory, project snapshot,
  prompt hooks, notices. A mid-turn change (staleness tripping after tool call
  3) renders into the next turn's frozen set. The memory block re-renders per
  turn so `remember`/`forget` updates land immediately; its tax is bounded by
  `memory.max_prompt_chars` (default 4000 chars, roughly 1-1.5k tokens per
  turn), documented in the plan with the persist-once rip cord.
- **D6 - Project snapshot decoupling.** `RunTurn` stops mutating the user
  content; the snapshot renders as a frozen turn block. The persisted user
  message stays the bare prompt, so dedup is exact and the prompt appears
  once.
- **D7 - Compaction single rewrite.** `AppendSummary` marks the previous
  summary out-of-context in the same save (the summarizer already merges the
  old summary, `internal/context/summarize.go:40-42`). Every rewrite logs
  direction, tokens before/after, and reason.
- **D8 - Provider verification.** Gemini implicit caching is not assumed: a
  guard warns when a large prompt reports `CachedContentTokenCount == 0`,
  reusing `finops.cache_warn_ratio` (`internal/finops/budget.go:265`), with
  explicit `CachedContent` as the documented escape hatch. OpenAI-compatible
  `prompt_cache_key` only via upstream adk-go support (`ExtraBody` is rejected
  by `adk/v2/model/openaimodel/request.go:442-456`). Anthropic, when it lands,
  ships with top-level automatic `cache_control` or not at all. Keepalive is
  deferred.

## Specs

### PC-001 - Canonical renderer

- One pure render function is used by both live send and replay; the wrap
  policy applies uniformly to text parts and is idempotent.
- For a persisted message, the request uses the render of the stored message.
  The live leading user content is dropped when it matches (reuse
  `CurrentUserMessageMatches`); otherwise keep the live copy and log
  `cache: live user content unmatched`.
- Normalize at ingestion (`internal/session/session_data.go:137`): LF, NFC.
  Attachments: persist a content hash with the ref and log
  `cache: attachment drift` when the re-read hash differs.
- Test: live bytes equal replay bytes across CRLF and NFC/NFD samples; wrap
  idempotence.

### PC-002 - Frozen turn context

- Reserve gates and sources are evaluated once per turn; the payload is stored
  in invocation state; later calls are read-only.
- Lifetime is the ADK invocation. No LRU, no TTL, no capacity tuning.
- Test: a source that increments a counter and returns `time.Now()` is invoked
  exactly once per turn; calls 1..N splice byte-identical blocks.

### PC-003 - Request assembly

- Assembly order per D1/D4. The un-persisted tail is whatever remains in
  `req.Contents` after the matched user content. Tool-protocol content
  (function calls/responses) is never the dedup anchor and an empty stored
  prompt never prefix-matches: the drop can only ever remove a real user
  message, never a gate answer or unrelated live content.
- Steering joins the frozen insertion section (moved from end-append);
  a steering arrival mid-turn is a logged tail rewrite.
- Sidekick notes stay persisted; because assembly renders the store in
  sequence order, a mid-turn note lands after the current user message and
  stays there on every later call and turn. No special-case exclusion.
- Tests: prefix extension across calls and turns; exactly one user content;
  blocks before steering before tool events.

### PC-004 - Context classes and placement

- SessionStart: persist once at store head before the first render.
- Memory, snapshot, prompt hooks, notices: frozen turn blocks.
- Memory tax is documented and measured; rip cord is persist-once +
  apply-next-session if telemetry shows impact.
- Tests: memory present on every call and every turn, never prepended;
  a `remember` write is visible on the next turn.

### PC-005 - Project snapshot

- `RunTurn` enqueues the snapshot as a frozen block; no user-content mutation;
  dedup exact.
- Test: one prompt occurrence in the assembled request; snapshot present and
  fresh each turn.

### PC-006 - Compaction

- Prior summary evicted in the same save as the new one; one rewrite per
  compaction; rewrite log line.
- Tests: replayed history contains exactly one summary; log emitted.

### PC-007 - Telemetry and provider guard

- `cached` is added to `EventSink.OnUsage` (`internal/agentrun/agentrun.go:45`)
  and the SSE payload (`internal/web/sse/bridge.go:254`); per-turn log line
  `cache read X/Y (Z%)`.
- Implicit-cache guard: warn once per session when cached tokens are zero
  while prompt tokens exceed the model minimum (the Flash-class 1024-token
  implicit-cache floor); the message names the endpoint kind and the
  explicit-caching escape hatch.
- Keepalive and `prompt_cache_key` stay deferred/optional.
- Tests: `OnUsage` carries cached; the guard fires on a synthetic zero-cache
  run and not on a warm one.

## Non-goals

- **Tool-transcript persistence.** Cost model: persisted tool output becomes a
  cached read (roughly 10% price) on every later turn and buys cross-turn
  recall. Revisit when telemetry shows repeated re-reads of the same paths.
  Stage A (`internal/context/context.go:638`) stays ready for that decision.
- **Session-handle/delta transport** (`previous_response_id` style). Replay is
  provider-portable, ZDR-friendly, and auditable.
- **Explicit Gemini `cachedContent`** beyond the PC-007 escape hatch. Implicit
  caching plus stable prefixes is the default path.

## Definition of done

- Prefix-stability tests green (across calls and turns, CRLF/NFC, freeze-once,
  single user occurrence, project snapshot).
- With memory and hooks enabled: turn 1 cold, later turns' cached/total stable
  and high (0.8 target in the verification run), and no
  non-compaction rewrites in the log.
- Docs updated: `docs/context-hygiene/spec.md` (request assembly contract),
  `docs/auto-memory/spec.md` D5, `docs/finops/spec.md` FO-005 telemetry note.
- Full suite green: `gofmt -l .`, `go vet ./...`, `GOOS=darwin go build ./...`,
  `go test ./...`, `pnpm --dir webui test`.
