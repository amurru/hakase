# Tasks: OTel GenAI tracing (#18)

Tracing for agent runs over OTLP/HTTP (GenAI semantic conventions), off by
default. Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule:
tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` documentation.

## Phase 1 — Foundation

- [x] **T1.1 [BE]** Add `go.opentelemetry.io/otel/sdk` + `otlptracehttp`
      v1.46.0 deps. Spec: OT-001.
- [x] **T1.2 [BE]** `internal/tracing`: Options/Install/Tracer, no-op when
      disabled, fail-loud endpoint validation (the exporter only logs parse
      errors, so Install validates the URL itself; pathless endpoints get
      `/v1/traces` appended). Spec: OT-001.
- [x] **T1.3 [BE]** `internal/tracing`: pinned attrs + `RunSpan`/`End` with
      canvas-status mapping. Spec: OT-002.
- [x] **T1.4 [BE]** `internal/tracing`: `Traceparent(ctx)`. Spec: OT-004.
- [x] **T1.5 [BE]** `config.TracingConfig` (enabled/endpoint/headers/
      sample_ratio) + defaults + validate + env overrides + `envConfigSet`.
      Spec: OT-006.
- [x] **T1.6 [QA]** tracing tests: disabled no-op, httptest OTLP export,
      pre-Install tracer delegation (aa_global_test.go), status mapping,
      traceparent format, ratio-0 sampling, bad endpoint fail-loud. Spec:
      OT-001/002/004.
- [x] **T1.7 [QA]** config tests: defaults, validate bounds, strict env
      parsing (errors name the variable), env-only config. Spec: OT-006.

## Phase 2 — Run-loop wiring

- [x] **T2.1 [BE]** `agentrun`: `Transport` field, `NewForTransport`,
      `hakase.run` root span in `RunTurn` (success + panic paths; span ctx
      precedes project binding so binding work is inside the trace). Spec:
      OT-003.
- [x] **T2.2 [BE]** `"web"` / `"telegram"` driver construction in
      `internal/web/handlers/chat.go` + `internal/channel/service.go`. Spec:
      OT-003.
- [x] **T2.3 [BE]** TUI `runAgentTask` + cron `runCronJob` root spans (cron
      adds `hakase.cron.job`). Spec: OT-003.
- [x] **T2.4 [QA]** agentrun test: one ended `hakase.run` span with attrs;
      panic path → Error status; disabled → nothing recorded. Spec: OT-003.

## Phase 3 — MCP + knowledge

- [x] **T3.1 [BE]** `internal/mcp` traceparent sending middleware in
      `newElicitingClient`. Spec: OT-004.
- [x] **T3.2 [QA]** middleware test over in-memory transports: injects only
      with a recording span, value matches the caller's ctx, never
      overwrites. Spec: OT-004.
- [x] **T3.3 [BE]** `retrieval knowledge` spans in recall + search handlers
      (result counts, error status). Spec: OT-005.

## Phase 4 — Wiring + docs

- [x] **T4.1 [BE]** `runServer` + `runTUI` tracing init/shutdown. Spec:
      OT-007.
- [x] **T4.2 [DOCS]** README tracing section + config reference bullet +
      config.json.example block + CHANGELOG `[Unreleased]` entry.
- [x] **T4.3 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...`.

## Deferred (follow-ups, not part of #18 acceptance)

- `trace_id` column in `logs/exec-audit.jsonl` for direct audit↔trace joins
  (session-id correlation is already possible via `hakase.session.id`).
- `hakase mcp serve` (server side) accepting/propagating incoming
  `_meta.traceparent`.
- Web UI for the tracing config (config file + env only today).
- Standalone `sleep` subcommands tracing via the `ModelCaller` seam.
