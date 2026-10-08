# Execution Plan: FinOps

Spec: [spec.md](spec.md). Capture-first, then price, meter, enforce.

## Phases

### Phase 1 — capture (FO-001)

1. `internal/agentrun/agentrun.go`: expand `lastUsage` to full struct at `:192,300-306`; same in `persistAgentResponse :361-382`; attach per-tool deltas at `:319-330`.
2. `internal/tui/ui.go:2252,2278-2280` (store full struct); truncation lives downstream at `:1936-1948` + `:1313-1316`; forward at end via `UsageUpdateMsg`.
3. `internal/agent/model_call.go:34-45,62-75`: capture with `reason` label (summarize/hyde/sleep/sidekick).

### Phase 2 — pricing + ledger (FO-002/003)

4. New static price table + tier thresholds + unknown-model policy, consulted alongside `internal/agent/modelinfo.go:14-37` + `provider.go` (capability fetch only today).
5. `internal/session/session_data.go:55-68`: extend or sidecar `sessions/<id>.usage.jsonl`; aggregate reader (reuse `internal/cli/session.go` store pattern).
6. `internal/config`: `finops` block + env overrides + `Validate` in `LoadConfig` tail.

### Phase 3 — enforcement + OTel

7. Pre-turn budget check in `Driver.RunTurn` (`agentrun.go:275-340`); counters `~/.hakase/` 0600 flock.
8. `internal/tracing/runspan.go:127-141`: usage + cost attrs on End.

### Phase 4 — surfaces

9. SSE `sse/bridge.go:252-259` v2 payload; TUI bar `ui.go:1936-1983`; webui `app.ts`, `useSSE.ts`, `views/Layout.vue`, `useNotifications.ts`; `GET /api/stats` (new route next to chat handlers).
10. `internal/cli/stats.go` + registration in `command.go:129-215`.
11. tasks.md ticked; CHANGELOG; README/docs mention.

## Critical path

1 → 4 → 5 → 7 → 9 is sequential. 6 independent but before 7. 8, 10 need 1-5.

## Verification baseline

- Golden ledger fixtures (tiered pricing, unknown model, zero-cost re-estimate flag).
- Budget-block e2e (warn vs block, reset semantics).
- Cache-ratio warning fires on synthetic low-cache turn.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **Price drift**: static table rots; mitigated by config override + unknown-model warn + documented source.
- **Turn abort vs in-flight spend**: pre-turn check only; mid-turn overrun reported, not killed (no tool-kill mid-call).
- **SSE payload compat**: v2 fields additive; old webui ignores extras.
- **ADK span duplication**: hakase attrs pinned `hakase.*` + verbatim `gen_ai.*` only per `runspan.go:20-24` guidance.
