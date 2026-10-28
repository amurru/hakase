# Tasks: ADK Adoption

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs. Security constraints cited per task come from the spec's "Security considerations (2026-10-28)" section and the per-AD constraint bullets — they are acceptance criteria, not suggestions.

## Phase 1 — delegation + workflows

- [x] **T1.1 [BE]** `agenttool.New` wrapper in `delegate.go`; keep gates/filter/cache/HITL. Spec: AD-001.
- [x] **T1.2 [QA]** delegation parity (isolation, blocked-tools, pause re-capture). Spec: AD-001.
- [x] **T1.3 [BE]** one workflow pilot (sequential plan→execute→verify or sleep cycle). Spec: AD-002.
- [x] **T1.4 [QA]** workflow label contract: under a v2 policy, in-runner workflow children resolve the registry label through the shared invocation ctx and fold narrowings into the same session; out-of-runner pipelines (sleep) seed an explicit label via `WithDelegateFlowState` (StartLabel v1 parity, never unregistered-fail-closed). Spec: AD-002 security constraints.
- [x] **T1.5 [QA]** session-state copy review: test that agenttool children inherit no hakase-written ADK session state; add a review note binding any future session-state writer to re-check the copy path. Spec: AD-001 / Security considerations.

## Phase 2 — artifacts + plugins

- [ ] **T2.1 [BE]** artifact service + `loadartifactstool` + `outputs/` migration shim; `loadartifactstool` pre-dispatch policy (v2 `artifact` rule family, fail-closed ask, `RedactSecrets` on content); writes stay system-initiated. Spec: AD-003 security constraints.
- [ ] **T2.2 [BE]** plugin consolidation (`plugin.Plugin`, `PluginConfig`); flow gates (`CheckPreFlowAsk`/`CheckPostFlow`), the C1 redaction rule, and the AD-001 run-state callbacks land as ordered plugins (plugin callbacks precede agent callbacks and short-circuit on non-nil). Spec: AD-004 security constraints.
- [ ] **T2.3 [QA]** order-preservation + `-race` on touched packages: chain-order test proves flow ask -> hooks deny -> redaction -> guard survives consolidation, including C1 on `runErr != nil`. Spec: AD-004 security constraints.

## Phase 3 — skills + A2A (deferrable)

- [ ] **T3.1 [BE]** `SkillToolset` pilot + skill-eval A/B; skill content pinned as semi-trusted instructions that cannot grant tool authority past a gate. Spec: AD-005 security constraints.
- [ ] **T3.2 [BE]** A2A executor + card, read-only first; peer results wrapped as untrusted data (injection policy), never widen the local session label; A2A egress evaluated by flow gates under a v2 policy. Spec: AD-005 security constraints.
- [ ] **T3.3 [DOCS]** CHANGELOG + DEVELOPMENT.md ADK surface table.
- [ ] **T3.4 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
