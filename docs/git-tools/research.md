# Research: Structured Git Tools

Feature: `git-tools`. Companion documents: `spec.md` (atomic specs), `plan.md` (phases).
Date: 2026-09-01. Status: research complete, decisions recorded.

## 1. What the feature is

Structured `git_*` tools for the orchestrator and general-purpose agent so the
model can inspect and mutate a repository without hand-crafting `system_exec`
invocations: `git_status`, `git_diff`, `git_log`, `git_branch` (read-only) and
`git_stage`, `git_commit` (mutating). Arguments become typed inputs, output
comes back parsed/structured, and every mutation still flows through the exact
same harmful-command policy, approval gate, path-audit, and audit log that
`system_exec` uses today.

## 2. Prior art surveyed

| Pattern | Where seen | Takeaway for hakase |
| --- | --- | --- |
| File-ops toolset | `internal/sandbox/fileops.go` (`CreateFileOpsTools`) | The exact tool-shape to mirror: typed input/output structs with `doc:` tags, `util.NewDocTool`, output wrapped via `WrapUntrustedDataFunc`, package-level testable handler functions. |
| Harmful-command policy | `internal/agent/gate.go` (`EvaluateCommand`, `classifyGitRisk`) | Git is already classified: `status`/`log`/`diff`/`show`/`branch`/`remote` are LOW (allow), everything else MEDIUM (ask), `push --force`/`reset --hard`/`clean -fdx`/`checkout -f` are HIGH. Structured tools must inherit this, not fork it. |
| Command executor | `internal/sandbox/systemexec.go` (`BuildExecCommand`) | Gate + approval + `AuditSystemCommandPaths` + env scrub + bwrap wrapping + Windows hardening in one callable. Reusing it means zero new security surface. |
| go-git (pure Go) | general ecosystem | Not needed: the system `git` binary is present on every supported platform (Linux, macOS, Windows via `git.exe`), and reusing `BuildExecCommand` gives us the sandbox confinement, approval, and audit for free. A library would duplicate all of that. |
| Git MCP servers (e.g. `git-mcp`, `mcp-server-git`) | MCP ecosystem | Their tool shapes (`status`, `diff`, `log`, `add`, `commit`, `push`) are the de-facto standard; hakase's versions stay smaller (no `push`/`pull`/`clone` in v1) and are policy-controlled natively instead of as an opaque MCP server. |

## 3. Codebase integration points (verified)

### 3.1 Executor reuse

`internal/sandbox/systemexec.go:200` `BuildExecCommand(command, args, workingDir, env)`:

- runs the gate (`EvaluateCommandFunc` → wired in `cmd/hakase/init.go:31` to `agent.EvaluateCommand`),
- asks approval (`ApproveFunc` → `agent.ApproveExec`) when the decision is `ask`,
- audits every absolute/relative path token against sandbox read roots/deny roots (`AuditSystemCommandPaths`),
- scrubs `HAKASE_*`/`AWS_*`/`GITHUB_*`/`OPENAI_*` env and routes through `buildDirectCommand` (explicit argv form, no shell),
- wraps in bubblewrap when `sandbox.mode == bubblewrap`.

Passing the **repo directory as `workingDir`** (not `-C`) keeps argv shape
`["git", "status", "--porcelain=v1", "-b"]` so `classifyGitRisk` sees
`argv[1] = "status"` → LOW. With `-C` it would see `"C"` → MEDIUM (wrong class
for read-only ops, unasked approvals). Verified against `gate.go:261`.

### 3.2 Sync-execution pattern

`systemexec.go:866-921` is the pattern for running a built command
synchronously: `runtime.LockOSThread` around `Start`/`Wait` (Pdeathsig thread
requirement), `EffectiveExecTimeout`, `AfterFunc` tree-kill on timeout,
`attachProcessTree`/`releaseProcessTree` (Windows Job Object, no-op on Unix).

### 3.3 Sandbox globals

`internal/sandbox/sandbox.go:65,458,466`: `CurrentSandbox`, `EvaluateCommandFunc`,
`ApproveFunc`. `sandbox.go:236` `ResolveScopedPath(path, write)` is the path
confinement helper the repo-dir resolution must use. `fileops.go:432`
`wrapUntrustedData` shows how untrusted output is wrapped.

### 3.4 Agent wiring

`internal/agent/agent.go` `SetupRunner`: `fileOpsTools` and `systemExecTools` are
built via `sandbox.CreateFileOpsTools(...)` / `sandbox.CreateSystemExecTools(...)`
and appended to `orchestratorTools` (agent.go:2143-2146); `generalPurposeAgent`
gets `append(fileOpsTools, visionTool)`. Git tools follow the same pattern.

## 4. Key decisions

- **D1 (executor)** — Every git tool runs `git` through `BuildExecCommand`.
  One policy, one code path; the git tools add parsing/schema, not policy.
- **D2 (repo dir)** — The repo directory is passed as `cmd.Dir` via
  `workingDir` (sandbox-validated), never as `-C`, so git risk classification
  stays correct. Empty `repo_dir` defaults to the agent working directory
  (or the sandbox workspace root when pinned).
- **D3 (approval identity)** — `ApprovalRequest.Tool` stays `"system_exec"`
  because the operation *is* a system command; the approval card shows the
  full `git ...` argv. No parallel approval vocabulary.
- **D4 (scope)** — v1 ships `status`/`diff`/`log`/`branch` (read-only, LOW,
  no approval) + `stage`/`commit` (MEDIUM, approval-gated). `push`/`pull`/
  `clone`/`checkout`/`reset`/`clean` are deferred to v2 (network policy
  interplay and destructive semantics need explicit decisions).
- **D5 (no hang)** — `GIT_TERMINAL_PROMPT=0` is always set so credential/
  confirmation prompts fail instead of hanging the run.
- **D6 (bounded output)** — Parsed/raw output is capped (256 KiB combined,
  20k diff lines, 100 log entries default 20) with `truncated` flags, so a
  huge repo cannot blow the model context.
- **D7 (untrusted output)** — All repo-derived strings are wrapped via
  `WrapUntrustedDataFunc` (mirrors `read_file`), and parsed porcelain output
  falls back to raw lines on unexpected shapes.

## 5. Deferred to v2 (recorded, out of scope)

- `git_push` / `git_pull` / `git_clone` — need a decision on `allow_network`
  interplay and remote/credential handling (D4).
- `git_checkout` / `git_reset` / `git_clean` — destructive; need explicit
  force-flag policy mapping onto the existing HIGH-risk rules.
- `git_stash` / `git_rebase` / `git_merge` — workflow depth beyond the initial
  commit loop.
- `git_commit --amend` / signing / GPG key handling.
- `git_remote` / `git_tag` management.
