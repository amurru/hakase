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

## Phase 2 (next arc, separate issue) — project scope + trust

Deliberately not in the first cut. Recorded here so it is not lost; tracked
against #20. Codex content-hash model (NOT Gemini's broken fingerprint — see
gemini-cli#27900).

- [ ] **T5.1 [BE]** project-scope `.hakase/hooks.json` loading, layered
      user → project.
- [ ] **T5.2 [BE]** content-hash trust store (`~/.hakase/hooks-trust.json`):
      fingerprint = hash(resolved argv + local script bytes); explicit
      `hakase hooks trust/untrust <name>` accept gate (no auto-trust, no
      warning-without-gate).
- [ ] **T5.3 [BE]** harden per the CVE record: trust never derived from
      repo-writable git config/worktree; hook config read-only inside the
      sandbox; managed/policy tier that lower tiers cannot disable.
- [ ] **T5.4 [BE]** `hakase hooks test <name>` dry-run + `SessionStart` event
      (needs a non-ADK injection point).
