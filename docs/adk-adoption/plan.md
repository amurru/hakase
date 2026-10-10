# Execution Plan: ADK Adoption

Spec: [spec.md](spec.md). Value-ordered, each independently shippable.

## Phases

### Phase 1 — delegation + workflows (AD-001/002)

1. `internal/agent/delegate.go:337-433`: wrap sub-agents with `agenttool.New` once; keep `BuildSubAgentTools` + `FilterBlockedTools` (`:591-600`, consumed at `:629-658`; blocked `:29-37`) + cache + HITL (`resume.go`).
2. One workflow pilot: `sequentialagent` plan→execute→verify or sleep `cycle.go` Sequential; `parallelagent` research fan-out where measured.

### Phase 2 — artifacts + plugins (AD-003/004)

3. `runner.Config` (`agent.go:2637`) + `artifact.InMemoryService()`; migrate `internal/media/tools.go`, `internal/cli/cronjob.go`, `internal/sleep/*`; add `loadartifactstool`.
4. Consolidate callbacks into `plugin.Plugin` (`agent.go:123-215,2591-2598`); preserve order with existing wiring tests.

### Phase 3 — skills + A2A (AD-005, deferrable)

5. `SkillToolset` behind existing discovery; A/B via skill eval.
6. `internal/a2a/` + web route; read-only tools first.

## Critical path

1 → 2 sequential (delegation before workflows share session semantics). 3, 4 independent. 5, 6 deferred.

## Verification baseline

- Delegation parity: gates fire, blocked tools filtered, cache hits, HITL pauses re-captured.
- Workflow pilot: deterministic order, exit-loop bounded, escalation path defined.
- Artifacts: versioned load/list via model tool; old `outputs/` paths still resolve.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `-race` on agent/delegate/sleep.

## Risk register

- **Session-isolation change**: `agenttool` shares runner session semantics vs per-call `InMemoryService`; re-verify with resume tests.
- **Over-determinism**: workflows trade flexibility for reliability; pilot on narrow pipeline first.
- **Callback migration**: signature drift fails at compile (pinned v2.4.0); order regressions caught by wiring tests.
- **Cloud backends**: credentials/cost/exfiltration; opt-in + fail-closed construction only.
