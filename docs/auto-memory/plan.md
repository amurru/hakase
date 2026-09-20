# Plan: Agent-Written Auto-Memory

Feature: `auto-memory` (issue [#16](https://github.com/amurru/hakase/issues/16)).
Governing contract: `spec.md`. This file records sequencing and the
dependency-driven order of attack; `tasks.md` carries the atomic boxes.

## Sequence

```
Phase 1  Foundation (no cross-package deps yet)
  1. internal/config MemoryConfig (+ accessors, env, example)      [T1.x]
  2. internal/memory leaf store + render (+ tests)                 [T2.x]

Phase 2  Agent surface (needs 1 + 2)
  3. remember/forget_memory tools in internal/agent + SetupRunner
     registration, disabled-when-off                               [T3.x]
  4. Session-start injection: HistoryBuilder provider + SetupRunner
     wiring                                                        [T4.x]

Phase 3  Surfacing (needs 2; independent of 3/4)
  5. hakase memory CLI                                             [T5.x]
  6. Web API + MemoryView.vue + router/nav                         [T6.x]

Phase 4  Close-out
  7. CHANGELOG, docs truthfulness pass, full suite                 [T7.x]
```

Phases 1a and 1b are independent; Phase 3's two tracks are independent of
each other and of Phase 2. Everything is standard-library Go plus existing
in-repo patterns — no new module dependencies (D9: no extraction pass, no
TUI work).

## Key risks and their answers

- **Import cycle** (memory → agent → context → memory): prevented by D8 —
  `internal/memory` is a leaf; tools live in `internal/agent`; context
  receives the block through a `func(agent.Context) string` provider set in
  SetupRunner, so `internal/context` never imports `internal/memory`.
- **Per-process staleness** (one runner, many sessions): prevented by D5 —
  injection reads the store per session-start, never at SetupRunner time.
- **Prompt bloat**: three caps (per-note 2000 chars, block
  `max_prompt_chars`, store `max_notes`) + once-per-session injection.
- **Cross-surface concurrency** (web + Telegram + CLI on one file):
  channels-state mtime/size reload + flock covers it; CLI works while the
  server runs.
- **TUI fallback path**: the callback resolves sessions exactly like the
  history prepend does (session-ID from ctx, active-session fallback), so
  the TUI's single session gets memory injected once without a registered
  run mapping.

## Verification strategy

Each phase lands with its package tests green (`go test ./internal/<pkg>/`).
The close-out phase runs the CI quartet (`gofmt -l`, `go vet ./...`,
`go test ./...`, `cd webui && pnpm test`) and ticks `tasks.md` in the same
change. Manual smoke: run the binary, ask the agent to remember a
preference, restart, confirm the block appears (visible in the canvas
inspector / debug log) — covered by the injection unit tests, so manual
smoke is optional.
