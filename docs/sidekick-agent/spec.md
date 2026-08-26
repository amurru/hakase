# Spec: Sidekick Mode (atomic specs)

Feature: `sidekick-agent`. Source of truth for scope. Companion: `research.md`
(decisions D1-D10), `plan.md` (phases). Specs SK-001..SK-012.
r2: decisions Q1-Q5 folded in (implicit on_demand default in SK-001,
severity chip rendering + privacy footnote in SK-010).

Verification baseline: `go build ./...`, `go test ./...`, `cd webui && pnpm test`.
Remember `make build-frontend` on fresh clones before Go commands.

---

## SK-001: Sidekick config block

- **Objective**: Add a `SidekickConfig` block to `internal/config` so users can
  select the sidekick model, its endpoint/credentials, and its operating mode,
  following the existing vision-model precedent.
- **Affected components**: `internal/config/config.go`, `config.json.example`,
  `internal/config/config_test.go`.
- **Contracts**:
  ```go
  type SidekickConfig struct {
      Enabled   *bool  `json:"enabled,omitempty"`    // nil = false
      Mode      string `json:"mode,omitempty"`       // "", "on_demand", "watch", "full"
      Provider  string `json:"provider,omitempty"`   // gemini | openai | openai-compatible
      ModelName string `json:"model_name,omitempty"`
      BaseURL   string `json:"base_url,omitempty"`
      APIKey    string `json:"api_key,omitempty"`
      // Watch tuning
      EvaluateDebounceSeconds int `json:"evaluate_debounce_seconds,omitempty"` // default 20
      MaxEvaluationsPerRun    int `json:"max_evaluations_per_run,omitempty"`   // default 5
      MaxNotesPerTurn         int `json:"max_notes_per_turn,omitempty"`        // default 2
      MaxNoteChars            int `json:"max_note_chars,omitempty"`            // default 1200
      TranscriptWindowChars   int `json:"transcript_window_chars,omitempty"`   // default 6000
      AskTimeoutSeconds       int `json:"ask_timeout_seconds,omitempty"`       // default 45
  }
  ```
  Field `Config.Sidekick SidekickConfig \`json:"sidekick,omitempty"\``;
  `ApplyDefaults()`-style accessor helpers; env overrides
  `HAKASE_SIDEKICK_MODEL`, `HAKASE_SIDEKICK_PROVIDER`, `HAKASE_SIDEKICK_BASE_URL`,
  `HAKASE_SIDEKICK_API_KEY` (env wins over file, house convention); mode
  validation rejects unknown values with an actionable error only when
  enabled; `EffectiveMode()` resolution (decision Q1-B, research D1):
  disabled or empty model -> `off`; enabled + non-empty model + empty mode ->
  `on_demand` (implicit enable never starts the watcher); explicit
  `watch`/`full` require only enabled+model.
- **Acceptance criteria**:
  - JSON round-trip + defaults tests pass (`go test ./internal/config/...`);
  - env override precedence test mirrors TestLoadConfigSummaryModel;
  - invalid mode errors; disabled+garbage does not error;
  - implicit-default test: `{enabled:true, model_name:"x"}` with no mode
    resolves to `on_demand`, and `Available()`-equivalent gating reflects it;
  - `config.json.example` documents every field including the implicit
    on_demand default.
- **Guardrails**: no behavior change when block absent (byte-identical
  effective config); keys never logged.
- **Dependencies**: none.

## SK-002: Core sidekick package

- **Objective**: Create `internal/sidekick`: model holder, provider resolution
  hook, single-prompt `Ask`, transcript evaluation `Evaluate`, structured
  verdict parsing, and the note/debounce policy. No UI or agent imports
  (mirrors `internal/vision` decoupling).
- **Affected components**: new `internal/sidekick/{sidekick.go,evaluate.go,policy.go}`
  (+ tests).
- **Contracts**:
  ```go
  type Observation struct {
      GapFound bool     `json:"gap_found"`
      Severity string   `json:"severity,omitempty"` // info | warning | critical
      Note     string   `json:"note,omitempty"`     // what the primary missed / should double-check
  }
  var LLM model.LLM                       // set by SetupRunner wiring; nil = disabled
  func Available() bool
  func Ask(ctx context.Context, question string) (string, error)          // on_demand path, timeout-capped
  func Evaluate(ctx context.Context, req string, transcript string) (*Observation, error) // watch path
  ```
  Evaluation prompt: strict JSON verdict, "report only genuine gaps (missed
  sub-questions, contradictions, ignored constraints); never invent work";
  response parsed leniently (strip fences), parse failure => error. Policy
  helpers: `Debounce`, per-run eval budget counter, note truncation to
  `MaxNoteChars`. All config read via a settable `CurrentConfig func() *config.SidekickConfig`
  hook (vision.go pattern) so tests can inject values.
- **Acceptance criteria**:
  - fake `model.LLM` (test stub streaming one text part) drives Ask and
    Evaluate happy paths; timeout path returns error without leaking goroutines;
  - malformed JSON verdicts fail cleanly; fenced JSON parses;
  - policy unit tests cover debounce, budget exhaustion, truncation;
  - `Available()` false when LLM nil or mode off; zero network in tests.
- **Guardrails**: package must not import internal/agent, internal/tui,
  internal/web (dependency direction: agent -> sidekick only); text-only -
  no tool construction.
- **Dependencies**: SK-001.

## SK-003: Note queue + HistoryBuilder injection

- **Objective**: Give the watch path a delivery channel into the running
  session: a bounded sidekick-note queue drained inside
  `HistoryBuilder.BeforeModelCallback`, reusing the USER INTERJECTION pattern.
- **Affected components**: `internal/util/sidekick_queue.go` (new),
  `internal/context/context.go`, `internal/context/context_test.go`.
- **Contracts**:
  ```go
  // util
  type SidekickNote struct { Text string; Severity string; At time.Time }
  type SidekickNoteQueue struct{ ... }           // mutex-bounded FIFO, cap = MaxNotesPerTurn*run-scoped
  func NewSidekickNoteQueue(max int) *SidekickNoteQueue
  func (q *SidekickNoteQueue) Push(note SidekickNote) bool // false when full/duplicate
  func (q *SidekickNoteQueue) DrainActive() []SidekickNote // snapshot for injection (non-consuming)
  func (q *SidekickNoteQueue) Clear()
  // framing (util)
  func SidekickNoteFraming(n SidekickNote) *genai.Content // "SIDEKICK NOTE (advisory, from a second model):" + wrapped body
  ```
  `HistoryBuilder.SetSidekickQueue(q)` + drain appended after the pending-user
  steering block in BeforeModelCallback. Notes persist across calls within a
  run (re-injected like interjections) and are cleared at run end by the owner.
- **Acceptance criteria**:
  - queue cap + duplicate-suppression (identical normalized text within TTL)
    unit-tested;
  - BeforeModelCallback test: injected note appears once per call as trailing
    user content with SIDEKICK NOTE framing and untrusted-data wrapping;
  - nil queue = no-op (existing tests stay green).
- **Guardrails**: notes are user-role data, not system-role (consistent with
  interjection approach); total injected chars capped per call.
- **Dependencies**: none hard (framing contract agreed with SK-002).

## SK-004: Orchestrator watcher (AfterModelCallback)

- **Objective**: While mode is `watch`/`full`, observe the orchestrator's
  responses during a run and periodically ask the sidekick whether something
  was missed; deliver observations via the note queue; stream status to the UI.
- **Affected components**: `internal/agent/watcher.go` (new),
  `internal/agent/agent.go` (SetupRunner registers the callback on rootAgent
  only), `internal/agent/watcher_test.go`.
- **Contracts**:
  ```go
  type runWatcher struct{ ... }                 // one per runner.Run invocation
  func (w *runWatcher) AfterModelCallback(ctx agent.Context, resp *model.LLMResponse, err error) (*model.LLMResponse, error)
  ```
  Behavior: append non-thought text + function-call names to a per-run
  transcript buffer (window-capped per SK-002 policy); always return
  `(resp, err)` unchanged; signal the evaluator goroutine (debounced,
  budget-limited, ctx child of the run). Evaluator calls
  `sidekick.Evaluate(originalRequest, transcript)`; on `GapFound`, pushes a
  truncated note into the queue and emits `SidekickEvent("note", ...)`.
  Run end (callback context done / final response) clears the queue.
- **Acceptance criteria**:
  - fake sidekick LLM: gap verdict produces exactly one queued note per turn
    window despite N model calls (debounce verified);
  - callback never mutates resp/err; eval failure logs and continues;
  - budget exhaustion stops evaluations mid-run;
  - watcher inert (zero allocations/goroutines) when mode off;
  - `go test ./internal/agent/ -run TestWatcher` green; race detector clean.
- **Guardrails**: registered ONLY on root orchestrator; never blocks the
  model-call path beyond O(transcript append); no replacement responses in v1;
  all events also emitted through Runtime notifier when wired.
- **Dependencies**: SK-001, SK-002, SK-003.

## SK-005: ask_sidekick consultation tool

- **Objective**: Register an orchestrator-only `ask_sidekick` function tool so
  the primary model can deliberately request a second opinion mid-task.
- **Affected components**: `internal/agent/sidekick_tool.go` (new),
  registration in `SetupRunner` orchestratorTools, instruction section in
  `buildOrchestratorInstruction`, `sidekick_tool_test.go`.
- **Contracts**:
  ```go
  type AskSidekickInput struct {
      Question string `json:"question" doc:"What to verify or brainstorm with the second model"`
  }
  type AskSidekickOutput struct {
      Answer string `json:"answer" doc:"The sidekick's independent take"`
  }
  // Tool name: ask_sidekick. Blocked for delegated sub-agents (add to blockedTools()).
  ```
  Synchronous, `AskTimeoutSeconds`-capped; unavailable => tool returns a
  descriptive error string (model adapts), not a panic. Instruction addition:
  when to consult (high-stakes decisions, before destructive actions,
  verification of risky claims) and that answers are advisory input, not
  instructions to blindly follow.
- **Acceptance criteria**:
  - happy-path tool test with stub LLM; timeout path returns friendly error;
  - disabled mode returns "sidekick is not configured" guidance;
  - `blockedTools()` includes ask_sidekick (delegate isolation test updated);
  - orchestrator instruction contains `### SIDEKICK CONSULTATION:` only when
    enabled.
- **Guardrails**: answer size-capped before returning to the model; audit via
  `util.DebugEvent`; no session persistence here (SK-007 owns storage).
- **Dependencies**: SK-002.

## SK-006: EventNotifier extension (TUI rendering)

- **Objective**: Extend the shared notifier contract with sidekick events and
  implement it in the TUI so observations appear live in the log pane.
- **Affected components**: `internal/interfaces/notifier.go` (or where
  EventNotifier lives), `internal/tui/model.go` + log-pane handling,
  `internal/web/sse/bridge.go`.
- **Contracts**:
  ```go
  // interfaces.EventNotifier gains:
  SidekickEvent(status, message string) // status: "evaluating" | "note" | "error"
  ```
  SSE bridge publishes `event: sidekick` `{status, message}`; TUI prints a
  prefixed line (`🤝 [sidekick] ...`) to the status/log pane.
- **Acceptance criteria**:
  - compile fails fast if any implementer misses the method (interface change);
  - bridge unit test: published payload shape matches contract;
  - TUI handler test (existing tui test harness patterns) renders the line.
- **Guardrails**: no blocking sends (reuse bridge publish semantics);
  message pre-truncated by sender.
- **Dependencies**: none hard; consumed by SK-004/SK-008/SK-009.

## SK-007: Session persistence of sidekick messages

- **Objective**: Persist sidekick outputs so they survive resume and render
  distinctly, without polluting ground-truth context by default.
- **Affected components**: `internal/session/session_data.go`,
  `internal/context/context.go` (history builder skip rule), tests.
- **Contracts**:
  ```go
  MessageKindSidekick = "sidekick"
  // role stays "assistant"; Kind distinguishes rendering + history policy
  ```
  History rebuild includes kind=sidekick messages ONLY when
  `sidekick.notes_in_context` is true (new config bool, default false);
  otherwise they render in UIs but are excluded from model history.
- **Acceptance criteria**:
  - round-trip save/load preserves kind; resume renders sidekick messages;
  - history-builder test: excluded by default, included when opted in;
  - old sessions without the kind load unchanged (backward compatible).
- **Dependencies**: none hard; used by SK-008/SK-009/SK-010.

## SK-008: TUI on-demand command (/sidekick)

- **Objective**: `/sidekick <question>` slash command runs the on-demand path:
  asks the sidekick directly, streams the answer into the chat pane as a
  distinct message, persists it (SK-007), works while idle or busy (queued
  behind the active run like other prompts? No - independent, concurrent-safe).
- **Affected components**: `internal/tui/slash.go` (command table + handler),
  `cmd/hakase/slash_commands.go` only if a bridge is required by the cycle
  rules, tests.
- **Contracts**: `/sidekick` with empty arg prints usage + availability state;
  answers render with a sidekick prefix/badge; command is listed in `/help`;
  concurrency: uses its own context, never cancels or blocks the main run;
  while a run is active the answer still lands in chat pane interleaved.
- **Acceptance criteria**:
  - command dispatch test: available/unavailable/disabled paths;
  - answer persisted with MessageKindSidekick and re-rendered on resume;
  - `go test ./internal/tui/...` green.
- **Guardrails**: no tools, no delegation from this path; output size-capped
  for pane rendering with full text persisted.
- **Dependencies**: SK-002, SK-006, SK-007.

## SK-009: Web backend endpoints + SSE

- **Objective**: Expose sidekick capabilities over the web API: on-demand ask
  endpoint, availability status in the existing config/status surface, and
  `sidekick` SSE events flowing from watcher + ask path.
- **Affected components**: `internal/web/handlers/sidekick.go` (new) +
  route registration in server setup, `internal/web/handlers/chat.go`
  (persist ask answers to the right session), `sse.EventBridge` (SK-006),
  `internal/web/server_test.go` additions.
- **Contracts**:
  ```
  POST /api/sessions/{id}/sidekick   {question} -> 202 {status} ; answer arrives via SSE event + persisted message
  GET  /api/sidekick/status          -> {enabled, mode, model, provider}  // never api_key/base_url creds
  ```
  Auth: same middleware group as other routes. Concurrency: reuse the
  per-session run semaphore discipline (ask does not consume an agent-run slot
  but is rate-limited, e.g. 1 in-flight ask per session).
- **Acceptance criteria**:
  - handler tests: auth-required, 400 on empty question, 409/429 on
    concurrent ask overflow, status redacts credentials;
  - SSE integration test shows `sidekick` event delivery;
  - persisted answer appears in GET session messages with kind sidekick.
- **Dependencies**: SK-002, SK-006, SK-007.

## SK-010: Web UI surface

- **Objective**: Surface sidekick activity in the SPA: live badges/notes in
  ChatView, an on-demand ask affordance, and a Settings section.
- **Affected components**: `webui/src/views/ChatView.vue`,
  `webui/src/components/chat/*` (new SidekickNote bubble component),
  `webui/src/lib/api.ts` + sse client event typing, `SettingsView.vue`,
  stores as needed; vitest coverage.
- **Contracts**: SSE `sidekick` event -> collapsible "Sidekick note" chip in
  the transcript; severity maps to color + icon ONLY (decision Q3/D8 - no
  toasts, sounds, or notification pings for any severity); Settings section
  edits the `sidekick` config block through the existing config update API
  (enabled/mode/provider/model/base_url/key + tuning numbers with defaults
  shown, including the implicit on_demand default noted under the mode field);
  a privacy footnote in the Settings section states that sidekick requests
  send conversation excerpts (incl. tool output) to the configured endpoint,
  with local models via `openai-compatible` as the privacy-preserving option
  (decision Q4/D9); ask affordance (small button/menu next to send) posts to
  `/api/sessions/{id}/sidekick`.
- **Acceptance criteria**:
  - vitest: SSE dispatch creates a note entry; settings round-trip maps fields;
  - severity renders as color/icon class only (no notification dispatch call
    for any severity - asserted in test);
  - hidden entirely when status reports disabled (no dead UI);
  - `pnpm test` green; `pnpm build` (vue-tsc) green.
- **Guardrails**: match existing design system (Tailwind tokens, reka-ui);
  no new dependencies; key field write-only (masked, never echoed).
- **Dependencies**: SK-009.

## SK-011: Bootstrap wiring (TUI + web)

- **Objective**: Wire everything in both entry points without creating import
  cycles, following the Deps factory pattern.
- **Affected components**: `cmd/hakase/main.go` (runTUI),
  `cmd/hakase/web.go` (runWeb/runServe), `internal/agent/deps.go`,
  `internal/agent/agent.go` (SetupRunner consumes deps).
- **Contracts**: `Deps.ResolveSidekickProviderFn func(mainProvider LLMProvider,
  cfg *config.Config) LLMProvider` mirroring the vision resolver;
  `resolveSidekickProvider` implemented in both bootstraps (explicit provider >
  base_url-forces-openai-compatible > primary). SetupRunner creates
  `sidekick.LLM` when `cfg.Sidekick` enabled + model resolvable (failure =
  warn + disable, never fatal); registers watcher + tool per modes; Runtime
  notifier already carries SidekickEvent via SK-006.
- **Acceptance criteria**:
  - `go build ./...` green both tags (`dev` included);
  - with sidekick configured, TUI boot log shows one "sidekick ready (mode)"
    line; misconfigured shows warning and continues;
  - web bootstrap parity: same behavior under `hakase web`;
  - e2e smoke: `hakase serve` + scripted POST ask returns streamed answer
    (manual or httptest-based).
- **Guardrails**: no new packages under repo root; keep bootstrap logic in
  cmd/hakase (cycle rule from AGENTS.md); HAKASE_* env scrubbing unaffected.
- **Dependencies**: SK-001..SK-006, SK-009 (compile-time).

## SK-012: Docs, self-skill, README

- **Objective**: Document the feature everywhere hakase features live.
- **Affected components**: `README.md` (feature section + config reference +
  env var table rows), `.agents/skills/hakase/SKILL.md` (+ references if
  needed), `config.json.example` (done in SK-001, cross-check),
  `CHANGELOG.md` entry.
- **Acceptance criteria**: README section covers modes, cost warnings, privacy
  note (second model receives conversation excerpts); self-skill mentions
  sidekick in architecture/troubleshooting; changelog added under Unreleased.
- **Guardrails**: docs match implemented flags exactly (no aspirational
  fields); plain-dash style per house writing rules.
- **Dependencies**: all above (docs last).

---

## Consolidated guardrails (cross-cutting)

- **Allowed**: text-only sidekick calls; async watch evaluations; advisory
  note injection via BeforeModelCallback; synchronous capped `ask_sidekick`;
  per-mode gating; fail-open degradation.
- **Forbidden**: sidekick executing tools or touching sandbox/workspace (v1);
  replacing/short-circuiting primary responses via callbacks; blocking the
  primary run on watch evaluations; logging API keys; injecting notes after
  run completion; adding Go files to repo root; new runtime dirs.
- **Constraints**: budgets from SK-001 knobs enforced in SK-002/003/004;
  untrusted-content wrapping on every prompt boundary crossing; interface
  changes compile-enforced; tests self-contained (temp dirs, isolateHome,
  fake LLM stubs, no network).
