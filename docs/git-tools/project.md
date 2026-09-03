# Project-Aware Sessions (project identity)

Feature: `project-aware`. Date: 2026-09-03. Status: implemented (v1).

## 1. Why

Structured git tools need something to anchor to. Running `git_commit` in
"whatever directory the agent happens to be in" is incoherent: nothing says
what repository the session is *about*, and the repo-state the model should
know (branch, status, recent commits) is invisible unless the model queries it
itself and hallucinates otherwise.

This feature promotes the git root from a walk bound for AGENTS.md/skill
discovery into a first-class **project identity** shared by the whole session,
and gives the orchestrator and general-purpose agents a compact **repo-state
snapshot** in the system prompt (the "repo awareness" pattern also used by
Hermes Agent and Claude Code).

Two concepts, deliberately kept separate:

| Concept | Question it answers | Where it lives |
| --- | --- | --- |
| **Workspace** | What may the agent touch? (confinement) | `SandboxConfig.WorkspaceRoots` |
| **Project** | What are we working on? (identity) | `internal/project` session root |

Conflating them would break both ways: a workspace can legitimately hold
several repositories, and a project is meaningful regardless of how strict
the sandbox is.

## 2. Behavior

At `SetupRunner` (once per process/session, next to the environment block):

1. `project.FindRoot(cwd)` walks up for `.git` (directory in a normal clone,
   file in a linked worktree) and falls back to the absolute cwd. The result
   is recorded via `project.SetCurrentRoot` so every agent and every
   tool call in the session shares one identity.
2. `sandbox.BuildGitWorkspaceBlock` runs two bounded git commands
   (`status --porcelain=v1 -b`, `log -n 3`) and renders a block:

   ```text
   ### GIT WORKSPACE (snapshot at session start - re-check with git_status/git_diff before acting on it):
   Root: /home/user/repo
   Branch: main -> origin/main (ahead 2, behind 1)
   Status: staged 1, modified 2, untracked 3, conflicts 0
   Recent commits (newest first):
     a1b2c3d subject line
   ```

   The block is a **snapshot with an explicit re-check note**, never a live
   view, so the prompt stays cacheable. The whole block is wrapped as
   untrusted data (every repo-derived line is attacker-controllable).
3. The block is appended to the instructions of exactly the agents that own
   the structured git tools: `orchestrator` and `general_purpose`
   (`web_researcher`/`code_interpreter` do not get it).
4. `GitWorkspaceBlockTokens` feeds the compaction reserve in
   `internal/context/context.go` next to `ContextBlockTokens`, so a snapshot
   cannot silently blow the token budget.

Git tools (`gitops.go` `resolveRepoDir`) default `repo_dir` in this order:

1. explicit `repo_dir` input (sandbox-scoped as before);
2. a pinned sandbox workspace root — an explicitly configured workspace
   (e.g. a web server pointed at a code directory) stays the deliberate
   default;
3. the session project root (sandbox-off runs, or when the project root sits
   inside the approved roots — `BuildExecCommand` rejects working directories
   outside the roots, so the project root is never forced through a sandbox
   it is not inside);
4. the process working directory.

## 3. Decisions

- **D-P1 (derived identity)** - The project is derived from git (upward walk),
  never stored. No registry, no config, no new JSON. This matches the local
  runtimes: TUI and local web run where the code is, so the repo under the
  launch cwd *is* the project. A stored "registered projects" model is a
  remote-web concern (the client's repo is not on the host) and is deferred;
  it will need clone/push tooling (v2 git scope) alongside it.
- **D-P2 (workspace vs project)** - Workspace stays confinement. Pin
  workspace roots to the project root was considered and rejected for v1:
  the sandbox branch of `resolveRepoDir` keeps configured roots authoritative,
  the default `["."]` root already equals cwd, and silently widening or
  narrowing confinement per-session changes file-tool semantics. Revisit only
  with an explicit per-session workspace story.
- **D-P3 (untrusted output)** - The whole snapshot block is wrapped once via
  `wrapUntrustedData`; the porcelain parser now returns raw values and the
  `git_status` tool handler wraps per-field (output unchanged, wrapping just
  moved so the snapshot can consume raw counts).
- **D-P4 (non-repos are fine)** - No git root -> no block, and git tools keep
  their existing `NotARepo` behavior. The block only appears for actual
  repositories; no fake project-ness is invented for plain directories.
- **D-P5 (best effort)** - A git failure, a missing `git` binary, or a slow
  repo logs a warning and boots without the block. The snapshot is context,
  never a hard dependency (20 s timeout).

## 4. File map

- `internal/project/project.go` - `FindRoot` (canonical git-root walk) and
  the session-root globals. `skill.FindProjectRoot` now delegates here; the
  `hctx.FindProjectRootFunc` hook in `cmd/hakase/init.go` keeps working
  unchanged.
- `internal/sandbox/gitops.go` - `resolveRepoDir` project default;
  `parsePorcelainStatus` now also returns `upstream` and raw values;
  `countGitEntries`; `BuildGitWorkspaceBlock`.
- `internal/agent/agent.go` - `SetupRunner` sets the project root, builds the
  block, and appends it to the orchestrator/general-purpose instructions via
  `ContextBlockFor`.
- `internal/context/instruction_context.go`, `internal/context/context.go` -
  `GitWorkspaceBlockTokens` folded into the compaction reserve.

## 5. Verification

- `go build ./...`, `go test ./...` green (remember `make build-frontend` on
  fresh clones).
- `go test ./internal/project/ ./internal/skill/ ./internal/sandbox/`
  covers: root walk incl. worktree `.git`-as-file and non-repo fallback;
  session-root globals; porcelain upstream/ahead/behind/detached parsing;
  entry-category counting (staged/modified/untracked/conflicts disjoint);
  block rendering on dirty/clean/non-repo trees; `resolveRepoDir` project
  default and cwd fallback.

## 6. Deferred (v2)

- Registered projects for remote web (registry + clone/pull/push).
- Refreshing the snapshot at session resume (it is per-process today, like
  the env block).
- Optional `workspace_roots` pinning to the project root behind explicit
  configuration.
