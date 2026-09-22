# Execution Plan: OTel GenAI tracing (#18)

Strategy: ADK go v2.4.0 already emits the GenAI span tree
(`invoke_agent` / `generate_content` / `execute_tool`) through the global
otel tracer provider, so hakase's work is (1) install an SDK provider +
OTLP/HTTP exporter when enabled, (2) add the correlation root span per
transport run, (3) propagate `traceparent` into MCP `_meta`, (4) add
retrieval spans for knowledge tools. No changes to the GraphEvent/canvas
path; delegation nesting falls out of ctx propagation.

## Phases

### Phase 1 — Foundation (internal/tracing + config)

1. `go get go.opentelemetry.io/otel/sdk@v1.46.0`
   `go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.46.0`
   (otel core v1.46.0 is already in the graph; exporter version follows core).
2. `internal/tracing/tracing.go` — Options/Init/Tracer per OT-001. Fail loud
   on exporter construction errors; no-op shutdown when disabled.
3. `internal/tracing/runspan.go` — pinned attribute constants + `RunSpan`/
   `End` per OT-002 (status map completed→Ok, else Error).
4. `internal/tracing/propagation.go` — `Traceparent(ctx)` per OT-004.
5. `internal/config`: `TracingConfig` + `ApplyDefaults` + `Validate` + env
   overrides + `envConfigSet` per OT-006.

### Phase 2 — Run-loop wiring

6. `agentrun.Driver.Transport` + `NewForTransport`; root span in `RunTurn`
   (span ctx before project binding; end on success path with
   `graph.runStatus`, `failed` on panic) per OT-003.
7. `internal/web/handlers/chat.go` / `internal/channel/service.go`: construct
   with `"web"` / `"telegram"`.
8. TUI `runAgentTask` + cron `runCronJob` root spans (cron adds
   `hakase.cron.job`) per OT-003.

### Phase 3 — MCP + knowledge

9. `internal/mcp/traceparent.go` middleware, installed in
   `newElicitingClient`, per OT-004.
10. `internal/knowledge/knowledge_tools.go`: `retrieval knowledge` child
    spans on recall + search handlers per OT-005.

### Phase 4 — Process wiring + docs

11. `cmd/hakase/web.go:runServer` + `cmd/hakase/main.go:runTUI`: Init after
    LoadConfig, defer shutdown, per OT-007.
12. README section + CHANGELOG `[Unreleased]` entry; issue #18 acceptance
    checklist cross-checked in the PR description.

## Critical path

1→2/3/4 (foundation) before everything; 5 and 6 are independent of each
other but both precede 11; 9 and 10 are independent leaf changes.

## Verification baseline

- `internal/tracing` tests: disabled = global provider untouched (spans not
  recorded); enabled = spans exported to an `httptest` OTLP endpoint
  (protobuf request decoded with `go.opentelemetry.io/proto/otlp/trace/v1`);
  status mapping; traceparent format; ratio 0 → unsampled.
- `internal/config`: defaults/validate/env-override cases following
  `config_env_overrides_test.go` conventions (strict parse errors name the
  variable).
- `internal/agentrun`: `RunTurn` under a span-recorder provider emits exactly
  one ended `hakase.run` span with transport/session attrs; error path marks
  Error status. Existing `graph_test.go` harness extended, not replaced.
- `internal/mcp`: middleware injects `_meta.traceparent` only when the ctx
  has a recording span and never overwrites an existing value.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`.

## Risk register

- ADK internal telemetry is explicitly unstable (their package warning) —
  accepted: hakase depends only on its public ctx/global behavior, and the
  spans it adds itself carry pinned keys.
- Global provider mutation in tests must save/restore via `t.Cleanup` and
  stay sequential within a package.
- The otel global delegating proxy is load-bearing (ADK captures tracers at
  package init) — asserted by an ordering-safe test (span recorded despite
  tracer obtained before SetTracerProvider… via the global Tracer() helper,
  which is exactly how ADK does it).
