# Spec: OpenTelemetry GenAI tracing (#18)

One waterfall trace per agent run — LLM calls with token usage, tool calls
with durations, delegated sub-agents nested — exported over OTLP/HTTP to any
GenAI-aware collector (Jaeger, Langfuse, Grafana Tempo, Datadog). Off by
default: disabled means the global otel tracer provider stays no-op, so there
are no exporter goroutines, no network traffic, and no span-recording work.

Governing issue: amurru/hakase#18. GenAI semantic conventions status note: the
conventions are still Development (moved to open-telemetry/semantic-conventions-genai
in June 2026), so hakase pins the attribute keys it owns as string constants
in `internal/tracing` and never re-exports them through a semconv package
version bump.

## Context: what already exists (research findings, ADK go v2.4.0)

- ADK emits GenAI spans through `otel.GetTracerProvider()` captured at package
  init (`adk/v2@v2.4.0/internal/telemetry/telemetry.go:51`): every agent
  activation starts `invoke_agent <name>` (`internal/telemetry/node_tracing.go:96`
  — attrs `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name`,
  `gen_ai.agent.description`, `gen_ai.conversation.id`), every LLM call starts
  `generate_content <model>` (`telemetry.go:86` — `gen_ai.request.model`,
  finish reason, and usage: input tokens, output tokens incl. reasoning, cache
  reads — `telemetry.go:127-148`), and every tool execution starts
  `execute_tool <tool>` (`telemetry.go:154`). The otel global API is a
  delegating proxy: tracers obtained before `otel.SetTracerProvider` start
  recording the moment a real provider is installed. Installing a provider is
  therefore the entire integration with ADK's telemetry.
- Per-call usage lives on ADK events (`session.Event.UsageMetadata`, aggregated
  per LLM call by `internal/llminternal/stream_aggregator.go:81`), NOT on
  `interfaces.GraphEvent` — canvas events carry no model name, no tokens, no
  finish reason, and the TUI (`internal/tui/ui.go:2248`) and cron
  (`internal/cli/cronjob.go:977`) loops never emit canvas events at all.
  **Deviation from the issue's literal step 1:** hakase does not map the
  GraphEvent stream into spans (it cannot satisfy the tokens-per-LLM-call
  acceptance criterion); it uses ADK's built-in GenAI spans for LLM/tool/agent
  spans and adds only the correlation layer ADK cannot know about.
- The three run loops (web/channels `internal/agentrun/agentrun.go:165`, TUI
  `internal/tui/ui.go:2227`, cron `internal/cli/cronjob.go:855`) plus the
  delegation sub-run loop (`internal/agent/delegate.go:491`) all reach the
  same ADK runner layer, so one global provider covers all transports; the
  delegation sub-run ctx derives from the parent tool-execution ctx
  (`delegate.go:441`), so sub-agent spans nest under the root trace with zero
  delegation-code changes (acceptance criterion 3).
- `go.opentelemetry.io/otel` v1.46.0 is already in the module graph (indirect,
  cascaded by the MCP bump); the sdk and the OTLP/HTTP exporter are new direct
  deps. `otlptracehttp` is HTTP + protobuf only — no grpc dependency.
- go-sdk v1.8.0 implements neither traceparent nor `_meta` propagation, but
  exposes `Client.AddSendingMiddleware` (`go-sdk@v1.8.0/mcp/client.go:1135`)
  and `RequestParams.GetMeta/SetMeta` (`shared.go:813`) — outbound request
  mutation is a supported extension point.

## Specs

### Spec OT-001: `internal/tracing` — provider bootstrap, off by default

New leaf package `internal/tracing` (imports only otel + stdlib; never
`internal/config` so any internal package can use it).

- `type Options struct { Enabled bool; Endpoint string; Headers map[string]string; SampleRatio float64; Version string }`.
- `Init(opts Options) (func(), error)`:
  - `!opts.Enabled` → return a no-op shutdown and touch nothing (no provider
    install, no exporter, no goroutines).
  - Enabled → build an `sdktrace.TracerProvider` with resource
    `service.name=hakase`, `service.version=opts.Version` (falls back to
    `dev`), sampler `ParentBased(TraceIDRatioBased(opts.SampleRatio))`, a
    batch span processor over `otlptracehttp` configured with
    `WithHeaders(opts.Headers)` and the endpoint, then
    `otel.SetTracerProvider` and
    `otel.SetTextMapPropagator(TraceContext{})`.
    Endpoint semantics: a pathless URL (`http://localhost:4318`) targets the
    collector at `/v1/traces` (the exporter's host-form option; a pathless
    URL passed as a full URL would target `/`, which no collector serves); a
    URL with an explicit path is used verbatim (path-shaped vendor endpoints
    like Langfuse's `.../api/public/otel`).
  - A malformed endpoint is a startup error — validated in `Install` itself,
    because the exporter merely LOGS parse errors (fail-soft) — an
    explicitly enabled tracing that cannot export is a config mistake; fail
    loud (house rule), never silently trace-nowhere. A collector that is
    merely DOWN at startup is fine: the exporter is lazy and retries; export
    failures surface through otel's error log, never block runs.
- `Tracer() trace.Tracer` — `otel.GetTracerProvider().Tracer("github.com/amurru/hakase")`
  with the pinned schema URL; callers use this so tests can swap the global
  provider.
- Shutdown flushes with a short timeout (5s) and is safe to call once.

### Spec OT-002: pinned attributes and the run root span

`internal/tracing/runspan.go` defines the correlation root span each
transport run creates. Hakase-owned attribute keys are pinned string
constants here (GenAI semconv Development status — see header note); the only
GenAI-registry key used is `gen_ai.conversation.id`:

- Span name `hakase.run`, kind Internal.
- `RunParams{ Transport, SessionID, TaskID, Project, Provider, Model string; Extra map[string]string }`
  → attrs `hakase.transport`, `hakase.session.id`, `hakase.task.id`,
  `hakase.project`, `gen_ai.request.model`, `gen_ai.conversation.id`
  (= session id), plus every `Extra` pair under its literal key (cron job
  name). Empty values are omitted.
- `End(status, errMsg string)` maps the canvas status vocabulary
  (`completed|failed|timed_out`, `interfaces/graph.go:72`) onto span status:
  `completed` → Ok, everything else → Error with the message as description.
- When no provider is installed the span is a no-op: `Start` still returns a
  usable `*Run` whose `End` is a zero-cost no-op, so call sites carry no
  `if tracing.Enabled` branches.

### Spec OT-003: run-loop wiring (one root per user-visible run)

- `agentrun.Driver` gains an exported `Transport string` field plus
  `NewForTransport(r, s, transport)`; `internal/web/handlers/chat.go:224`
  uses `"web"`, `internal/channel/service.go:78` uses `"telegram"`.
- `Driver.RunTurn` derives its `runCtx` from the run span ctx (before project
  binding, so bound-project/snapshot work inherits the span), ends the span
  after `graph.agentEnd` with `graph.runStatus`/`graph.runError`, and ends it
  `failed` on the panic path.
- TUI `runAgentTask` (`internal/tui/ui.go:2227`) and cron `runCronJob`
  (`internal/cli/cronjob.go:855`) wrap their run ctx the same way
  (`transport` `"tui"` / `"cron"`; cron adds
  `hakase.cron.job=<job name>`). ADK spans inside those loops then nest under
  the run root, giving all transports one trace shape (issue step 2).
- Delegation sub-runs get no new spans: their ADK `invoke_agent` spans already
  parent correctly via ctx (see Context).

### Spec OT-004: MCP `traceparent` propagation (SEP-414)

`internal/mcp/traceparent.go`: a sending middleware installed on every
managed-server client in `newElicitingClient` (`internal/mcp/elicitation.go:78`)
that, when the outbound ctx carries a recording span, sets
`_meta["traceparent"]` (W3C format) on the request params via
`RequestParams.GetMeta/SetMeta`, never overwriting an existing value. When
tracing is off the middleware is a no-op read (span not recording → empty
traceparent → params untouched), so installing it unconditionally is free.

`internal/tracing` exposes `Traceparent(ctx) string` producing
`00-<32 hex trace id>-<16 hex span id>-01` from the ctx span context (empty
when absent/not recording) so the MCP package needs no otel imports of its
own beyond none.

### Spec OT-005: knowledge retrieval spans

`recall_knowledge` and `search_knowledge` handlers
(`internal/knowledge/knowledge_tools.go:411,467`) wrap their body in a child
span `retrieval knowledge` with `gen_ai.operation.name=retrieval` and a
result-count attribute (`hakase.retrieval.results`), ended with error status
on failure. Query text is deliberately not recorded (content-capture stays
opt-in per the conventions). Recall-by-name and search both count as
retrieval (issue step 1: "retrieval for knowledge recall").

### Spec OT-006: config `tracing` section

`config.TracingConfig` in `internal/config/config.go`, following the
MemoryConfig pattern (`config.go:238-289`):

- `Enabled bool` (`json:"enabled,omitempty"`) — zero value = off, matching
  the feature default; no tri-state pointer needed.
- `Endpoint string` — OTLP/HTTP base URL; default
  `http://localhost:4318` (the OTLP convention) applied in `ApplyDefaults`.
- `Headers map[string]string` — sent verbatim on OTLP exports (vendor auth).
- `SampleRatio float64` — root-sampling ratio, default 1.
- `Validate()`: ratio must be within `[0,1]` (NaN rejected), header keys
  non-empty; everything negative-shape fails loudly per house convention.
- Env overrides in `LoadConfig` + `envConfigSet` (`config.go:719`):
  `HAKASE_TRACING_ENABLED` (strict bool policy),
  `HAKASE_TRACING_ENDPOINT`, `HAKASE_TRACING_SAMPLE_RATIO` (strict float,
  error names the variable), `HAKASE_TRACING_HEADERS` (`K=V,K2=V2`, entries
  without `=` are load errors).

### Spec OT-007: process wiring

`cmd/hakase/web.go:runServer` (covers web, serve, Telegram channel, and the
cron scheduler inside them) and `cmd/hakase/main.go:runTUI` call
`tracing.Init` right after `config.LoadConfig` succeeds and defer shutdown.
Standalone headless subcommands (`sleep run` etc.) use the `ModelCaller` seam,
not ADK runner loops, and are out of scope.

## Non-goals

- No span content capture (prompts/completions/args beyond ADK's own behavior).
- No trace_id column in `logs/exec-audit.jsonl` (session id correlation
  already lands via `hakase.session.id`; audit↔trace join is a follow-up).
- No web UI for the tracing config (config file + env only).
- No metrics/logs OTLP signals — traces only (#18 scope).
- No hakase-emitted `chat` spans — ADK's `generate_content` spans are the
  chat spans; duplicating them would double-count usage in collectors.

## Definition of done

- [ ] A web (or Telegram) run produces one trace: `hakase.run` →
      `invoke_agent` → `generate_content`/`execute_tool` (with per-call token
      usage and durations), visible in an OTLP-ingesting collector.
- [ ] Disabled config adds zero spans, zero goroutines, zero traffic.
- [ ] Delegated sub-agent `invoke_agent` spans appear under the parent run's
      trace id.
- [ ] MCP `tools/call` requests carry `_meta.traceparent` while a span is
      recording.
- [ ] `gofmt`, `go vet`, `go test ./...` green; new behavior covered by
      self-contained tests (in-memory httptest collector, span recorder).
