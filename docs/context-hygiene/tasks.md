# Tasks: Context Hygiene

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — skill search path

- [ ] **T1.1 [BE]** clamp + stub threshold in `getSkillsPrompt` (`agent.go:1109`); update `skills_index_size_test.go`. Spec: CX-001.
- [ ] **T1.2 [BE]** `search_skills` tool (keyword over name+description, bounded 5-10, disabled-mirroring, re-scan on miss). Spec: CX-001.
- [ ] **T1.3 [BE]** discovery lint (description quality, body lines, `references/`). Spec: CX-003 lint part.
- [ ] **T1.4 [QA]** index overhead ceiling, stub threshold, search relevance. Spec: CX-001.

## Phase 2 — KB + caps + pins

- [ ] **T2.1 [BE]** KB caps (`knowledge_tools.go:262,290,906`). Spec: CX-002.
- [ ] **T2.2 [BE]** tool-output cap persist-time + durable-pin bin + reserve accounting + eviction log. Spec: CX-003.
- [ ] **T2.3 [QA]** KB cap, pin survival reconstruction, cache-stability (byte-identical prefix). Spec: CX-002/003.

## Phase 3 — config + docs

- [ ] **T3.1 [BE]** config flags default-off + example. Spec: CX-004.
- [ ] **T3.2 [DOCS]** CHANGELOG + README/docs + ROADMAP tick.
- [ ] **T3.3 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
