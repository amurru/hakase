# Spec: FinOps — live cost, attribution, budgets, `hakase stats`

Governing issue: TBD (Tier-3 proposal, P0-1). Ecosystem reference: opencode stats/ccusage, Langfuse cost_details, OTel GenAI semconv, gateway budget blocking (Requesty/Maxim/ACP).

**Scope:** capture full usage, price it, persist a ledger, enforce budgets, surface live meter + CLI. Non-goals: multi-tenant billing, gateway proxy, plan-window subscriptions.

## Decisions

- **Fix capture truncation first.** `internal/agentrun/agentrun.go:300-306` collapses `UsageMetadata` to one int and drops cached/thoughts/tool-use. Same collapse in `persistAgentResponse` (`agentrun.go:361-382`) and downstream in TUI (usage-percent `internal/tui/ui.go:1936-1948` + persist `ui.go:1313-1316`; `ui.go:2278-2280` stores the full struct, truncation happens when computing percent/persisting). `internal/agent/model_call.go:34-45` ignores usage entirely (summarize/HyDE/sleep/sidekick invisible). All three get a full struct before any pricing work.
- **Ledger is local JSONL, metadata-only.** Per-turn rows in `sessions/<id>.usage.jsonl` or extended `Message`, aggregated by scanning `sessions/*.json` (reuse `internal/cli/session.go:72-88` pattern). Never prompt text. Rotation 5MBx5 like audit (`internal/agent/audit.go:39-42,89-98`).
- **Pricing table + override.** New static LiteLLM-style $/1M in/out/cached table with tiered >200K, joined on model name alongside capability fetch in `internal/agent/modelinfo.go:14-37` (fetch only today, no pricing). Unknown models = warn + tokens-only.
- **Budgets enforce at turn boundary.** Pre-turn check in `Driver.RunTurn` (`agentrun.go:275-340`) following the `AuditHookBlock` pattern (`audit.go:106-115`, `Decision: hook_blocked`) for a new budget-denial entry shape. `enforce: warn|block`, scopes session/daily/weekly/monthly. Counters in `~/.hakase/` 0600 flock (channels.json precedent).
- **OTel additive.** New attrs on `hakase.run` End (`internal/tracing/runspan.go:127-141`): `gen_ai.usage.input_tokens/output_tokens`, cache read, `cost.usd`. Langfuse `.../otel` endpoint already supported (`internal/tracing/tracing.go:89-91`).

## Specs

### Spec FO-001: capture full usage

- New `UsageRecord{prompt,candidates,cached,thoughts,toolUse,total,model,provider,reason}` built at `agentrun.go:192,300-306`, TUI `ui.go:2252,2278-2280` (store; truncation downstream at `:1936-1948,:1313-1316`), and `model_call.go:34-45,62-75` (label `reason=summarize|hyde|sleep|sidekick`).
- Per-tool deltas attached at `graph.toolStart/toolEnd` (`agentrun.go:319-330`); sub-agent splits via `internal/agent/delegate.go` + `graph.observeEvent:452-480`.

### Spec FO-002: pricing

- Price table (new) consulted alongside `modelinfo.go` + `provider.go:55-74,183-244` (capability fetch only today, no pricing). Config override `finops.prices.overrides`. Tiered thresholds explicit. Reasoning tokens billed as output (Langfuse parity).

### Spec FO-003: ledger + config

```jsonc
"finops": {"enabled": true, "currency": "USD",
  "prices": {"overrides": {"gemini-2.5-flash": {"input_per_1m": 0.30, "output_per_1m": 2.50, "cached_per_1m": 0.075}}},
  "budgets": {"daily_usd": 5.0, "weekly_usd": 20.0, "monthly_usd": 50.0, "per_session_usd": 2.0,
    "enforce": "warn", "cache_warn_ratio": 0.5},
  "ledger": {"path": "~/.hakase/usage.jsonl", "per_tool": true}}
```

Env: `HAKASE_BUDGET_DAILY_USD`, `HAKASE_FINOPS_ENFORCE`. Follows the `hybrid_search` default-off precedent (`config.json.example:49`; `search_expansion` lives only in `config.go:218`).

### Spec FO-004: surfaces

- SSE `internal/web/sse/bridge.go:252-259` `SendUsage` extended `{tokens,percent,cost_usd,budget_pct}` back-compat. TUI `ui.go:1936-1983` adds $ + budget bar. Webui `stores/app.ts:27-33`, `useSSE.ts:161-173`, `views/Layout.vue:170-193`, `useNotifications.ts:61-76` adds cost meter + toast. New `GET /api/stats` next to the chat API handlers (`handlers/chat.go`, e.g. canvas backfill `GetGraph:683-694` - new route, not an extension of it).
- CLI `hakase stats [--format table|json] [--since] [--by model|session|tool|day]`, `hakase stats session <id>`, `hakase stats budgets`. New `internal/cli/stats.go` with `init()` + `registerCommand` (mirror `internal/cli/session.go`, `internal/cli/skill.go` pattern). JSON emits Langfuse-compatible `usage_details/cost_details`.

### Spec FO-005: prompt-cache guard

- Per-turn `cached/total` ratio; warn below `cache_warn_ratio`. Addresses OpenClaw date-field cache-buster class. `buildTimeReminder` day-keyed pattern (`agent.go:349`, day key `agent.go:311,375`) is the template: no per-request-unique bytes in prefix.

## Non-goals

- Gateway metering proxy, team/project attribution beyond session, subscription plan windows, auto model-routing (separate P1).

## Definition of done

- [ ] Full usage captured on web/TUI/model_call paths; no truncation.
- [ ] `hakase stats` shows tokens + USD by session/day/model.
- [ ] Budget `block` stops a run pre-turn with legible denial; `warn` toasts.
- [ ] Suite green (`gofmt`, `vet`, `go test ./...`, `pnpm test`).

## Validation against codebase (2026-10-08)

Already exists, do NOT rebuild: `OnUsage(tokens,percent)` plumbing (`agentrun.go:42-43`, `sse/bridge.go:252-259`, telegram/discord sinks mirror via `discord/run.go:276-282` + `telegram/run.go:586-591` `OnUsage` and `OnGraphEvent` bridge mirror), `Message.tokens int` (`session/session_data.go:55-68`), OTel run span scaffold (`tracing/runspan.go:25-37`), audit `session_id` join (`agent/audit.go`), `loop_guard.max_output_tokens` (anti-degeneration, not cost), sleep `max_tokens_per_night` (estimated offline only).
Note: "do NOT rebuild" means do not add a second function doing the same thing (NO SLOP). Extending or enhancing the listed paths in place is fine and expected.
To build: full usage struct, pricing table, ledger, budgets, stats CLI, cost UI (0 product hits in `webui/src`; 1 test-comment hit in `useMermaid.test.ts:9`), OTel usage/cost attrs.
