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

- ~~`git_push` / `git_pull` / `git_clone`~~ — **shipped 2026-09-03 (v2.1)**:
  decisions D9-D12 below. Remote/credential handling: prompts fail fast via
  `GIT_TERMINAL_PROMPT=0`; real remotes need host-side credentials or an
  agent-approved `gh auth` flow, exactly like system_exec would.
- `git_checkout` / `git_reset` / `git_clean` — destructive; need explicit
  force-flag policy mapping onto the existing HIGH-risk rules (v2.2, pending).
- `git_stash` / `git_rebase` / `git_merge` — workflow depth beyond the
  commit loop; `git_pull` is deliberately `--ff-only` so merges/rebase stays
  an explicit user-approved system_exec action.
- `git_commit --amend` / signing / GPG key handling.
- `git_remote` / `git_tag` management.

## 6. v2.1 decisions (remote operations, 2026-09-03)

- **D9 (clone sources)** — `git_clone` accepts explicit URLs with schemes
  https/http/git/ssh. `file://` URLs and scheme-less local paths read the
  host filesystem directly and would bypass the sandbox read roots, so they
  are accepted only when no sandbox is active (local runs with the user's
  filesystem trust); an active sandbox keeps clone sources strictly remote.
- **D10 (clone target)** — The target directory resolves through write
  containment (`taskResolve(write=true)`), git runs with the target's parent
  as working directory and receives the bare directory name, so the URL is
  the only remote-controlled token in argv. `--branch` input passes a
  name-charset check.
- **D11 (no force push)** — `git_push` never passes force flags: the
  HIGH-risk `push --force` path stays a system_exec + approval concern
  (gate.go already classifies it HIGH). The tool exposes optional
  `--set-upstream` and explicit remote/branch (name-validated).
- **D12 (ff-only pull)** — `git_pull` always runs `--ff-only`: an agent must
  never silently create merge commits. Diverged branches error out and the
  model proposes an explicit rebase/merge workflow through system_exec with
  the user's approval. Push/pull surface `NotARepo` like `git_commit` does.

## 7. v2.2 decisions (destructive operations, 2026-09-03)

Gate semantics that make these tools safe: `classifyGitRisk` already elevates
`checkout --force/-f`, `reset --hard`, and `clean -fdx` combinations to HIGH,
and HIGH always maps to an approval ask (never silent run, never auto-deny
outside configured permissions/deny-patterns). The tools below therefore
never add their own policy vocabulary - they expose flags and let the
existing gate classify the exact argv, so the approval card shows the real
git command.

- **D13 (checkout scope)** — `git_checkout` supports branch switching
  (`create=true` maps to `-b`) and single-path restore (`checkout -- <path>`);
  the two modes are mutually exclusive and both require their argument. The
  tool never passes `-f`/`--force`: force-discard stays a system_exec HIGH-ask
  action, and without it git itself refuses destructive switches (dirty
  conflicting files) so the working tree is protected by git, not by us.
  Restore paths must be repository-relative with no traversal/absolute/NUL
  shapes.
- **D14 (reset scope)** — `git_reset` exposes `mode` soft|mixed|hard
  (default mixed) and an optional ref (default HEAD, charset-validated).
  `--hard` is passed through verbatim so the gate classifies it HIGH and the
  approval card shows `git reset --hard ...`; soft/mixed stay MEDIUM. No
  `--keep`/`--merge` variants in v1 (system_exec domain).
- **D15 (clean scope)** — `git_clean` always passes a removal flag (`-f`, or
  `-n` for `dry_run=true` which removes nothing and only lists what would be
  removed). `include_dirs` and `include_ignored` map to `-d`/`-x`; their
  combined form trips the existing HIGH classifier, anything else stays
  MEDIUM. Only untracked files are ever touched - tracked history and
  worktree content are never at risk from this tool. Optional `paths` filter
  (repo-relative, traversal-checked) narrows the scope; without it git clean
  considers the whole untracked tree, which the approval card makes visible.
- **D16 (still out of scope)** — stash/rebase/merge, `commit --amend`,
  signing, and remote/tag management remain system_exec domain: they either
  rewrite history or need interactive workflows the structured tools should
  not paper over.
