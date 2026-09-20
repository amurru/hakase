# Task List: Agent-Written Auto-Memory

Feature: `auto-memory` (issue [#16](https://github.com/amurru/hakase/issues/16)).
Atomic, hand-offable tasks; each references the governing spec decision in
`spec.md` and is sized to 1-3 tool calls plus one verification step.

Legend: `[BE]` Go backend, `[FE]` frontend, `[QA]` test/docs.

---

## Phase 1 - Foundation

- [ ] **T1.1 [BE]** `internal/config`: `Memory MemoryConfig` (`Enabled *bool`,
      `MaxPromptChars int`, `MaxNotes int`), accessors `MemoryEnabled` (nil ⇒
      true), `MemoryMaxPromptChars` (4000), `MemoryMaxNotes` (200);
      `ApplyDefaults`/`Validate` in the LoadConfig sequence; env overrides
      `HAKASE_MEMORY_ENABLED`, `HAKASE_MEMORY_MAX_PROMPT_CHARS`,
      `HAKASE_MEMORY_MAX_NOTES`.
      Verify: `go test ./internal/config/` — defaults, nil-vs-false, env
      precedence, validation. Spec: D4/D6.

- [ ] **T1.2 [BE]** `internal/memory`: `Note`/`State` types, fixed category
      enum, `DefaultPath`/`Default`/`Open`, `Store.Get/Update/Add/Remove`
      (dedupe, trim/cap 2000, category validation), flock + tmp+rename
      0600/0700, corrupt-file quarantine, `SelectForProject`,
      `RenderBlock(notes, maxChars)`.
      Verify: `go test ./internal/memory/` — round-trip, perms, reload,
      quarantine, dedupe, cap, filter, render budget + truncation notice.
      Spec: D1/D2/D3/D6.

- [ ] **T1.3 [QA]** `config.json.example`: documented `memory` block.
      Verify: read-through / `jq .memory config.json.example`. Spec: D6.

## Phase 2 - Agent surface

- [ ] **T2.1 [BE]** `internal/agent/remember.go`: `remember` (upsert by
      optional id) + `forget_memory` (idempotent) via `util.NewDocTool`;
      project stamping `RootFrom` → `CurrentRoot` → `""`; store-full error
      names the remedy.
      Verify: `go test ./internal/agent/ -run Memory` — upsert, forget,
      invalid category/content, full store, project stamping.
      Spec: D3/D4/D6/D7.

- [ ] **T2.2 [BE]** Register the tools in `SetupRunner` (orchestrator only,
      conditional on `MemoryEnabled`; absent entirely when disabled — no
      sub-agent fallback entry).
      Verify: `go test ./internal/agent/ -run Setup` (disabled config ⇒ no
      memory tools); `gofmt`/`go vet`. Spec: D7.

- [ ] **T2.3 [BE]** `internal/context`: `SetMemoryProvider(func(agent.Context)
      string)` + per-session once-set; inject user-role block at head of run
      contents on the session's first model call (even with empty history),
      swallow provider errors.
      Verify: `go test ./internal/context/ -run Memory` — first-call-only,
      empty-session injection, nil provider, two-session isolation.
      Spec: D5.

- [ ] **T2.4 [BE]** SetupRunner wiring: memory provider closure
      (`Default()` → `SelectForProject(RootFrom/CurrentRoot)` →
      `RenderBlock` under `MemoryMaxPromptChars`) onto the history builder.
      Verify: `go test ./internal/agent/ -run Memory` (provider renders
      scoped block); `go build ./...`. Spec: D5/D8.

## Phase 3 - Surfacing

- [ ] **T3.1 [BE]** `internal/cli/memory.go`: `hakase memory list [--all]`,
      `forget <id>`, `add --category <cat> [--project <path>] <content...>`;
      register in `command.go`.
      Verify: `go test ./internal/cli/ -run Memory`; `hakase memory` usage
      exit code 2. Spec: D1.

- [ ] **T3.2 [BE]** `internal/web/handlers/memory.go`: `GET /api/memory`,
      `DELETE /api/memory/{id}`; register in `spa.go` beside knowledge.
      Verify: `go test ./internal/web/handlers/ -run Memory`. Spec:
      surfacing.

- [ ] **T3.3 [FE]** `webui/src/views/MemoryView.vue` (list, category badge,
      project, delete, empty state), route `/memory`, nav entry, API client,
      vitest.
      Verify: `cd webui && pnpm vitest run src/views/MemoryView.test.ts`
      and `pnpm build` typecheck. Spec: surfacing.

## Phase 4 - Close-out

- [ ] **T4.1 [QA]** CHANGELOG `Unreleased/Added` entry; tick this file in
      the same change set; ROADMAP untouched (issue closing is the status
      signal, per its maintenance conventions).
      Verify: docs agree with code (spec decisions D1-D9 all observable).

- [ ] **T4.2 [QA]** Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`,
      `cd webui && pnpm test`.
      Verify: all four green.

## Deferred (tracked in the issue, not boxes here)

- Session-end extraction pass (cheap `summary_model` proposer + sleep-miner
  redaction reuse) — spec D9.
- TUI `/memory` inspection command — spec D9.
