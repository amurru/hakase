# Task List: Sidekick Mode

Feature: `sidekick-agent` (r2 scope: text-only sidekick; modes `off` /
`on_demand` / `watch` / `full`; no sidekick tools, no response replacement;
naming locked per D10)
Atomic, hand-offable tasks. Each references the governing spec in `spec.md`
and is sized to 1-3 tool calls + one verification step.
Date: 2026-08-24 (r1)

Legend: `[BE]` Go backend, `[FE]` frontend, `[QA]` test/docs. Status: TODO unless marked.

---

## Phase 0 - Decisions (CLOSED r1, user-confirmed 2026-08-24)

- [x] **T0.1 [QA]** Default mode on implicit enable = `on_demand` (Q1-B): enabled + model + empty `mode` -> `/sidekick` + `ask_sidekick` live, watcher never starts implicitly. Governs SK-001, SK-004.
- [x] **T0.2 [QA]** Watch evaluator sees current-run transcript only (~6k char window); persisted history excluded (Q2-A). Governs SK-002, SK-004.
- [x] **T0.3 [QA]** Severity rendering: quiet inline chips, color/icon only, zero notification dispatch (Q3). Governs SK-010.
- [x] **T0.4 [QA]** Privacy posture accepted; README + Settings footnote required (Q4). Governs SK-010, SK-012.
- [x] **T0.5 [QA]** Naming locked: `sidekick` / `ask_sidekick` / `/sidekick` / SSE `sidekick` / config `sidekick` (Q5). Governs all specs.

---

## Phase 1 - Foundation

- [ ] **T1.1 [BE]** Add `SidekickConfig` to `internal/config/config.go`: fields per SK-001 contract (`Enabled *bool`, `Mode`, `Provider`, `ModelName`, `BaseURL`, `APIKey`, tuning numbers), `Config.Sidekick` field, accessor helpers with defaults, `EffectiveMode()` implementing the Q1-B resolution (disabled/empty-model -> `off`; empty mode -> `on_demand`), mode validation error only when enabled.
      Verify: `go test ./internal/config/...` defaults, validation, implicit-on_demand case, zero-config regression byte-stability. Spec: SK-001.

- [ ] **T1.2 [BE]** Env overrides in config loading after file read: `HAKASE_SIDEKICK_MODEL`, `HAKASE_SIDEKICK_PROVIDER`, `HAKASE_SIDEKICK_BASE_URL`, `HAKASE_SIDEKICK_API_KEY`. Nothing else - do NOT read `OPENAI_API_KEY`/provider-native keys (house rule).
      Verify: `go test ./internal/config/...` file < env precedence mirroring TestLoadConfigSummaryModel. Spec: SK-001.

- [ ] **T1.3 [QA]** Document the `sidekick` block in `config.json.example`: commented fields, defaults shown, implicit on_demand default noted under `mode`, env hints.
      Verify: manual read / `jq .sidekick config.json.example`. Spec: SK-001.

- [ ] **T1.4 [BE]** Create `internal/sidekick/sidekick.go`: package state (`LLM model.LLM`, `CurrentConfig func() *config.SidekickConfig` hook), `Available()` (LLM non-nil AND EffectiveMode != off), `Ask(ctx, question)` single-prompt call with `AskTimeoutSeconds` cap, accumulated-text extraction skipping thought parts. Import direction: must NOT import agent/tui/web.
      Verify: `go test ./internal/sidekick/...` fake-LLM happy path, timeout path, Available() gating matrix. Spec: SK-002.

- [ ] **T1.5 [BE]** Create `internal/sidekick/evaluate.go`: `Evaluate(ctx, request, transcript)` building the strict-JSON verdict prompt ("report only genuine gaps...never invent work"), lenient parsing (strip code fences), `Observation{GapFound, Severity, Note}` result, parse-failure => error.
      Verify: `go test ./internal/sidekick/...` valid JSON, fenced JSON, malformed JSON error, severity normalization. Spec: SK-002.

- [ ] **T1.6 [BE]** Create `internal/sidekick/policy.go`: debounce window, per-run evaluation budget counter, note truncation to `MaxNoteChars`, duplicate-suppression helper for identical normalized notes within TTL.
      Verify: `go test ./internal/sidekick/...` debounce, budget exhaustion, truncation cap, dedupe unit cases. Spec: SK-002.

---

## Phase 2 - Injection + observation core (tracks parallel)

### Track A - Note queue + injection (SK-003)

- [ ] **T2A.1 [BE]** Create `internal/util/sidekick_queue.go`: `SidekickNote{Text, Severity, At}`, `SidekickNoteQueue` mutex-bounded FIFO (`Push` returns false when full/duplicate, `DrainActive` non-consuming snapshot, `Clear`), `SidekickNoteFraming(n)` rendering `"SIDEKICK NOTE (advisory, from a second model):"` user-role content with untrusted-data wrapping and per-call char cap.
      Verify: `go test ./internal/util/...` cap, dedupe, framing shape, nil-safe behavior. Spec: SK-003.

- [ ] **T2A.2 [BE]** Extend `HistoryBuilder` (`internal/context/context.go`): `SetSidekickQueue(q)`, drain appended AFTER the pending-user steering block in `BeforeModelCallback`; re-injected every call; total injected chars capped. Nil queue = no-op.
      Verify: `go test ./internal/context/...` injected-note placement/framing test; existing suite stays green. Spec: SK-003.

### Track B - Eventing (SK-006)

- [ ] **T2B.1 [BE]** Add `SidekickEvent(status, message string)` to `interfaces.EventNotifier` (status: `evaluating` | `note` | `error`) and implement in `sse.EventBridge` publishing `event: sidekick` `{status, message}` (pre-truncated by sender, non-blocking publish).
      Verify: `go build ./...` breaks without TUI implementer (intentional), `go test ./internal/web/sse/...` payload-shape test. Spec: SK-006.

- [ ] **T2B.2 [BE]** Implement `SidekickEvent` on the TUI model: prefixed log-pane line (`[sidekick] ...`), no pane stealing, safe before program start (nil program guard like existing notifiers).
      Verify: `go test ./internal/tui/...` render/guard test; full `go build ./...` green again. Spec: SK-006.

### Track C - Session persistence (SK-007)

- [ ] **T2C.1 [BE]** Add `MessageKindSidekick = "sidekick"` to `internal/session/session_data.go`; verify save/load round-trip preserves kind and old sessions load unchanged.
      Verify: `go test ./internal/session/...` round-trip + backward-compat cases. Spec: SK-007.

- [ ] **T2C.2 [BE]** Context-inclusion policy: add `NotesInContext bool` (`notes_in_context`, default false) to `SidekickConfig` (spec SK-007 addition) and make history rebuild skip kind=sidekick messages unless opted in; UIs still render them.
      Verify: `go test ./internal/config/... ./internal/context/...` excluded-by-default + opted-in cases. Spec: SK-007.

---

## Phase 3 - Agent integration (after Tracks A+B; SK-004/SK-005 parallelizable)

- [ ] **T3.1 [BE]** Create `internal/agent/watcher.go`: `runWatcher` with per-run transcript buffer (window-capped per D7/T0.2: original request + last K events), append-only `AfterModelCallback` returning `(resp, err)` unchanged, inert (no goroutine) when mode is off.
      Verify: `go test ./internal/agent/ -run TestWatcher` callback purity (resp/err untouched), inert-off-mode case. Spec: SK-004.

- [ ] **T3.2 [BE]** Watcher evaluator goroutine: debounced signal (default 20s), per-run eval budget (default 5), ctx child of the run (cancel-safe), calls `sidekick.Evaluate`, on GapFound pushes truncated note into queue + emits `SidekickEvent("note", ...)` via Runtime notifier; clears queue at run end; late notes dropped with debug log.
      Verify: watcher tests with stub LLM: one note per window despite N calls, failure-degrades-to-log, budget stop, race detector clean (`go test -race ./internal/agent/`). Spec: SK-004.

- [ ] **T3.3 [BE]** Register the watcher in `SetupRunner` (`agent.go`) on the ROOT orchestrator only: append to `BeforeModelCallbacks` sibling list position (`AfterModelCallbacks`), construct queue+watcher per run, wire Runtime notifier. Delegated sub-agents remain unwatched.
      Verify: `go test ./internal/agent/` instruction/tool wiring regression; sub-agent configs contain no sidekick callbacks. Spec: SK-004.

- [ ] **T3.4 [BE]** Create `internal/agent/sidekick_tool.go`: `ask_sidekick` function tool per SK-005 contracts (synchronous, `AskTimeoutSeconds`-capped, unavailable => descriptive error string, answer size-capped); add `ask_sidekick` to `blockedTools()` for delegation isolation.
      Verify: `go test ./internal/agent/` happy path, timeout path, disabled-mode guidance string, blockedTools test updated. Spec: SK-005.

- [ ] **T3.5 [BE]** Conditional orchestrator instruction: `### SIDEKICK CONSULTATION:` section in `buildOrchestratorInstruction` (when to consult: high-stakes decisions, pre-destructive actions, risky claims; answers are advisory input) rendered only when enabled; register tool in `orchestratorTools`.
      Verify: `go test ./internal/agent/` instruction contains section iff enabled; tool count assertion. Spec: SK-005.

---

## Phase 4 - Surfaces

### Track A - TUI command (SK-008)

- [ ] **T4A.1 [BE]** `/sidekick <question>` slash command in `internal/tui/slash.go`: usage + availability message on empty arg, own context (never blocks/cancels the main run), answer streams into chat pane with sidekick prefix; listed in `/help`.
      Verify: `go test ./internal/tui/...` dispatch available/unavailable/disabled paths. Spec: SK-008.

- [ ] **T4A.2 [BE]** Persist `/sidekick` answers as `MessageKindSidekick` (role assistant) bound to the active session; resume renders them distinctly; busy-run interleave verified.
      Verify: `go test ./internal/tui/...` persistence + resume render; manual busy-interleave smoke. Spec: SK-008, SK-007.

### Track B - Web backend (SK-009)

- [ ] **T4B.1 [BE]** Create `internal/web/handlers/sidekick.go`: `POST /api/sessions/{id}/sidekick` `{question}` -> 202 (answer arrives via SSE + persisted message), `GET /api/sidekick/status` `{enabled, mode, model, provider}` (never api_key/base_url creds); register inside auth group; 1 in-flight ask per session rate limit (not consuming an agent-run slot).
      Verify: `go test ./internal/web/...` auth-required, 400 empty question, 429 concurrent overflow, status redaction. Spec: SK-009.

- [ ] **T4B.2 [BE]** Wire the ask path: goroutine runs `sidekick.Ask` detached from request lifecycle (mirroring chat.go run-context rule), streams completion via `SidekickEvent`, persists answer with kind sidekick to the URL-named session (misroute-proof like persistAgentResponse).
      Verify: `go test ./internal/web/...` SSE delivery integration test + persisted-message assertion. Spec: SK-009.

### Track C - Web UI (SK-010)

- [ ] **T4C.1 [FE]** Extend `webui/src/lib/api.ts` + SSE client typing: `sidekick` event union, `postSessionSidekick()`, `getSidekickStatus()`.
      Verify: `cd webui && pnpm build && pnpm test`. Spec: SK-010.

- [ ] **T4C.2 [FE]** SidekickNote chip component in `webui/src/components/chat/`: collapsible, accent color + icon per severity ONLY (assert NO notification dispatch for any severity - T0.3), hidden when status disabled.
      Verify: vitest dispatch/severity/no-toast assertions. Spec: SK-010.

- [ ] **T4C.3 [FE]** SettingsView sidekick section: enabled/mode (with implicit on_demand hint)/provider/model/base_url/key (write-only masked) + tuning numbers with defaults; privacy footnote per T0.4 (excerpts incl. tool output leave the machine; local `openai-compatible` option).
      Verify: vitest settings round-trip field mapping; `pnpm build` typecheck. Spec: SK-010.

- [ ] **T4C.4 [FE]** Ask affordance in ChatView (small menu/button next to send) posting to the sidekick endpoint; disabled state when status reports unavailable.
      Verify: vitest affordance enable/disable + POST call. Spec: SK-010.

---

## Phase 5 - Wiring + hardening

- [ ] **T5.1 [BE]** Add `Deps.ResolveSidekickProviderFn func(mainProvider LLMProvider, cfg *config.Config) LLMProvider` (mirror vision resolver); `SetupRunner` creates `sidekick.LLM` when enabled+model resolvable; failure => warn log + disable, never fatal.
      Verify: `go build ./...`; `go test ./internal/agent/` creation-success/failure/disable paths. Spec: SK-011.

- [ ] **T5.2 [BE]** `cmd/hakase/main.go`: implement `resolveSidekickProvider` (explicit provider > base_url-forces-openai-compatible > primary); boot log line `sidekick ready (mode)` or warning; pass through Deps.
      Verify: `go build ./...` default tag; boot-log unit/manual check. Spec: SK-011.

- [ ] **T5.3 [BE]** `cmd/hakase/web.go`: identical resolver + wiring parity for web/serve bootstrap (keep bootstrap logic in package main per cycle rule).
      Verify: `go build -tags dev ./...` + `go build ./...`; `hakase serve` smoke: status endpoint reflects config. Spec: SK-011.

- [ ] **T5.4 [QA]** README: Features bullet, `sidekick` block documentation, `HAKASE_SIDEKICK_*` env table rows, privacy note (T0.4), cost warning for watch mode.
      Verify: `grep -n "sidekick" README.md`; flags match implemented behavior exactly. Spec: SK-012.

- [ ] **T5.5 [QA]** `.agents/skills/hakase/SKILL.md` (+references): sidekick in architecture/configuration/troubleshooting sections; `CHANGELOG.md` Unreleased entry.
      Verify: skill validates; changelog entry present. Spec: SK-012.

- [ ] **T5.6 [QA]** Final gates: `go build ./...`, `go test ./...`, `go test -race ./internal/agent/ -run TestWatcher`, `cd webui && pnpm test && pnpm build`, `make build`; manual smoke: configure sidekick -> `/sidekick "..."` answers + persists; watch mode produces <= MaxNotesPerTurn notes during a research run.
      Verify: all commands exit 0; manual QA checklist below. Spec: all.

---

## Verification Commands

```bash
go test ./internal/config/...        # T1.1-T1.3, T2C.2
go test ./internal/sidekick/...      # T1.4-T1.6
go test ./internal/util/...          # T2A.1
go test ./internal/context/...       # T2A.2, T2C.2
go test ./internal/interfaces/... ./internal/web/sse/...  # T2B.1
go test ./internal/tui/...           # T2B.2, T4A.*
go test ./internal/session/...       # T2C.1
go test ./internal/agent/            # T3.* (+ -race once after T3.2)
go test ./internal/web/...           # T4B.*
cd webui && pnpm test && pnpm build  # T4C.*
go build ./... && go test ./...      # gates before merge (T5.6)
make build                           # embed check (T5.6)
```

## QA Matrix

| Scenario | Expected |
|---|---|
| Config absent / `enabled:false` | Zero behavior change: no extra model calls, no UI noise, `/help` may list nothing new |
| `{enabled:true, model_name:"x"}` no mode | EffectiveMode = `on_demand`; watcher never starts; no background API spend |
| `/sidekick "q"` idle (TUI) | Answer renders with sidekick prefix, persisted as kind=sidekick, survives resume |
| `/sidekick` while agent run active | Concurrent-safe; main run unaffected; answer interleaves |
| `ask_sidekick` with sidekick disabled | Descriptive guidance string returned to the model (no panic/error abort) |
| `ask_sidekick` exceeding timeout | Friendly error string; primary continues |
| Watch mode, N model calls in one window | Exactly one debounced evaluation; <= MaxNotesPerTurn SIDEKICK NOTEs injected |
| Watch mode, sidekick HTTP failure | Log line only; primary run latency unchanged |
| Note arrives after run end | Dropped with debug log; never leaks into next turn |
| Delegate_task sub-agent runs | No sidekick callbacks attached; `ask_sidekick` blocked |
| Same provider+model as primary | Allowed; cost warning logged once at startup |
| `GET /api/sidekick/status` | `{enabled,mode,model,provider}` only; no api_key/base_url material |
| POST ask x2 concurrently per session | Second gets 429; neither consumes an agent-run slot |
| SSE `sidekick` critical note | Chip color/icon changes; NO toast/notification dispatched |
| Old sessions without kind=sidekick | Load and render unchanged (backward compatible) |
| `HAKASE_SIDEKICK_MODEL` set | Overrides file value; wins precedence |
