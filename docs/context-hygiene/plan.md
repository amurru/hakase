# Execution Plan: Context Hygiene

Spec: [spec.md](spec.md). Threshold-gated, byte-identical under threshold.

## Phases

### Phase 1 — skill search path (CX-001)

1. `internal/agent/agent.go:1109`: clamp + stub threshold; sibling `search_skills` tool next to `:1215`; register at `:2364,2514`.
2. Update `skills_index_size_test.go` ceilings in lockstep.
3. Discovery lint in `internal/skill/skill_discovery.go:38`, `skills_md.go:40`.

### Phase 2 — KB + caps + pins (CX-002/003)

4. `internal/knowledge/knowledge_tools.go:262,290,913` (`snippetWindow :905`): result/byte caps + marker.
5. Persist-time tool-output cap + durable-pin bin in `internal/context/context.go:532,627` + `internal/context/summarize.go:65`; reserve accounting `:84-87,554-555`; eviction log.

### Phase 3 — config + docs

6. `internal/config` flags (default-off precedent) + `config.json.example`.
7. tasks.md ticked; CHANGELOG; README/docs mention.

## Critical path

1 → 4 → 5 sequential. 3, 6 independent but before wiring. Tests ride each phase.

## Verification baseline

- Under-threshold byte-identical prompt on the repo skill tree (23 own, ~148 with editor/agent dirs); over-threshold stub + search relevance.
- KB cap marker; pin survival reconstruction; prefix byte-stable across turns.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **Extra round-trip over threshold**: bounded limit=5, full description in search avoids double-load.
- **Clamp hurts routing**: full description returned by search/load; lint nudges authors to front-load verbs.
- **Pin bloat**: pins counted in reserve; overflow falls back to summarizer with log.
