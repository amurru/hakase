# Tasks: FinOps

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[FE]` frontend, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — capture

- [x] **T1.1 [BE]** full `UsageRecord` in `agentrun.go:192,300-306,361-382` + per-tool deltas at `:319-330`. Spec: FO-001.
- [x] **T1.2 [BE]** same in `tui/ui.go:2252,2278-2280` + `model_call.go` reason labels. Spec: FO-001.
- [x] **T1.3 [QA]** capture tests: cached/thoughts/tool-use preserved, invisible paths labeled. Spec: FO-001.

## Phase 2 — pricing + ledger

- [x] **T2.1 [BE]** price table + tiers + override in `modelinfo.go`/`provider.go`. Spec: FO-002.
- [x] **T2.2 [BE]** ledger write + aggregate reader (`session_data.go`, `cli/session.go` pattern). Spec: FO-003.
- [x] **T2.3 [BE]** `finops` config block + env + Validate. Spec: FO-003.
- [x] **T2.4 [QA]** pricing fixtures (tiered, unknown, zero-cost re-estimate). Spec: FO-002/003.

## Phase 3 — enforcement + OTel

- [x] **T3.1 [BE]** pre-turn budget check + counters `~/.hakase/` flock. Spec: FO-003.
- [x] **T3.2 [BE]** OTel usage/cost attrs on `Run.End()`. Spec: FO-005 (OTel part).
- [x] **T3.3 [QA]** budget e2e (warn/block/reset), chain with audit decision. Spec: FO-003.

## Phase 4 — surfaces

- [x] **T4.1 [BE+FE]** SSE v2 + TUI $/budget bar + webui meter + `GET /api/stats`. Spec: FO-004.
- [x] **T4.2 [BE]** `hakase stats` CLI (`stats.go` + `registerCommand` in `init()`). Spec: FO-004.
- [x] **T4.3 [BE]** cache-ratio guard. Spec: FO-005.
- [x] **T4.4 [DOCS]** CHANGELOG + README/docs + ROADMAP Tier-3 tick.
- [x] **T4.5 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
