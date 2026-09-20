# Spec: Agent-Written Auto-Memory

Feature: `auto-memory` (ROADMAP Tier 1, issue [#16](https://github.com/amurru/hakase/issues/16))
Status: implemented against this spec. See `plan.md` (sequence) and `tasks.md` (atomic tasks).

## Context and objective

Hakase has strong long-term machinery — the knowledge wiki, `recall_knowledge`
reflexion recall, and SkillOpt-Sleep's nightly harvest — but no continuous,
low-friction write path *between* nightly runs. Facts learned mid-conversation
("user prefers X", "this project's tests need Y", "user corrected me about Z")
currently live only in session transcripts until sleep mines them, if ever.

Auto-memory closes that gap with the primitive all three major 2026 harnesses
converged on: short typed notes the agent itself writes, persisted across
sessions, and injected back at session start. It also feeds SkillOpt-Sleep a
curated, continuously growing lesson source instead of raw transcript
archaeology.

## Decisions (with alternatives)

**D1 — Storage is one JSON leaf store, not markdown or per-project files.**
`~/.hakase/memory/notes.json`, mirroring `internal/channel/state` (in-mutex
cache, mtime/size re-check, flock + tmp+rename, 0600 file / 0700 dir) with
`internal/sleep`'s corrupt-file quarantine. Markdown (Claude Code's choice)
is friendlier to hand-editing but our house pattern for machine-managed state
is the leaf JSON store + CLI inspection (`hakase memory list/forget/add`),
which keeps the human-prunable property. Per-project files were rejected:
a `Project` field on each note plus read-time filtering gives the same scoping
with one file, one lock, one CLI. Corrupt file → quarantined to
`<store path>.corrupt-<UTC timestamp>.json` and the store restarts empty,
never wedges the agent.

**D2 — Scoping: every note carries the project root it was written under.**
`Note.Project` is stamped at write time (`project.RootFrom(ctx)` →
`project.CurrentRoot()` fallback → `""` = global); the tool takes no project
input. Injection includes notes whose `Project` is empty or equals the
current session's project root. Consequence, accepted for v1: a `user`
preference learned inside project A is scoped to project A; the CLI `add`
(empty `--project` = global) is the escape hatch for genuinely global facts,
and the deferred extraction pass can revisit. This matches how the harness
already scopes identity (`internal/project` resolves identity *to the path
itself* — no hash, no registry ID — so registry-bound sessions and local cwd
sessions on the same checkout share memory).

**D3 — Two tools, not four.** `remember` (create, or update when `id` is
passed — the agent has note IDs in its context from the injected block) and
`forget_memory` (delete by id). Separate create/update/delete/list tools like
knowledge were rejected: memory notes are one-liners, the injected block
already lists everything, and every tool schema costs tokens every turn.
Exact-duplicate writes (same category + project + content) bump `UpdatedAt`
instead of stacking copies — the cheap anti-bloat guard.

**D4 — Categories are a fixed enum: `user`, `feedback`, `project`, `lesson`**
(the Claude Code taxonomy, minus `reference` — reference material belongs in
the knowledge wiki, which has richer structure). Fixed enum keeps rendering
order stable and the web UI filter trivial. Invalid categories are rejected
by the tool with the allowed list in the error.

**D5 — Injection is a once-per-session user-role content block via
`HistoryBuilder.BeforeModelCallback`, not a system-prompt block.**
SetupRunner renders system-prompt blocks once per *process* (the web/serve
process keeps one runner across many sessions), so a prompt block would go
stale the moment session 2 starts. The callback fires per model call with
fresh session resolution (web chat, channel threads, TUI each resolve their
own session — same logic the history prepend uses). A
`memorySeen map[sessionID]bool` on the builder makes it once per session:
injected on the session's first model call, not re-paid on every turn of the
run. Placement follows the `ContextUpdateNotice` pattern: user-role content
at the head of the run's contents, after persisted history, before the
current run contents. Injection happens even when persisted history is empty
(brand-new session), so it sits *before* the early return on empty sessions.

**D6 — Budget caps, enforced at render time.** `memory.max_prompt_chars`
(default 4000) bounds the injected block; `memory.max_notes` (default 200)
bounds the store, enforced by `remember` with an error that names the
remedy (`forget_memory` first) rather than silent eviction — the agent
decides what to drop, same posture as the task store. Per-note content is
capped at 2000 chars by the tool. Rendering order within the cap: category
groups in fixed order (user, feedback, project, lesson), newest-updated
first inside a group; notes that do not fit are dropped from the tail and
the block says so (one line), so the model knows the list is truncated.

**D7 — Tools are orchestrator-only.** Delegated sub-agents do not get
`remember`/`forget_memory` (they are not added to the `BuildSubAgentTools`
fallback set). Memory authorship stays with the agent that sees the user's
intent; sub-agent noise is the standard bloat failure mode.

**D8 — Wire format and safety.** IDs are `mem_` + 16 hex chars
(`crypto/rand`). The store is a leaf package (`internal/memory` imports only
`internal/config`, `internal/util`, stdlib) so `internal/cli` and the web
handlers can use it without dragging the agent runtime in — same rationale as
`internal/channel/state`. Tool definitions live in `internal/agent`
(`internal/memory` must NOT import `internal/agent`: `internal/context` will
consume the rendered block via a provider func set by SetupRunner, and
memory → agent → context → memory would cycle).

**D9 — Deferred (v1 does not ship):** the optional session-end extraction
pass (cheap `summary_model` call proposing notes, reusing the sleep miner's
redaction) and a TUI `/memory` command. Both are additive; the store, tool
contract, and config already accommodate them. Recorded in the issue.

## Components

### `internal/memory` (new, leaf)

- `Note{ID, Category, Content, Project, CreatedAt, UpdatedAt}`;
  `State{Version, Notes}`; `Categories` = the D4 enum in render order.
- `DefaultPath()` = `<HakaseHome>/memory/notes.json`; `OpenDefault()` /
  `Open(path)` open per use — deliberately no process-wide singleton, so
  `$HAKASE_HOME` stays authoritative and tests stay deterministic.
- `Store`: `Get() (State, error)` (reloads when another process wrote),
  `Update(fn func(*State) error) error` (reload → mutate → save → refresh
  cache), `Add(note) (final Note, err)` (dedupe D3, content trim/cap,
  category validation), `Remove(id) (bool, error)`.
- Save: `MkdirAll 0700`, flock `<path>.lock` (`util.FlockExclusive`),
  write `path.tmp` 0600, rename. Corrupt JSON on load → quarantine (D1).
- `SelectForProject(state, root) []Note` — D2 filter.
- `RenderBlock(notes, maxChars) string` — D5/D6 format, `""` when no notes.

### `internal/config`

`Memory MemoryConfig` on `Config`; `MemoryConfig{Enabled *bool,
MaxPromptChars int, MaxNotes int}`; accessors `MemoryEnabled` (nil cfg/nil
pointer ⇒ **true** — the feature ships on; the off-switch is explicit, same
pointer-keeps-absent-distinguishable rationale as `system_env`),
`MemoryMaxPromptChars` (default 4000), `MemoryMaxNotes` (default 200);
`ApplyDefaults`/`Validate` in the LoadConfig sequence; env overrides
`HAKASE_MEMORY_ENABLED`, `HAKASE_MEMORY_MAX_PROMPT_CHARS`,
`HAKASE_MEMORY_MAX_NOTES`. `config.json.example` gains a documented
`memory` block.

### `internal/agent` (tools + wiring)

`remember.go`: `createRememberTools(cfg)` → `[]tool.Tool` via
`util.NewDocTool`:

- `remember` `{category, content, id?}` → upsert; returns the stored note
  (id + timestamps) so the agent can refer to it. Errors: unknown category,
  empty content, store full (D6), store I/O.
- `forget_memory` `{id}` → `{forgotten bool}`; unknown id is a clean
  `false`, not an error (idempotent cleanup).

Registered in `SetupRunner` next to the knowledge append (agent.go
`orchestratorTools`), conditionally — when `MemoryEnabled` is false the
tools are not created at all (the injected block also disappears, so the
model is never told about tools it lacks). SetupRunner also builds the
memory provider closure (`memory.Default()` → `SelectForProject` with
`project.RootFrom(ctx)`/`CurrentRoot` → `RenderBlock` under
`MemoryMaxPromptChars`) and hands it to the history builder.

### `internal/context` (injection)

`HistoryBuilder.SetMemoryProvider(fn func(agent.Context) string)` +
`memorySeen` set. In `BeforeModelCallback`: after session resolution, if a
provider is set, this session has not been served yet, and the block is
non-empty → inject `genai.NewContentFromText(block, RoleUser)` at the head
of the run's contents (same splice pattern as the context-update notice) and
mark the session. Provider nil / empty block / disabled → no injection, no
state change (a session that starts before the first note is written gets
its memory on its *next* session, which is the semantics D5 wants).

### `internal/web` + webui

- `handlers/memory.go`: `RegisterMemoryRoutes(r, store)` —
  `GET /api/memory` (all notes as DTOs; the panel shows the project column
  so the operator sees global vs project scoping), `DELETE /api/memory/{id}`.
  Registered in `spa.go` beside knowledge routes (same auth middleware
  blanket).
- `webui/src/views/MemoryView.vue`: list with category badge, project,
  content, updated time; delete button per note; empty state naming the
  feature. Route `/memory` + nav entry. API client functions + vitest.

### `internal/cli` (memory subcommand)

`hakase memory list [--all]` (default: current project + global, grouped by
category; `--all` includes other projects), `hakase memory forget <id>`,
`hakase memory add --category <cat> [--project <path>] <content...>`.
Leaf-store only — works while the server runs (cross-process flock), same
contract as `hakase channels`. Registered in `command.go` `init()`.

## Error handling

- Store full / unknown category / empty content: tool errors with the
  remedy spelled out (the model can act on it immediately).
- Corrupt store: quarantine + empty store (D1); writes rebuild it.
- Store I/O errors: tool returns the error verbatim (the agent reports it);
  injection provider swallows and logs (a read failure must never fail a
  model call).
- Disabled feature: no tools, no injection, web API still serves the file
  if it exists (pruning works even when disabled).

## Security notes

Notes are agent-authored from conversation content, so the block is wrapped
the way injected context is treated elsewhere in the callback (plain text,
no templating — ADK brace-quoted identifiers in user content are not
processed, unlike instruction text; AGENTS.md's canvas warning does not
apply here). Files are 0600/0700 under `~/.hakase`. No network, no new
dependencies.

## Testing

- `internal/memory`: save/load round-trip, 0600/0700 perms, tmp-file
  cleanliness, cross-process reload (mtime/size), corrupt-file quarantine,
  dedupe, cap+trim, category validation, project filter, render budget
  (truncate note at the tail, truncation notice line).
- `internal/agent`: tool runnable invocation (knowledge-tools test pattern,
  `isolateHome` for the store), upsert by id, forget idempotence, disabled
  config → no memory tools in the toolset.
- `internal/context`: injects on first call only (second call: absent),
  injects into a brand-new empty session, disabled/nil provider no-op,
  per-session isolation for two sessions on one builder.
- `internal/config`: defaults, nil-vs-false, env override precedence,
  validation.
- `internal/cli`: list/forget/add against an isolated home.
- `internal/web/handlers`: GET list, DELETE existing/unknown (knowledge
  handler test pattern).
- `webui`: MemoryView render + delete flow (vitest, jsdom).

## Acceptance criteria

- [ ] Agent can `remember` and `forget_memory` typed notes; they persist at
      `~/.hakase/memory/notes.json` (0600, atomic, flocked).
- [ ] A new session starts with a budget-capped memory block, once per
      session, scoped to the session's project + global notes.
- [ ] `memory.enabled: false` (or env) removes the tools and the injection.
- [ ] The web UI can inspect and prune memory; `hakase memory` can too,
      while the server is running.
- [ ] `gofmt -l`, `go vet ./...`, `go test ./...`, `pnpm test` green.
