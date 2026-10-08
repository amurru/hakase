# Spec: ADK Adoption — use more of `google.golang.org/adk/v2 v2.4.0`

Governing issue: TBD (Tier-3 proposal, supports all P0). Base: `go.mod:36-37` (`adk/v2 v2.4.0`, `genai v1.71.0`).

**Scope:** replace bespoke runners with ADK primitives where they already exist. Non-goals: cloud backends by default, `adk web` Dev UI, eval harness port (none in Go module).

## Decisions

- **Adopt where ADK is strictly more maintained.** Delegation runner, workflow orchestration, artifacts, plugins, skill toolset, A2A all exist in-module and are unused (grep `agenttool|workflowagents|skilltoolset|remoteagent|PluginConfig|load_memory` in `internal/` = 0 hits, only generic `artifact` words in comments). Cloud backends (`session/database`, `session/vertexai`, `memory/vertexai`, `artifact/gcsartifact`, `telemetry`) stay opt-in only, fail-closed like `DurableADKService` (`internal/session/adk_service.go`).
- **Keep what is genuinely better local.** Custom session JSON + snapshots/rewind, memory notes + hybrid KB retrieval, hooks runner trust model, own compaction cascade, own skill eval + sleep `evalkit` stay (no `bench.json` in repo; "bench" hits are Go `Benchmark` tests + `internal/skill/eval.go`). ADK `session/compaction` (`LLMSummarizer`) and `memory.Service` do not cover pins/hybrid/skill-awareness.

## Specs

### Spec AD-001: `agenttool.New` replaces bespoke delegation

- Wrap 3 sub-agents once (`tool/agenttool.New(agent, &Config{SkipSummarization})`) instead of per-call `llmagent.New` + `runner.New` + `InMemoryService` (`internal/agent/delegate.go:337-433`). Keep gate wrappers + blocked-tools filter (`FilterBlockedTools :591-600`, consumed by `BuildSubAgentTools :629-658`; blocked list `:29-37`), delegation cache, HITL pause-capture (`resume.go`). Re-verify `transfer_to_agent` isolation semantics (prompt `agent.go:1891`).

### Spec AD-002: workflow agents for deterministic pipelines

- `sequentialagent.New([plan, execute, verify])` for coding tasks; `parallelagent` for multi-source research fan-out; `loopagent{maxIterations}` + `exitlooptool` for refine loops; sleep night stages (`internal/sleep/cycle.go`) as Sequential instead of hand-chained steps. Package `agent/workflowagents/*`; `workflow` graph only if branching needed.

### Spec AD-003: `artifact.Service` for `outputs/`

- `artifact.InMemoryService()` in `runner.Config` (`agent.go:2637`); migrate `outputs/media` + `outputs/cron` reports (present today; no `outputs/sleep` dir yet - evidence lands there when the sleep loop ships) to versioned artifacts; add `loadartifactstool.New()`. Path-compat shim for Telegram `CronEvent` + web UI consumers.

### Spec AD-004: `plugin.Plugin` consolidation

- Merge hooks-runner adapters (`agent.go:128-160`), `VisionInjectionCallback`, `ToolResultGuard`, sidekick watcher, audit into ordered plugins via `runner.PluginConfig`. Preserve deny/override order; pin with `hooks_wiring_test.go`, `toolresult_guard_test.go`. API: `plugin.Plugin{Config{Name, Before/AfterModel/Tool/AgentCallbacks}}`.

### Spec AD-005: `SkillToolset` + A2A (deferrable)

- `skilltoolset.New(Config{Source})` backed by existing skill dirs (`internal/skill/skill_discovery.go:38`); keep `skill://` MCP serve for external hosts; A/B via skill eval. A2A: `server/adka2a` executor + agent card alongside web server (`internal/web/server.go` route), `remoteagent.NewA2A` to consume peers; scope read-only tools first with `server/authn|authz`.

## Non-goals

- `server/adkrest`, `launcher` Dev UI (own chi + Vue SPA stays), `memory/vertexai` default (local store stays), eval port (keep skill eval + sleep `evalkit`).

## Definition of done

- [ ] Delegation via `agenttool` with identical gates/cache/HITL behavior.
- [ ] One deterministic pipeline (sleep cycle or plan-execute-verify) on workflow agents.
- [ ] Suite green + `-race` on touched packages.

## Validation against codebase (2026-10-08)

Already uses, do NOT re-add: `llmagent.New` (orchestrator + 3 SubAgents, `agent.go:2570,2637`), `runner.New` (`AppName hakase_harness`, `AutoCreateSession`), `session.InMemoryService` (default) + custom `DurableADKService` opt-in (`session/adk_service.go`, validated vs ADK `sessiontestsuite`), `functiontool`/`Toolset`/`mcptoolset` (all tools; `mcptoolset` at `internal/mcp/mcp_servers.go:573,600`; `websearch_fallback.go` implements `Toolset`), raw Before/After callbacks.
Note: "do NOT re-add" means do not add a second function doing the same thing (NO SLOP). Extending or enhancing the listed paths in place is fine and expected.
Confirmed unused (0 code hits): `tool/agenttool`, `agent/workflowagents/*`, `artifact.Service` + `loadartifactstool`, `tool/skilltoolset`, `agent/remoteagent` + `server/adka2a`, `plugin.Plugin` + `runner.PluginConfig`, `memory.Service` + `loadmemorytool`/`preloadmemorytool`, `session/database|compaction|vertexai`, `memory/vertexai`, `telemetry` setup (own `internal/tracing` instead), `server/adkrest|adka2a|agentengine`, `launcher`.
