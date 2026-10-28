# Execution Plan: ADK Adoption

Spec: [spec.md](spec.md). Value-ordered, each independently shippable.

## Phases

### Phase 1 — delegation + workflows (AD-001/002)

1. `internal/agent/delegate.go`: run delegation through `agenttool.New` (done, #145); keep `BuildSubAgentTools` + `FilterBlockedTools` (`delegate.go:990,1028`; blocked `:30`) + cache + HITL (`resume.go`).
2. One workflow pilot: `sequentialagent` plan→execute→verify or sleep `cycle.go` Sequential; `parallelagent` research fan-out where measured.
3. Workflow label contract (T1.4): pin that in-runner workflow children inherit registry label resolution through the shared invocation ctx and fold narrowings into the same session; out-of-runner pipelines (sleep) seed an explicit label via `WithDelegateFlowState` (StartLabel v1 parity, never unregistered-fail-closed); escalation path defined.

### Phase 2 — artifacts + plugins (AD-003/004)

4. `runner.Config` (`agent.go:3019`) + `artifact.InMemoryService()`; migrate `internal/media/tools.go`, `internal/cli/cronjob.go`, `internal/sleep/*`; add `loadartifactstool` with the pre-dispatch policy (v2 `artifact` rule family, fail-closed ask, `RedactSecrets` on content; writes stay system-initiated — spec AD-003 security constraints).
5. Consolidate callbacks into `plugin.Plugin` (`agent.go:126-209` + delegateRunState); preserve order with existing wiring tests **and** a dedicated chain-order test covering flow ask -> hooks deny -> redaction -> guard, including the C1 rule (never a non-nil result override on `runErr`).

### Phase 3 — skills + A2A (AD-005, deferrable)

6. `SkillToolset` behind existing discovery; skill content pinned as semi-trusted instructions that cannot grant tool authority past a gate; A/B via skill eval.
7. `internal/a2a/` + web route; read-only tools first; peer results wrapped as untrusted data (injection policy) and never widen the local session label; A2A egress evaluated by flow gates like other network paths.

## Critical path

1 → 2 → 3 sequential (delegation before workflows share session semantics; label contract before the pipeline scales). 4, 5 independent. 6, 7 deferred.

## Verification baseline

- Delegation parity: gates fire, blocked tools filtered, cache hits, HITL pauses re-captured (done, #145).
- Workflow pilot: deterministic order, exit-loop bounded, escalation path defined, **label tests green under a v2 policy (in-runner inheritance + out-of-runner explicit seed)**.
- Artifacts: versioned load/list via model tool; old `outputs/` paths still resolve; **load gated under the session label (fail-closed ask without a rule), redacted content, no model-facing save tool**.
- Plugins: **chain-order test proves flow gates and C1 survive consolidation**; existing wiring tests green.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `-race` on agent/delegate/sleep.

## Risk register

- **Session-isolation change**: `agenttool` shares runner session semantics vs per-call `InMemoryService`; re-verify with resume tests. **State copy**: parent ADK session state reaches the child session; pinned empty by T1.5, re-check on any new session-state writer.
- **Over-determinism**: workflows trade flexibility for reliability; pilot on narrow pipeline first.
- **Callback migration**: signature drift fails at compile (pinned v2.6.0); order regressions caught by wiring tests **plus the chain-order test — plugin callbacks precede agent callbacks and short-circuit them (ADK v2.6.0 `base_flow.go`), so a misplaced plugin can bypass a gate**.
- **Unlabeled channels**: artifacts and A2A peer results are new content paths the flow model does not natively label; each lands with an explicit gate/redaction policy (spec AD-003/AD-005) or not at all.
- **Cloud backends**: credentials/cost/exfiltration; opt-in + fail-closed construction only.
