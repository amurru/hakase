# Tasks: Prompt-Cache Stability

Spec: [spec.md](spec.md) - plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 - canonical renderer + frozen turn context + assembly (PR A)

- [ ] **T1.1 [BE]** split `BeforeModelCallback` into `beginTurn` (render once, store `temp:hakase.frozen_tail` via `ctx.State()`) and read-only `assemble` (`internal/context/context.go:301`). Spec: PC-002.
- [ ] **T1.2 [BE]** remove deferred head prepends (`context.go:363-373`); move notices (`:406-420`) and steering (`:427-431`) into the frozen insert; sidekick to persist-only (`:436-449`). Spec: PC-003/004.
- [ ] **T1.3 [BE]** request assembly per D4: store in sequence order, drop matched live user content, append frozen blocks + steering, keep ADK tail. Spec: PC-003.
- [ ] **T1.4 [BE]** shared wrap function for live and replay; ingestion normalization (LF, NFC) in `internal/session/session_data.go:137`; attachment hash + drift log. Spec: PC-001.
- [ ] **T1.5 [QA]** prefix extension across calls and turns; freeze-once with a counter/`time.Now()` source; live-vs-replay bytes across CRLF/NFC; exactly one user content. Spec: PC-001/002/003.

## Phase 2 - context classes + project snapshot (PR B)

- [ ] **T2.1 [BE]** SessionStart persist-once: insert at store head before the first render of a session. Spec: PC-004.
- [ ] **T2.2 [BE]** `internal/agentrun/agentrun.go:246-252`: snapshot becomes a frozen turn block; no user-content mutation. Spec: PC-005.
- [ ] **T2.3 [QA]** update `memory_test.go` / `sessionstart_test.go` (blocks after user content, never prepended; `remember` visible next turn); snapshot single occurrence + freshness. Spec: PC-004/005.
- [ ] **T2.4 [DOCS]** rewrite `docs/auto-memory/spec.md` D5 (frozen turn block, not one-shot head injection). Spec: D5.

## Phase 3 - compaction + telemetry (PR C)

- [ ] **T3.1 [BE]** `AppendSummary` evicts the prior summary in the same save (`internal/session/session_service.go:212-227`). Spec: PC-006.
- [ ] **T3.2 [BE]** one rewrite log line in `fitToBudget` (`context.go:563-620`) and summary landing: direction, tokens before/after, reason. Spec: PC-006.
- [ ] **T3.3 [BE]** `cached` in `EventSink.OnUsage` + SSE payload + webui chip; per-turn `cache read X/Y (Z%)` log; implicit-cache guard (reuse `cache_warn_ratio`, once per session, endpoint kind + escape hatch). Spec: PC-007.
- [ ] **T3.4 [QA]** one-summary replay test; rewrite log test; `OnUsage` carries cached; guard fires on synthetic zero-cache run only. Spec: PC-006/007.
- [ ] **T3.5 [DOCS]** request-assembly contract in `docs/context-hygiene/spec.md`; FO-005 telemetry note in `docs/finops/spec.md`; ROADMAP status update. Spec: DoD.

## Phase 4 - optional provider knobs

- [ ] **T4.1 [BE]** `prompt_cache_key` for OpenAI-compatible endpoints via upstream adk-go support only. Spec: PC-007.
- [ ] **T4.2 [BE]** keepalive spike, default off, interactive surfaces only (Aider pattern). Spec: PC-007.

## Final gates

- [ ] **T5.1 [QA]** manual 6-turn scenario with memory + hooks at `cache_warn_ratio: 0.8`: turn 1 cold, later turns above threshold, only compaction/steering rewrites logged. Spec: DoD.
- [ ] **T5.2 [QA]** full suite: `make build-frontend`, `gofmt -l .`, `go vet ./...`, `GOOS=darwin go build ./...`, `go test ./...`, `pnpm --dir webui test`.
