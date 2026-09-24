# Tasks: MCP server mode (#45)

Expose hakase runs as MCP tools (`hakase mcp serve --agent`) with
elicitation-backed gates. Spec: [spec.md](spec.md) — plan: [plan.md](plan.md).
House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — runserver package

- [x] **T1.1 [BE]** `internal/mcp/runserver`: `Deps` (runner seam, session
      service, gates, logger), `Mount` adding `run`, `list_sessions`,
      `get_session` to an `*mcp.Server`; input/output types with JSON schemas.
      Spec: MS-001.
- [x] **T1.2 [BE]** `run` semantics: session resolve/create, user-turn
      persistence, per-session + global run caps, timeout bound, outcome
      mapping (tool error vs `status`), SEP-2322 suspension/resume via
      `RequestState`/`InputResponses`. Spec: MS-002, MS-003.
- [x] **T1.3 [BE]** per-call sink: answer/thinking accumulation, bounded
      activity buffer, progress notifications on the call's progress token.
      Spec: MS-002.
- [x] **T1.4 [BE]** gates: per-run broker registry keyed by session id;
      approval mode mapping; clarify choices/multi-select/free-text; expiry;
      fail-closed on unbound/unfulfillable/expiry. Spec: MS-003.
- [x] **T1.5 [QA]** package tests: in-memory client/server pair covering
      happy-path run (fake runner), busy/cap/unknown-session/unknown-state
      tool errors, timeout, progress notifications, gate round trip over the
      wire, unfulfillable-client degradation, approval + clarify mapping
      (accept/deny/decline/expiry/enum/multiselect/free-text), list/get
      session shapes, tracker caps. Spec: MS-001/002/003.

## Phase 2 — CLI + bootstrap

- [x] **T2.1 [BE]** `internal/cli/mcp.go`: `--agent` flag on `serve`,
      `MCPAgentServeFn` hook + unset wiring error, updated usage text.
      Spec: MS-004.
- [x] **T2.2 [BE]** `cmd/hakase/mcpserver.go`: full agent bootstrap (config,
      tracing, sandbox, sessions, registry, deps, media, gates, runner, model
      info) + server assembly (skills resources + runserver tools, name
      `hakase`) on stdio. Spec: MS-004.
- [x] **T2.3 [BE]** `cmd/hakase/main.go` wires `cli.MCPAgentServeFn`.
      Spec: MS-004.
- [x] **T2.4 [QA]** flag/hook tests: `--agent` wiring dispatch, unwired
      exit 2, stray-argument usage error. Spec: MS-004.

## Phase 3 — Docs + verification

- [x] **T3.1 [DOCS]** `docs/mcp-server/` truthful (spec carries the SEP-2322
      redesign + host-configuration example + client-timeout notes), this
      file ticked in the landing PR.
- [x] **T3.2 [QA]** full CI suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...`, `cd webui && pnpm test`.
- [x] **T3.3 [DOCS]** cross-link: #45 references, #20 tick note, ROADMAP
      untouched (status lives in the issues).
