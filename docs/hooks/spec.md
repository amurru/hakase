# Spec: Hooks — `PreToolUse` / `PostToolUse` (user-scope)

Let users run their own commands at the tool lifecycle — a `PreToolUse` hook
that can **block** a tool call, and a `PostToolUse` hook that observes the
result. This is the cheapest genuinely-useful harness capability: it reuses
ADK's central tool callbacks so one wiring point covers every tool (built-in,
MCP, git, fileops), and `PreToolUse` plugs into the risk/approval machinery
hakase already has.

Governing issue: amurru/hakase#20 (Tier-2 backlog item 3, "Hooks system").
Ecosystem reference: Claude Code hooks (the converged shape), with the
security posture hardened per the 2026 CVE record (see "Security decisions").

**Scope of this first cut (agreed):** `PreToolUse` + `PostToolUse`, **user-scope
only** (`~/.hakase/config.json`). Project-scope hooks and the fingerprint/trust
store are Phase 2 (see Non-goals). Rationale in plan.md "Critical path".

## Decisions

- **Command hooks only, exec form — never a shell string.** The handler
  `command` is an **argv array**; the runner uses `os/exec` with no shell. This
  sidesteps shell-injection-through-config entirely and matches Claude Code's
  exec form (the `args`-present path). `shell: true` is not implemented in v1.
- **Riding ADK's tool callbacks, not wrapping tools.** ADK v2.4.0's
  `llmagent.Config` exposes `BeforeToolCallbacks` / `AfterToolCallbacks`
  (`agent/llmagent/llmagent.go:324,330`). `Flow.callTool`
  (`internal/llminternal/base_flow.go:1374`) invokes `BeforeToolCallback`
  first and **skips `tool.Run` when it returns a non-nil result**. One wiring
  point on each of the five `llmagent.New` sites in `internal/agent`
  (orchestrator + `web_researcher` + `code_interpreter` + `general_purpose`
  + the per-delegation ephemeral sub-agent in `delegate.go`)
  covers every tool, including MCP toolsets. No per-tool wiring, and the
  existing per-tool approval gate (`sandbox/systemexec.go:285-367`) still runs
  underneath when a hook allows the call.
- **Block = non-nil callback result.** A blocking `PreToolUse` returns a
  structured result (`{"error": "...", "blocked_by": "hook", ...}`) so the
  denial is legible to the model and lands in the audit/canvas like any other
  tool outcome. `AfterToolUse` cannot block (matches the ecosystem); it may
  append `additionalContext` to the result via the override channel.
- **Matcher = unanchored regex on the tool name**, Claude-compatible
  (`"system_exec"` matches any tool name containing it; anchor `^...$` for
  exact). Compiled once at config load; a bad regex is a config error.
- **Exit 2 = block (universal contract).** Hook stdout JSON (exit 0) may also
  carry `hookSpecificOutput.permissionDecision: "deny"` (and legacy
  `{"decision":"block"}`); stderr is the reason. Any other non-zero exit is a
  hook *error*, handled per `on_failure` (below).
- **Timeout default 30s** (Crush/Goose-style), not Claude's 600s — a blocking
  gate in an interactive harness must not hang a turn for minutes. Per-hook
  `timeout` (seconds) overrides; a timeout is treated as a hook error.
- **`on_failure: allow|block` per handler** (Goose's knob). `allow` (default)
  = fail-open on hook error, so a broken hook never wedges a session.
  `block` = fail-closed, for users who want the gate to be strict. Only
  meaningful on `PreToolUse`.

### Security decisions (hardened against the 2026 CVE record)

The Tier-2 roadmap originally cited Gemini CLI's "fingerprint-and-approve"
model. That model is **known-broken** (gemini-cli#27900: the trust key
`name:command` is forgeable, the dialog never lists hook commands, the trust
store self-fills, and the warning does not gate execution → one-click shell
from a repo `SessionStart` hook). Codex CLI's **content-hash** trust is the
model we adopt for Phase 2. For this user-scope-only cut there is no trust
surface to get wrong (you author your own `~/.hakase/config.json`), so the
hardening below is belt-and-braces and sets the pattern Phase 2 must follow:

- Content-address every hook by a hash of the **resolved argv + local script
  bytes** — never a name (CVE-2026-40068 `commondir`; gemini #27900).
- Gate execution on an **explicit accept**, not a warning (gemini #27900).
- Never let a trust decision depend on a repo-writable git config/worktree
  file (CVE-2026-72718 `core.fsmonitor`; CVE-2026-19590 `core.hooksPath`).
- Keep the hook config **read-only inside the sandbox** (CVE-2026-25725).
- Redact env passed to hooks by default (gemini ships this off — wrong
  default).

## Specs

### Spec HK-001: `internal/hooks` package

New leaf package (imports only stdlib + `internal/config` + `internal/interfaces`;
**never** `internal/agent`, matching the `internal/sleep` precedent).

- `Event` = `"PreToolUse" | "PostToolUse"` (constants; unknown names rejected
  at config load).
- `Config` mirrors the config block (below) with `Validate()` + `ApplyDefaults()`.
- `Runner` holds the compiled, validated hook set. Constructed once per process:
  `NewRunner(cfg HooksConfig) (*Runner, error)`. Nil/empty cfg → a runner that
  is a no-op (callbacks return `nil, nil`).
- `Runner.BeforeToolUse(ctx agent.Context, t tool.Tool, args map[string]any)
  (map[string]any, error)` — matches `llmagent.BeforeToolCallback`. Returns
  `nil, nil` to allow; a non-nil result blocks.
- `Runner.AfterToolUse(ctx agent.Context, t tool.Tool, args, result map[string]any,
  runErr error) (map[string]any, error)` — matches `llmagent.AfterToolCallback`.
  Returns `nil, nil` to pass the tool result through unchanged; a non-nil
  result **overrides** it (used to append `additionalContext`).

### Spec HK-002: hook payload (stdin) and verdict (stdout)

One JSON object on stdin, mirroring the Claude payload subset that applies:

```json
{
  "hook_event_name": "PreToolUse",
  "session_id": "<hakase session>",
  "invocation_id": "...",
  "tool_name": "system_exec",
  "tool_input": { ... },
  "cwd": "<project root>",
  "timestamp": "2026-..."
}
```

`session_id` resolved from the callback's `agent.Context` via
`interfaces.SessionIDFromCtx` (falls back to `TaskIDFromCtx`, then `""`).
`cwd` from `internal/project.Root`. Env: the parent environment **minus a
redaction denylist** (values matching `KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL`
are dropped), plus `HAKASE_HOOK_EVENT`, `HAKASE_SESSION_ID`, `HAKASE_PROJECT_DIR`.

Verdict parsing (exit-code first, then stdout JSON):

- **exit 0**: parse stdout as JSON if it starts with `{`. A
  `hookSpecificOutput.permissionDecision` of `deny`/`block` (or legacy
  top-level `decision: "block"`) blocks with
  `permissionDecisionReason`/`reason`. `hookSpecificOutput.additionalContext`
  is carried through on the block path (appended to the denial) and on
  PostToolUse (appended to the result). On the PreToolUse **allow** path it
  is logged server-side but NOT delivered to the model: ADK's
  BeforeToolCallback can only allow (`nil, nil`) or skip the tool
  (non-nil), so there is no channel for allow-path context. PostToolUse
  context is likewise suppressed when the tool itself **failed**: ADK drops
  the tool error whenever an AfterTool callback returns a non-nil result,
  so overriding there would convert the failure into a success — the error
  reaches the model intact and the context goes to the warn log instead.
  Unparseable stdout on exit 0 is a hook error (per `on_failure`).
- **exit 2**: block; `stderr` (trimmed, truncated to 500 runes) is the reason.
- **other non-zero / timeout / exec failure**: hook error → `on_failure`
  decides (`allow` = proceed, warn; `block` = deny with the error as reason).

Multiple matching handlers run **sequentially in config order**; the first
block wins and short-circuits the rest (deny-wins, deterministic). A timeout
kills the child process group.

### Spec HK-003: config `hooks` block

`HooksConfig` in `internal/config/config.go` (or a sibling `hooks.go` in that
package) following the `MemoryConfig` pattern:

```jsonc
"hooks": {
  "enabled": true,                     // *bool; nil/true = on, false = off entirely
  "PreToolUse": [{
    "matcher": "^system_exec$",        // unanchored regex on tool name
    "hooks": [{
      "name": "no-rm-rf",              // optional, for logs/CLI listing only
      "type": "command",               // only "command" in v1
      "command": ["/home/me/.hakase/hooks/no-rm-rf.sh"],  // argv, no shell
      "timeout": 30,                   // seconds, default 30
      "on_failure": "allow"            // "allow" (default) | "block"
    }]
  }],
  "PostToolUse": [ /* same shape */ ]
}
```

- `type` must be `command`; anything else is a config error (fail loud, the
  `landlock` precedent).
- Unknown event keys, non-command types, malformed argv, bad `on_failure`, and
  invalid matcher regex are **config-load errors** with actionable messages.
- `ApplyDefaults` fills timeout 30 and `on_failure: allow`; `Validate` runs in
  `LoadConfig` (tail, near the sandbox refusal) so a bad block fails startup.
- Accessor `HooksEnabled(c)` (nil-safe). Env override `HAKASE_HOOKS_ENABLED`
  (strict bool) in `envConfigSet`.
- **No env-var expansion of the command** in v1 (a hook that needs a path uses
  an absolute path or a small wrapper script) — keeps the trust surface and the
  config surface minimal.

### Spec HK-004: wiring into all five agents

In `internal/agent/agent.go`, build one `*hooks.Runner` from `cfg.Hooks`,
publish it on `Deps.HooksRunner` for the delegation path, then adapt it via
`hookToolCallbacks` (nil/disabled runner = nil slices = unchanged behaviour)
at each `llmagent.New` config:

```go
BeforeToolCallbacks: hookBeforeTool,
AfterToolCallbacks:  hookAfterTool,
```

The five sites: `web_researcher` (2155), `code_interpreter` (~2230),
`general_purpose` (~2280), `orchestrator` (2436), plus the per-delegation
ephemeral sub-agent in `internal/agent/delegate.go` (which reads
`Deps.HooksRunner` because it is built long after SetupRunner returns).
Because all five are wired, `PreToolUse` also sees tool calls made by
delegated sub-agents (Claude behaviour) — including `delegate_task`
runs, which would otherwise be a gate bypass. The `internal/agent` package
gains a direct import of `internal/hooks`; the runner is built from `cfg`
(no `Deps` bridge factory needed — hooks depend only on config, keeping
`internal/agent` decoupled).

Note: the existing `BeforeModelCallbacks` on some sites are preserved and
concatenated, not replaced.

### Spec HK-005: audit + CLI

- **Audit**: a blocking hook and a `PreToolUse` denial append to the existing
  exec-audit / toolresult-guard trail with `blocked_by: "hook"` and the hook
  `name`, so a hook denial is as traceable as a policy denial. Wired via the
  existing `internal/agent/audit.go` path (best-effort, never blocks the turn).
- **CLI**: `hakase hooks list` (dump the loaded, validated hooks and their
  fingerprints) in `internal/cli/hooks.go`. v1 is read-only — no `trust`
  subcommand (that is Phase 2 with project scope).

## Non-goals (deferred, tracked in tasks.md)

- **Project-scope hooks** (`.hakase/hooks.json`) and the content-hash trust
  store + explicit-accept gate (Codex model). Phase 2. This cut is
  user-scope-only specifically so the trust machinery can be built correctly
  rather than shipped broken like Gemini's.
- `SessionStart` / `SessionEnd` / `Stop` / `PreCompact` / `UserPromptSubmit`
  events. `SessionStart` in particular needs a non-ADK injection point (no
  `llmagent` callback) — separable, useful, but not in the first cut.
- `prompt` / `agent` (LLM-eval) hooks, `http` / `mcp_tool` handler types,
  `async`/background hooks, shell-string commands.
- TUI `/hooks` browser, web UI for hooks, editing hooks via chat.
- Per-hook `if` prefilter (Claude's permission-rule syntax).

## Definition of done

- [ ] A `PreToolUse` hook with `matcher: "^system_exec$"` and a script that
      exits 2 blocks a `system_exec` call, the model sees a clear denial, and
      the call never reaches the tool handler.
- [ ] A `PostToolUse` hook runs after a matching tool and can append context to
      the result.
- [ ] Hooks are **off by default** and a no-op when unconfigured; existing
      runs are byte-identical with no `hooks` block.
- [ ] Invalid hook config (bad regex, non-command type, bad `on_failure`) fails
      config load with an actionable error.
- [ ] `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test` green.
