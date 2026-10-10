# Tasks: MCP Gateway

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — registry + install

- [x] **T1.1 [BE]** registry client (`registry.go`) + `mcp search`. Spec: MG-001.
- [x] **T1.2 [BE]** install flow (`install.go`) + MCPB local + `skill install`; CLI wiring. Spec: MG-002.
- [x] **T1.3 [QA]** mock-registry search/install + credential-plan (no secrets persisted). Spec: MG-002.

## Phase 2 — audit + tokens + shadow

- [x] **T2.1 [BE]** `mcp audit` 7 checks + baseline file. Spec: MG-003/005.
- [x] **T2.2 [BE]** scope picker + `mcp logout` + elicitation audit log. Spec: MG-004.
- [x] **T2.3 [QA]** poisoned fixtures FAIL; shadow drift; auth posture matrix. Spec: MG-003.

## Phase 3 — gateway + budget

- [x] **T3.1 [BE]** gateway toolset + auto-degrade + sub-agent default. Spec: MG-006.
- [x] **T3.2 [QA]** over-budget e2e (flat → gateway), hot-tools passthrough. Spec: MG-006.
- [x] **T3.3 [DOCS]** CHANGELOG + README/docs + ROADMAP tick.
- [x] **T3.4 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
