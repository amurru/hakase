# Tasks: Permissions Policy

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[FE]` frontend, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — engine

- [ ] **T1.1 [BE]** triple engine + eval precedence (`internal/permissions/` or `gate.go`). Spec: PM-001/002.
- [ ] **T1.2 [BE]** wiring into `EvaluateCommand`, fileops, path audit; `approval.mode` honor. Spec: PM-002.
- [ ] **T1.3 [QA]** precedence matrix + no-match default + multi-resource deny-wins. Spec: PM-002.

## Phase 2 — layering

- [ ] **T2.1 [BE]** user/project/enterprise loader + trust gate + URL sync + locks. Spec: PM-001.
- [ ] **T2.2 [BE]** config plumbing + Validate. Spec: PM-001.
- [ ] **T2.3 [QA]** layering/lock matrix + trust-lapse + non-widening pin. Spec: PM-001.

## Phase 3 — queue + audit

- [ ] **T3.1 [BE]** pending endpoint + RBAC + batch respond (first-wins preserved). Spec: PM-003.
- [ ] **T3.2 [BE]** hash chain fields + `hakase audit export/verify` + SIEM forward. Spec: PM-004.
- [ ] **T3.3 [QA]** queue e2e + chain tamper + rotation compat. Spec: PM-003/004.
- [ ] **T3.4 [DOCS]** `docs/permissions/` guide + CHANGELOG + README.
- [ ] **T3.5 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
