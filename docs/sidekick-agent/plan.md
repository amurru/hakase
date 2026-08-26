# Execution Plan: Sidekick Mode

Feature: `sidekick-agent`
Source of truth: `spec.md` (atomic specs SK-001..SK-012, r1). This file
sequences the work into phases with exit criteria and parallelization.
Date: 2026-08-24 (r1). Scope: v1 = text-only sidekick, modes
off / on_demand / watch / full; no sidekick tools, no response replacement.
r2: Phase 0 decisions closed with user confirmation (Q1-Q5); no scope change.

## Phases

### Phase 0 - Decisions (CLOSED r1, user-confirmed 2026-08-24)

- [x] **T0.1 (Q1)** Default mode on implicit enable = `on_demand`: enabled +
  model + empty `mode` enables `/sidekick` and `ask_sidekick` but never the
  background watcher; block absent/disabled stays `off`. Recorded in
  research.md D1 and spec.md SK-001 (`EffectiveMode()`).
- [x] **T0.2 (Q2)** Watch evaluator sees the CURRENT RUN transcript only
  (~6k char window); persisted session history excluded. Revisit only if note
  quality disappoints. Recorded in research.md D7.
- [x] **T0.3 (Q3)** Severity rendering: quiet inline chips for all severities;
  color + icon differ, no notifications/pings for critical. Recorded in
  research.md D8 and spec.md SK-010.
- [x] **T0.4 (Q4)** Privacy posture accepted: sidekick requests may carry
  conversation excerpts (incl. tool output) to its endpoint; documented in
  README + Settings footnote pointing at local `openai-compatible` models.
  Recorded in research.md D9, spec.md SK-010/SK-012.
- [x] **T0.5 (Q5)** Naming locked: feature `sidekick`, tool `ask_sidekick`,
  command `/sidekick`, SSE event `sidekick`, config block `sidekick`.
  Recorded in research.md D10.

Exit: recorded across research.md/spec.md. Done - no open questions remain.

### Phase 1 - Foundation (serial)

**SK-001** Config block + env overrides (`internal/config`).
- Exit: `SidekickConfig` with defaults/validation/env precedence,
  `config.json.example` documented, `go test ./internal/config/...` green.

**SK-002** Core package `internal/sidekick` (Ask/Evaluate/policy).
- Exit: fake-LLM tests for happy/timeout/parse-fail paths, policy unit tests,
  zero network in tests, import-direction rule holds.

Sequence: SK-001 -> SK-002.

### Phase 2 - Injection + observation core (after Phase 1)

Run in parallel:

- **Track A: SK-003** Note queue + HistoryBuilder drain. Exit: queue
  cap/dedupe tests, BeforeModelCallback injection test, nil-queue no-op.
- **Track B: SK-006** EventNotifier extension (interfaces + TUI line + SSE
  event). Exit: all implementers compile, bridge payload test, TUI render test.
- **Track C: SK-007** Session persistence kind. Exit: round-trip + resume +
  history-inclusion policy tests.

### Phase 3 - Agent integration (after A + B)

- **SK-004** Orchestrator watcher (AfterModelCallback). Exit: debounce/budget/
  inert-when-off/race-clean per spec; notes reach queue.
- **SK-005** ask_sidekick tool + instruction section + blockedTools update.
  Exit: stubbed tool tests, delegation isolation test.

SK-004 and SK-005 are parallelizable (different files) after their common deps.

### Phase 4 - Surfaces (after Phase 3; B/C from Phase 2)

- **SK-008** TUI `/sidekick` command. Exit: dispatch tests, persistence,
  busy-run interleave safety.
- **SK-009** Web backend endpoints + SSE wiring. Exit: handler auth/redaction/
  concurrency tests, SSE delivery test.
- **Track D: SK-010** Web UI (can start as soon as SK-009 contracts are
  merged behind a disabled status). Exit: vitest + vue-tsc green.

SK-008 and SK-009 are parallelizable.

### Phase 5 - Wiring + hardening

**SK-011** Bootstrap wiring in `cmd/hakase/main.go` + `web.go`.
- Exit: both bootstraps create/disable sidekick identically, build green under
  default and `dev` tags, smoke ask works end-to-end.

**SK-012** Docs, self-skill, README, changelog.
- Exit: README section + env table rows, hakase skill updated, CHANGELOG entry.

## Critical Path

```text
SK-001 ─► SK-002 ─┬─► SK-003 ─┐
                  ├─► SK-006 ─┼─► SK-004 ─► SK-008/SK-009 ─► SK-010 ─► SK-011 ─► SK-012
                  └─► SK-007 ─┴─► SK-005 ──────────────┘
```

- SK-001 -> SK-002 is the serial spine; everything else fans out from it.
- SK-003/SK-006/SK-007 are independent of each other.
- SK-011 is the integration gate; SK-012 closes the feature.

## Task Tracking

Atomic, hand-offable tasks live in `tasks.md` (T-phases mirror the phases
above; each task cites its governing SK spec and a Verify command). Update
checkboxes there as work progresses - this file stays strategy-only.

## Verification commands per phase

```bash
go test ./internal/config/...      # SK-001
go test ./internal/sidekick/...    # SK-002
go test ./internal/context/...     # SK-003, SK-007
go test ./internal/interfaces/... ./internal/web/sse/... ./internal/tui/...  # SK-006
go test ./internal/session/...     # SK-007
go test ./internal/agent/          # SK-004, SK-005 (+ -race once)
go test ./internal/web/...         # SK-009
cd webui && pnpm test              # SK-010
go build ./... && go test ./...    # gates before SK-012 merge
```

## Definition of Done (feature-level)

1. With `sidekick` absent/disabled: byte-for-byte current behavior (no extra
   model calls, no UI noise, config loads unchanged).
2. `on_demand`: `/sidekick "question"` in TUI and the web ask endpoint return a
   second-model answer rendered distinctly and persisted across resume;
   orchestrator can call `ask_sidekick` mid-task when enabled.
3. `watch`/`full`: during an agent run, genuine gaps produce at most
   MaxNotesPerTurn advisory SIDEKICK NOTEs that visibly steer the next model
   call; every sidekick failure degrades to a log line without touching run
   latency beyond O(append).
4. Web UI shows live sidekick events via SSE and a Settings section; TUI shows
   log lines; credentials never appear in any surface or log.
5. `go build ./...`, `go test ./...`, `pnpm test`, `pnpm build` all green;
   `-race` clean on agent watcher tests.

## Open Questions

None. All decisions closed in Phase 0 (r1, user-confirmed 2026-08-24).
New questions discovered during implementation get appended here with a
decision date, never silently resolved in code.
