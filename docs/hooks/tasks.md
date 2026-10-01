# Tasks: Hooks — PreToolUse / PostToolUse (user-scope)

User-configurable command hooks that can block (`PreToolUse`) and observe
(`PostToolUse`) tool calls. Spec: [spec.md](spec.md) — plan: [plan.md](plan.md).
House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — `internal/hooks` core

- [x] **T1.1 [BE]** `hooks.go`: `Event` constants, `Config`/`Group`/`Handler`
      types, `ApplyDefaults()` (timeout 30, `on_failure: allow`), `Validate()`
      (bad matcher regex / non-`command` type / bad `on_failure` are errors).
      Spec: HK-001, HK-003.
- [x] **T1.2 [BE]** `runner.go`: `NewRunner`; matcher compile + unanchored
      match; per-handler `os/exec` (no shell) with timeout + process-group
      kill; stdin payload JSON; env redaction (KEY/TOKEN/SECRET/PASSWORD/
      CREDENTIAL); verdict parse (exit 2 = block w/ stderr reason; exit-0 JSON
      `permissionDecision` deny/block, legacy `decision:block`,
      `additionalContext`); sequential deny-wins short-circuit. Spec: HK-002.
- [x] **T1.3 [BE]** runner check methods (`CheckPreToolUse` /
      `CheckPostToolUse` on `Runner`, taking `context.Context` so the package
      stays ADK-free) + thin `llmagent`-signature adapters in
      `internal/agent` (`makeHookBeforeToolCallback` /
      `makeHookAfterToolCallback`); block = non-nil result, allow = `nil,
      nil`; session id via `interfaces.SessionIDFromCtx` → `TaskIDFromCtx`
      → `""` (all recover-guarded); `cwd` via `project.Root`. Spec: HK-001,
      HK-002.
- [x] **T1.4 [QA]** runner unit tests: verdict matrix (exit 2 / exit-0 deny /
      legacy / garbage stdout), matcher (exact / `^...$` / substring / miss),
      deny-wins ordering across handlers, timeout kill, env redaction,
      no-op-on-empty runner. Spec: HK-002.

## Phase 2 — config

- [x] **T2.1 [BE]** `internal/config`: `HooksConfig` (`enabled` tri-state +
      `PreToolUse`/`PostToolUse` groups), accessors (`HooksEnabled`),
      `HAKASE_HOOKS_ENABLED` env override in `envConfigSet`, `Validate()` wired
      into `LoadConfig`. Spec: HK-003.
- [x] **T2.2 [QA]** config tests: defaults, bad regex/type/on_failure fail
      load, `enabled:false` no-op, env override. Spec: HK-003.

## Phase 3 — wiring

- [x] **T3.1 [BE]** `internal/agent/agent.go`: build `*hooks.Runner` from
      `cfg.Hooks`, publish on `Deps.HooksRunner`; add
      `BeforeToolCallbacks`/`AfterToolCallbacks` (via the shared
      `hookToolCallbacks` helper — nil/disabled runner yields nil slices) to
      all four `llmagent.New` sites (researcher, code_interpreter,
      general_purpose, orchestrator), concatenating with existing callbacks;
      `internal/agent/delegate.go`: same callbacks on the per-delegation
      ephemeral sub-agent (read from `Deps.HooksRunner`), closing the
      `delegate_task` gate bypass. Spec: HK-004.
- [x] **T3.2 [BE]** `internal/agent/audit.go`: record hook denials
      (`blocked_by: "hook"` + hook name) on the audit trail, best-effort.
      Spec: HK-005.
- [x] **T3.3 [QA]** wiring tests: a `PreToolUse` exit-2 hook on `^system_exec$`
      short-circuits `tool.Run`; an allow returns `nil, nil` and the tool runs;
      no `hooks` block = unchanged behaviour; the shared
      `hookToolCallbacks` helper yields nil slices for nil/disabled runners
      and an enforcing gate for enabled ones (the contract `delegate.go`
      relies on). Spec: HK-004.

## Phase 4 — CLI + docs

- [x] **T4.1 [BE]** `internal/cli/hooks.go`: `hakase hooks list` (read-only
      dump of loaded hooks + content fingerprint); register in dispatcher.
      Spec: HK-005.
- [x] **T4.2 [DOCS]** CHANGELOG entry + README/docs mention; roadmap Tier-2
      hooks item marked in-progress.
- [ ] **T4.3 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...`, `cd webui && pnpm test`. Status 2026-09-29: Go side
      green locally (36/36 packages, incl. the new hooks/config/agent/cli
      tests); `pnpm test` could not run on this host (npm registry fetch of
      the mermaid tarball times out; `pnpm install` aborts before vitest).
      Zero `webui/` files are touched by this change, so CI is the backstop
      — confirm green there before merge.

## Phase 2 (this arc) — project scope + trust + SessionStart

Governing model: Codex content-hash trust (NOT Gemini's broken
fingerprint — see gemini-cli#27900). Spec: Phase-2 section in
[spec.md](spec.md) (HK-101..HK-105).

- [x] **T5.1 [BE]** project-scope `.hakase/hooks.json` loading, layered
      user → project: `ProjectHooksPath` / `LoadProjectFile` (strict keys,
      no `enabled`, symlink-escape guard, relative commands rooted at the
      project), per-turn root resolution with per-root mtime cache.
      Spec: HK-101, HK-103.
- [x] **T5.2 [BE]** content-hash trust store (`~/.hakase/hooks-trust.json`,
      0600, atomic rename under `.lock` flock, mtime-cached reads):
      fingerprint = hash(resolved argv + local script bytes); explicit
      `hakase hooks trust/untrust` accept gate (no auto-trust, no
      warning-without-gate); script-body rewrite lapses trust.
      Spec: HK-102.
- [x] **T5.3 [BE]** harden per the CVE record: `hooks-trust.json` in
      `sensitiveFilePaths()`; write-deny (reads allowed) for
      `*/.hakase/hooks.json` in `ResolveScopedPath` pre- and post-resolve;
      project files cannot disable user hooks or the trust gate (shape
      enforced). Full MDM tier explicitly deferred (no MDM infra exists;
      see spec). Spec: HK-105.
- [x] **T5.4 [BE]** `hakase hooks test <name|prefix>` dry-run (sample
      payload, printed verdict; warns loudly on untrusted project hooks)
      + `SessionStart` event: user + trusted-project groups, once per
      session via `HistoryBuilder` provider slot (reserve/rollback mirror),
      plain-stdout-is-context contract, exit 2 warns. Spec: HK-104, HK-105.
- [x] **T5.5 [QA]** project/trust/layer/session/sandbox-deny/CLI/config
      tests (skip-on-windows for shell spawns; portable missing-binary
      and pure-parse rows run everywhere). Spec: HK-101..105.
- [x] **T5.6 [DOCS]** spec Phase-2 section, tasks, `config.json.example`
      (`project.enabled`, SessionStart sample), DEVELOPMENT.md
      (hooks bullet + env table), README (CLI row), CHANGELOG entry,
      roadmap Tier-2 hooks note.
- [x] **T5.7 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...` (36/36 packages, plus `-race` on hooks/agent and
      windows cross-compile). `pnpm test` could not run on this host (npm
      registry fetch times out; pre-existing environmental issue, same as
      the Phase-1 note). Zero `webui/` files touched — CI is the backstop.

## Phase 3 (gap-fill arc) — UserPromptSubmit + surfaces + guide

Spec: Phase-3 section in [spec.md](spec.md) (HK-106..HK-108). Guide:
[usage.md](usage.md) + `examples/` scripts.

- [x] **T6.1 [BE]** `updatedInput` loud-degradation: `ignoredUpdate` flag
      on the verdict, runner warn + dry-run annotation; SessionStart
      shares the path (`sessionContextFromStdout` triple return).
      Trust-disabled warning in `hakase hooks trust`; corrupt-store
      wording fix. Spec: HK-108.
- [x] **T6.2 [BE]** `UserPromptSubmit` event: config/project shape +
      validation (empty matcher, no `on_failure:block`), layered runner
      (`RunUserPromptSubmit`, no once-keying, `prompt` payload field),
      `HistoryBuilder` per-prompt slot keyed on message Sequence,
      `wireHookUserPrompt` agent wiring, CLI `list`/`test` coverage.
      Spec: HK-106.
- [x] **T6.3 [BE]** TUI `/hooks` read-only browser (`RunHooksCommand`
      func var, `hooksTUIReport` pure report fn); web API (`GET /hooks`,
      `POST /hooks/trust|untrust`, `agent.HooksRunner()` accessor,
      `agentrun.ProjectRoot`). Spec: HK-107.
- [x] **T6.4 [FE]** web Hooks page (`HooksView`, nav, `lib/hooks.ts`
      wrappers + `hooks.test.ts` vitest coverage).
      Spec: HK-107.
- [x] **T6.5 [DOCS]** `usage.md` user guide, four verified example
      scripts (`no-rm-rf`, `log-tool`, `session-start`,
      `prompt-context`), spec Phase-3 section, `config.json.example`
      UserPromptSubmit sample. Spec: HK-106..108.
- [x] **T6.6 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...` (36/36), `-race` on hooks/agent, windows
      cross-compile, e2e trust lifecycle, `pnpm test` (110/110 incl.
      the new `hooks.test.ts`) + `pnpm build` (`vue-tsc -b` + vite).
      Note: the sandbox network needed `--fetch-timeout 600000` for
      the 26 MB mermaid tarball (`pnpm install --fetch-timeout
      600000 --fetch-retries 5 --fetch-retry-maxtimeout 300000`); the
      chunk-size warning on `elk` is pre-existing.

## Phase 4 (management arc) — CRUD + reload

Spec: Phase-4 section in [spec.md](spec.md) (HK-109..HK-112).

- [x] **T7.1 [BE]** `Handler.Enabled *bool` (nil = on), Validate +
      strict-keys coverage, fingerprint-unchanged pinning test,
      `[disabled]` in CLI list / snapshots. Spec: HK-109.
- [x] **T7.2 [BE]** in-place `Runner.Reload` under RWMutex (trust/log/
      fired persist; invalid reload keeps old set); always-wire
      callbacks + always-install providers; update the nil/disabled
      wiring tests to the new contract. Spec: HK-110.
- [x] **T7.3 [BE]** `internal/hooks/manage.go` pure ops + prefix
      resolution (user layer only) + map-surgery `WriteUserHooks`
      (unknown keys survive; returns validated block); `agent.
      ReloadUserHooks` + SIGHUP reload in serve/TUI processes.
      Spec: HK-111.
- [x] **T7.4 [BE]** CLI `hooks add/rm/enable/disable/on/off` (+ SIGHUP
      hint). Spec: HK-112.
- [x] **T7.5 [BE+FE]** web `POST /api/hooks/user/*` + `/master`
      (mutate → reload → refreshed DTO) with handler tests; HooksView
      master toggle, enable switches, remove, add/edit forms;
      `hooks.test.ts` coverage for new wrappers. Spec: HK-112.
- [x] **T7.6 [BE]** TUI `/hooks` subcommands (`trust/untrust/enable/
      disable/rm/on/off/add/update/test/list`) reusing manage.go +
      the CLI review/confirm; in-process reload. Spec: HK-112.
- [x] **T7.7 [DOCS]** `usage.md` management section, spec/tasks boxes,
      `config.json.example` enabled-flag sample.
- [x] **T7.8 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...`, `-race` on hooks/agent/context, windows
      cross-compile, `pnpm test` (115/115) + `pnpm build`, e2e CRUD
      cycle via real binary (add → `test` BLOCK → disable by name →
      `[disabled]` in list → rm → empty). Unified name+prefix
      resolution (e2e caught CRUD rejecting names while `test`
      accepted them).
