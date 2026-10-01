# Execution Plan: Hooks — PreToolUse / PostToolUse (user-scope)

User-configurable command hooks at the tool lifecycle. `PreToolUse` can block a
tool call; `PostToolUse` observes the result. Rides ADK's central tool
callbacks so one wiring point per agent covers every tool, layered above (not
into) the existing per-tool approval gate. Command handlers only, exec form (no
shell), user-scope config only. Spec: [spec.md](spec.md).

## Phases

### Phase 1 — `internal/hooks` core

1. `internal/hooks/hooks.go`: `Event` constants (`PreToolUse`/`PostToolUse`),
   `Config`/`Handler`/`Group` types, `Validate()` (bad regex / non-command
   type / bad `on_failure` are errors), `ApplyDefaults()` (timeout 30,
   `on_failure: allow`). Spec: HK-001, HK-003.
2. `internal/hooks/runner.go`: `NewRunner`, matcher compile+match, per-handler
   exec with timeout + process-group kill, stdin payload marshal, verdict
   parse (exit 2 / exit-0 JSON `permissionDecision`), sequential deny-wins
   short-circuit, env redaction. Spec: HK-002.
3. `internal/hooks/callbacks.go`: `BeforeToolUse` / `AfterToolUse` matching the
   `llmagent` callback signatures; block = non-nil result; allow = `nil, nil`.
   Session id via `interfaces.SessionIDFromCtx` → `TaskIDFromCtx` → `""`.
   Spec: HK-001, HK-002.

### Phase 2 — config

4. `internal/config`: `HooksConfig` block + accessors + `HAKASE_HOOKS_ENABLED`
   env override + `Validate()` wired into `LoadConfig` (tail, beside the
   sandbox refusal). Spec: HK-003.

### Phase 3 — wiring

5. `internal/agent/agent.go`: build `*hooks.Runner` from `cfg.Hooks`; add
   `BeforeToolCallbacks` / `AfterToolCallbacks` to all four `llmagent.New`
   sites (researcher, code_interpreter, general_purpose, orchestrator),
   concatenating with existing `BeforeModelCallbacks`. Spec: HK-004.
6. `internal/agent/audit.go`: record hook denials (`blocked_by: "hook"`,
   hook name) on the existing audit trail, best-effort. Spec: HK-005.

### Phase 4 — CLI + docs

7. `internal/cli/hooks.go`: `hakase hooks list` (read-only dump + fingerprint).
   Register in the dispatcher. Spec: HK-005.
8. `docs/hooks/tasks.md` ticked; CHANGELOG entry; README/docs mention.

## Critical path

1 → 3 → 5 is sequential (core types → callbacks → agent wiring). 2 depends on
1. 4 (config) is independent of 1-3 but must land before 5 (agent reads
`cfg.Hooks`). 6, 7 need 5. Tests ride each phase.

First-cut boundary: **Phase 1-4 is the whole user-scope feature.** Phase 2's
trust store is intentionally *not* here (see Non-goals); user-scope hooks have
no untrusted-input surface.

## Verification baseline

- `internal/hooks`: verdict parsing (exit 2, exit-0 deny JSON, legacy
  `decision:block`, garbage stdout), matcher (exact/anchor/regex/miss),
  deny-wins ordering, timeout kill, env redaction, no-op when empty.
- Config: bad regex / type / on_failure fail load; env override; defaults.
- Wiring: a `PreToolUse` exit-2 hook on `^system_exec$` short-circuits the tool
  (assert `tool.Run` not called via a fake tool in `agent` or a runner unit
  test); an allow returns `nil, nil`.
- Regression: with no `hooks` block, the runner is a no-op and behaviour is
  unchanged (guard test on a no-op runner).
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **ADK callback signature drift** — pinned to adk/v2 v2.4.0; the callbacks are
  plain function types, so a future ADK change would fail at compile time, not
  silently. Mitigated by the wiring being 4 small, identical edits.
- **Blocking on every tool** — a `matcher: ""` `PreToolUse` hook fires for all
  ~60 tools; with 30s timeout a hung hook could stall a turn. Mitigated by the
  30s default (vs Claude's 600s) and the option to scope matchers narrowly.
- **Sub-agent coverage** — wiring all four agents means delegated tool calls
  are hooked (Claude parity). Confirmed intentional; noted in spec Decisions.
- **Env leakage** — mitigated by default-deny redaction of KEY/TOKEN/SECRET/
  PASSWORD/CREDENTIAL values (gemini ships this off, which is the wrong
  default).
- **Config-load strictness** — a malformed hook block now fails `LoadConfig`.
  Intentional (fail-loud, `landlock` precedent) but it is a behaviour change
  for anyone with a partial `hooks` block; no existing configs have one.
