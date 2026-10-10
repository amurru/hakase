# Execution Plan: Prompt-Cache Stability

Spec: [spec.md](spec.md). One invariant, four PRs, no schema changes.
Invariant P: `[rendered store][frozen turn blocks][steering][un-persisted run
tail]`, append-only between logged compactions.

## Phases

### Phase 1 - canonical renderer + frozen turn context + assembly (PC-001/002/003)

1. `internal/context/context.go:301` `BeforeModelCallback`: split into
   `beginTurn(ctx)` (evaluate reserve gates, render sources once,
   `ctx.State().Set("temp:hakase.frozen_tail", payload)`) and `assemble(ctx,
   req)` (read-only splice). Render functions become unreachable from
   `assemble`.
2. Remove the deferred head prepends (`context.go:363-373`); route memory,
   SessionStart, and prompt-hook blocks through the frozen payload.
3. Move notices (`context.go:406-420`) and steering (`context.go:427-431`)
   into the frozen insertion section; switch sidekick (`context.go:436-449`)
   to persist-only.
4. Assembly per D4: render store in sequence order, drop the matched live
   leading user content, append frozen blocks + steering, keep ADK's tail.
5. Canonical renderer: shared wrap function for live and replay; ingestion
   normalization (LF, NFC) in `internal/session/session_data.go:137`;
   attachment hash + drift log.
6. Tests: prefix extension across calls and turns; freeze-once with a
   side-effectful source; live-vs-replay bytes across CRLF/NFC; single user
   occurrence.

### Phase 2 - context classes + project snapshot (PC-004/005)

7. SessionStart persist-once path: insert at store head on the first callback
   of a session, before the first render.
8. `internal/agentrun/agentrun.go:246-252`: stop mutating the user content;
   enqueue the snapshot as a frozen turn block (mirror the
   `PendingQueue`/`SidekickNoteQueue` pattern, `context.go:126-135`).
9. Update `internal/context/memory_test.go`, `sessionstart_test.go`: blocks
   after the user content on every call, never prepended, memory updates
   visible next turn.
10. New test: project snapshot single occurrence and freshness per turn.

### Phase 3 - compaction + telemetry (PC-006/007)

11. `internal/session/session_service.go:212-227`: evict the prior summary
    (`InContext=false`) in the same save as the new one.
12. `fitToBudget` (`context.go:563-620`) and `AppendSummary` log one rewrite
    line: direction, tokens before/after, reason.
13. Add `cached` to `EventSink.OnUsage` (`agentrun.go:45`, call site `:335`),
    `bridgeSink` (`internal/web/handlers/chat.go:665` area), the channel sink,
    `EventBridge.SendUsage` (`internal/web/sse/bridge.go:254`), and the webui
    usage chip.
14. Per-turn log `cache read X/Y (Z%)` from `finops.UsageRecord.Cached`; the
    implicit-cache guard reuses `cache_warn_ratio` and fires once per session
    with the endpoint kind and escape hatch in the message.

### Phase 4 - optional provider knobs (PC-007 tail)

15. `prompt_cache_key` for OpenAI-compatible endpoints only via an upstream
    adk-go issue/PR (`ExtraBody` is rejected today,
    `adk/v2/model/openaimodel/request.go:442-456`).
16. Keepalive spike (Aider pattern, default off): reuse the last frozen
    request contents, `max_tokens=1`, only for interactive surfaces.

## Critical path

Phase 1 -> 2 -> 3. Phase 4 is independent and opt-in.

## File map

| Area | Files |
|---|---|
| Assembly + freeze | `internal/context/context.go`, `internal/context/instruction_context.go` |
| Compaction | `internal/context/summarize.go`, `internal/session/session_service.go` |
| Ingest + store | `internal/session/session_data.go` |
| Runs | `internal/agentrun/agentrun.go` |
| Telemetry | `internal/finops/budget.go`, `internal/web/sse/bridge.go`, `internal/web/handlers/*`, `webui/src` usage chip |
| Docs | `docs/context-hygiene/spec.md`, `docs/auto-memory/spec.md` D5, `docs/finops/spec.md` FO-005 |

## Verification baseline

- New tests: prefix-stability (calls, turns), freeze-once, CRLF/NFC byte
  equality, project snapshot single occurrence, one-summary replay.
- Manual scenario: scripted 6-turn session with memory and hooks enabled,
  `finops.enabled` + `cache_warn_ratio: 0.8`; turn 1 cold, later turns above
  threshold; grep the log for rewrite lines - only compaction (and a steering
  arrival, if any) may appear.
- Full suite per house rule: `make build-frontend`, `gofmt -l .`,
  `go vet ./...`, `GOOS=darwin go build ./...`, `go test ./...`,
  `pnpm --dir webui test`.

## Risk register

- **R1 mid-turn mutation** (review catch): solved structurally by PC-002;
  freeze-once test with a counter/time source guards it.
- **R2 byte drift between live and replay** (review catch): solved by
  render-from-store plus ingestion normalization; CRLF/NFC tests.
- **R3 freeze lifetime/eviction** (review catch): solved by ADK invocation
  state (`temp:`); no LRU or capacity tuning exists to get wrong.
- **R4 Gemini endpoint variance**: implicit caching varies by tier (AI Studio
  key, Vertex model/region, Code Assist OAuth); PC-007 guard plus explicit
  `CachedContent` escape hatch.
- **R5 memory token tax**: bounded at 4000 chars/turn; rip cord is persist-once
  with apply-next-session (Claude Code CLAUDE.md precedent).
- **R6 attachment drift**: re-read from disk at replay can change bytes;
  content hash + log; text snapshot at first send if it ever matters.
- **R7 tool transcript omission** (review note): accepted non-goal; cost model
  and revisit trigger recorded in the spec.

## Rollout

- Branch off `develop`; one logical change per PR (Phases 1-3 = PRs A-C;
  Phase 4 separate and optional).
- Tick `tasks.md` boxes in the same PR that lands each item (house rule).
- Update the `docs/prompt-cache/` docs, the three cross-referenced docs, and
  the ROADMAP entry in the same arc.
