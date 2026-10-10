# Tasks: ADK Adoption

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — delegation + workflows

- [ ] **T1.1 [BE]** `agenttool.New` wrapper in `delegate.go`; keep gates/filter/cache/HITL. Spec: AD-001.
- [ ] **T1.2 [QA]** delegation parity (isolation, blocked-tools, pause re-capture). Spec: AD-001.
- [ ] **T1.3 [BE]** one workflow pilot (sequential plan→execute→verify or sleep cycle). Spec: AD-002.

## Phase 2 — artifacts + plugins

- [ ] **T2.1 [BE]** artifact service + `loadartifactstool` + `outputs/` migration shim. Spec: AD-003.
- [ ] **T2.2 [BE]** plugin consolidation (`plugin.Plugin`, `PluginConfig`). Spec: AD-004.
- [ ] **T2.3 [QA]** order-preservation + `-race` on touched packages. Spec: AD-004.

## Phase 3 — skills + A2A (deferrable)

- [ ] **T3.1 [BE]** `SkillToolset` pilot + skill-eval A/B. Spec: AD-005.
- [ ] **T3.2 [BE]** A2A executor + card, read-only first. Spec: AD-005.
- [ ] **T3.3 [DOCS]** CHANGELOG + DEVELOPMENT.md ADK surface table.
- [ ] **T3.4 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
