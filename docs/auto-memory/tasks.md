# Task List: Agent-Written Auto-Memory

Feature: `auto-memory` (issue [#16](https://github.com/amurru/hakase/issues/16)).
Atomic, hand-offable tasks; each references the governing spec decision in
`spec.md` and is sized to 1-3 tool calls plus one verification step.

Legend: `[BE]` Go backend, `[FE]` frontend, `[QA]` test/docs.
Status (2026-09-20): shipped and verified against the code; the only open
items are the two deferred follow-ups at the bottom.

---

## Phase 1 - Foundation

- [x] **T1.1 [BE]** `internal/config`: `Memory MemoryConfig` (`Enabled *bool`,
      `MaxPromptChars int`, `MaxNotes int`), accessors `MemoryEnabled` (nil ⇒
      true), `MemoryMaxPromptChars` (4000), `MemoryMaxNotes` (200);
      `ApplyDefaults`/`Validate` in the LoadConfig sequence; env overrides
      `HAKASE_MEMORY_ENABLED`, `HAKASE_MEMORY_MAX_PROMPT_CHARS`,
      `HAKASE_MEMORY_MAX_NOTES`.
      Verify: `go test ./internal/config/` — defaults, nil-vs-false, env
      precedence, validation. Spec: D4/D6.

- [x] **T1.2 [BE]** `internal/memory`: `Note`/`State` types, fixed category
      enum, `DefaultPath`/`OpenDefault`/`Open`, `Store.Get/Update/Add/Remove/
      Touch` (dedupe, trim/cap 2000, category validation), flock +
      tmp+rename 0600/0700 (dir created before the lock file), corrupt-file
      quarantine, `SelectForProject`, `RenderBlock(notes, maxChars)` (header
      cost counted against the budget; truncation notice always appended).
      Verify: `go test ./internal/memory/` — round-trip, perms, reload,
      quarantine, dedupe, cap, filter, render budget + truncation notice.
      Spec: D1/D2/D3/D6.

- [x] **T1.3 [QA]** `config.json.example`: documented `memory` block.
      Verify: read-through / `jq .memory config.json.example`. Spec: D6.

## Phase 2 - Agent surface

- [x] **T2.1 [BE]** `internal/agent/remember.go`: `remember` (upsert by
      optional id) + `forget_memory` (idempotent) via `util.NewDocTool`;
      project stamping `project.RootFrom(ctx)` (context root → process-root
      fallback → ""); store-full error names the remedy.
      Verify: `go test ./internal/agent/ -run 'TestRemember|TestWireMemory'`
      — upsert, forget, invalid category/content, full store, project
      stamping. Spec: D3/D4/D6/D7.

- [x] **T2.2 [BE]** `wireMemory(cfg, historyBuilder)` registered in
      `SetupRunner`: orchestrator only, conditional on `MemoryEnabled`;
      absent entirely when disabled — no sub-agent fallback entry.
      Verify: `go test ./internal/agent/ -run TestWireMemoryDisabled`;
      `gofmt`/`go vet`. Spec: D7.

- [x] **T2.3 [BE]** `internal/context`: `SetMemoryProvider(func(agent.Context)
      string)` + per-session once-set (`MemoryProvider` accessor for
      inspection); inject user-role block at head of run contents on the
      session's first model call via a deferred splice (fires on every
      return path, including empty-history sessions); empty blocks mark
      nothing and stay eligible for a later call.
      Verify: `go test ./internal/context/ -run TestMemory` —
      first-call-only, empty-session injection, nil provider, empty-block
      retry, two-session isolation. Spec: D5.

- [x] **T2.4 [BE]** SetupRunner wiring: provider closure
      (`OpenDefault()` → `SelectForProject(RootFrom(ctx))` → `RenderBlock`
      under `MemoryMaxPromptChars`) onto the history builder.
      Verify: `go test ./internal/agent/ -run
      TestWireMemoryEnabledAttachesProvider`; `go build ./...`. Spec: D5/D8.

## Phase 3 - Surfacing

- [x] **T3.1 [BE]** `internal/cli/memory.go`: `hakase memory list [--all]`,
      `forget <id>`, `add --category <cat> [--project <path>] <text...>`
      (cwd-derived project root, `-` = global); registered in `command.go`.
      Verify: `go test ./internal/cli/ -run TestMemoryCLI`; usage exit code
      2. Spec: D1.

- [x] **T3.2 [BE]** `internal/web/handlers/memory.go`: `GET /api/memory`,
      `DELETE /api/memory/{id}` (404 on unknown); registered in `spa.go`
      beside knowledge under the auth middleware, nil-tolerant when no
      hakase home.
      Verify: `go test ./internal/web/handlers/ -run TestMemory`. Spec:
      surfacing.

- [x] **T3.3 [FE]** `webui/src/views/MemoryView.vue` (list grouped by
      category, filter chips, project scope, delete, empty state), route
      `/memory`, Layout nav entry, `webui/src/lib/memory.ts` API client,
      vitest at the lib layer (house convention: no view-level tests).
      Verify: `cd webui && pnpm vitest run src/lib/memory.test.ts` and
      `pnpm build` typecheck. Spec: surfacing.

## Phase 4 - Close-out

- [x] **T4.1 [QA]** CHANGELOG `Unreleased/Added` entry; this file ticked in
      the same change set; ROADMAP untouched (issue closing is the status
      signal, per its maintenance conventions).
      Verify: docs agree with code (spec decisions D1-D9 all observable).

- [x] **T4.2 [QA]** Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`,
      `cd webui && pnpm test`.
      Verify: all four green (2026-09-20).

## Review fixes (PR #35, CodeRabbit round 1)

- [x] **R1 [BE]** `internal/memory`: the exclusive flock is held across the
      whole load-mutate-save transaction (concurrent agent/CLI/web writers
      can no longer last-save-wins over each other's notes); the store dir
      is `Chmod 0700` and the tmp file re-chmodded 0600 on every write so a
      pre-existing permissive dir/tmp cannot leak into the renamed store;
      `OpenDefault` fails closed when no home directory resolves.
      Verify: flock-serialization, perm-tightening, and no-home tests in
      `internal/memory`.
- [x] **R2 [BE]** `internal/context`: the once-per-session gate is an
      atomic reserve-before-render (rollback on empty block), so concurrent
      model calls for one session inject exactly once.
      Verify: 8-callback concurrency race test in `internal/context`.
- [x] **R3 [BE]** `internal/cli`: `memory add --project` is normalized to
      the absolute enclosing project root (matching the agent's write path)
      before storing.
      Verify: CLI test case (`./sub/repo` → `project.FindRoot(abs)`).
- Declined: strict rejection of malformed `HAKASE_MEMORY_*` env values —
  the silent-fallback bool/int env idiom is house-wide (sidekick, telegram,
  debug, max-output-tokens); making one section strict diverges. Revisit as
  a house-wide strictness pass if wanted.

- [x] **R4 [BE]** `internal/memory` (CodeRabbit round 2): disk-reading
      readers (`Open`/`reload`, `Get`'s reload path) take the same flock as
      writers, so a corrupt-file quarantine cannot race a concurrent write
      into renaming away a freshly-written valid store; the unchanged-cache
      fast path stays lock-free.
      Verify: `Get`-reload flock-blocking test in `internal/memory`.

## Deferred (tracked in the issue, not boxes here)

- Session-end extraction pass (cheap `summary_model` proposer + sleep-miner
  redaction reuse) — spec D9.
- TUI `/memory` inspection command — spec D9.
