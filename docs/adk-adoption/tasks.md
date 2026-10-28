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

- [x] **T2.1 [BE]** artifact service + `loadartifactstool` + `outputs/` migration shim; `loadartifactstool` pre-dispatch policy (v2 `artifact` rule family, fail-closed ask, `RedactSecrets` on content); writes stay system-initiated. Spec: AD-003 security constraints.
- [x] **T2.2 [BE]** plugin consolidation — **evaluated and rejected** (2026-10-28): `runner.Run` overwrites the inherited plugin manager and `agenttool` builds a plugin-less runner, so a plugin-only chain would skip every delegated sub-agent. Security callbacks stay on the agents as the single mechanism. Spec: AD-004 decision + evidence.
- [x] **T2.3 [QA]** chain order pinned by behavior (`chain_order_test.go`): hooks deny before the flow gate, flow deny under a permissive hook, C1 (never a result override on `runErr`) and error redaction under v2. `-race` green on touched packages. Spec: AD-004.

## Phase 3 — skills + A2A (deferrable)

- [x] **T3.1 [BE]** `SkillToolset` pilot (`skills_toolset`, off by default) behind existing discovery via a `markdownSkillSource` adapter; replaces list/load tools on `code_interpreter` (search/save stay), resource access confined to the skill dir with `..` refused, trust tier pinned in the injected instruction and by test. Skill-eval A/B deferred with the pilot: the eval harness scores skill quality against recorded sets, not toolset mechanics, so the flag keeps the A/B a config flip rather than a code path. Spec: AD-005 security constraints.
- [ ] **T3.2 [BE]** A2A executor + card, read-only first; peer results wrapped as untrusted data (injection policy), never widen the local session label; A2A egress evaluated by flow gates under a v2 policy. Spec: AD-005 security constraints. **Deferred 2026-10-28 -> #151** (network-exposing surface, own design pass).
- [ ] **T3.3 [DOCS]** CHANGELOG + DEVELOPMENT.md ADK surface table.
- [x] **T3.4 [QA]** Full suite green: `gofmt -l` clean, `go vet ./...` clean, `GOOS=darwin go build ./...` ok, `go test ./...` green, `-race` green on agent/config/media, `cd webui && pnpm test` 132/132 (2026-10-28).
