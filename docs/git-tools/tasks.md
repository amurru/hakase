# Task List: Structured Git Tools

Feature: `git-tools` (r1 scope: read-only `git_status` / `git_diff` /
`git_log` / `git_branch` + mutating `git_stage` / `git_commit`; push/pull/
clone/checkout/reset/clean deferred to v2 per research D4).

Atomic, hand-offable tasks. Each references the governing spec in `spec.md`
and is sized to 1-3 tool calls + one verification step.
Date: 2026-09-01 (r1)

Legend: `[BE]` Go backend, `[QA]` test/docs. Status: TODO unless marked.

---

## Phase 1 - Foundation (serial)

- [x] **T1.1 [BE]** Add `internal/sandbox/gitops.go` scaffolding: constants
  (`gitMaxOutputBytes`, `gitMaxDiffLines`, `gitMaxLogDefault`, `gitMaxLogCap`,
  `gitExecTimeout`), `gitResult`, `resolveRepoDir(repoDir, write)` delegating
  to `CurrentSandbox.ResolveScopedPath` when the sandbox is active (write flag
  by mutability, default to cwd / workspace root), and
  `runGit(ctx, repoDir, args, write, log)` building argv `["git", ...args]`,
  passing repoDir as `workingDir` to `BuildExecCommand`, env
  `GIT_TERMINAL_PROMPT=0`, sync run with `runtime.LockOSThread`, timeout
  tree-kill, capped combined output with Truncated flag, nonzero exit → error
  with bounded stderr (wrapped).
      Verify: `go test ./internal/sandbox/ -run 'TestGit.*'` against a
      temp-dir repo: success, nonzero-exit error, truncation, sandbox
      confinement on a fake `CurrentSandbox`. Spec: GT-001.

## Phase 2 - Read-only tools

- [x] **T2.1 [BE]** `git_status` in `gitops.go`: input/output per GT-002,
      `git status --porcelain=v1 -b` (+ `--untracked-files=no` when
      `untracked=false`), `parsePorcelainStatus` handling `##` header
      (branch/ahead/behind), `XY path`, `??`, renames/copies; `NotARepo`
      detection on fatal stderr; strings wrapped via `wrapUntrustedData`.
      Verify: fresh repo, untracked, staged, rename, not-a-repo cases. Spec: GT-002.
- [x] **T2.2 [BE]** `git_diff`: `git diff --no-color [--staged] [-- path]`
      with 20k-line cap + Truncated; empty diff returns empty. Verify:
      unstaged vs staged, path filter, cap. Spec: GT-003.
- [x] **T2.3 [BE]** `git_log`: tab-separated pretty format
      (`%h%x09%an%x09%ad%x09%s`, `--date=short`), limit clamped to
      [1,100] default 20, optional `-- path`. Verify: newest-first, path
      filter, clamping. Spec: GT-004.
- [x] **T2.4 [BE]** `git_branch`: `git branch --list [--all]`, parse `*`
      current marker and detached-HEAD line. Verify: two branches + `--all`
      with a remote-tracking branch. Spec: GT-005.

## Phase 3 - Mutating tools

- [x] **T3.1 [BE]** `git_stage`: `git add -- paths...` / `git rm --cached -- paths...`
      (unstage), empty paths = argument error, MEDIUM risk flows through
      buildExecCommand → `ApproveFunc` (denied returns the not-approved
      error). Verify: stage, unstage, empty-paths error, approval-denied
      stub error. Spec: GT-006.
- [x] **T3.2 [BE]** `git_commit`: optional `git add -A -- .` pre-step,
      `git commit -m msg [--allow-empty]`, then `git rev-parse HEAD` +
      `git log -1 --pretty=%s` for the response; empty message = argument
      error; nothing-to-commit surfaces the git error. Verify: commit,
      stage_all, empty-message, nothing-to-commit. Spec: GT-007.

## Phase 4 - Wiring & docs

- [x] **T4.1 [BE]** `CreateGitOpsTools(log)` returning the six tools; append
      to `orchestratorTools` after `systemExecTools` and to
      `generalPurposeAgent` tools in `internal/agent/agent.go`. Verify:
      `go build ./...`, `go test ./...`. Spec: GT-008.
- [x] **T4.2 [QA]** CHANGELOG Unreleased bullet, README capability line,
      DEVELOPMENT.md deferred-items link to `docs/git-tools/`. Verify:
      manual read of the three docs. Spec: GT-008.

## Phase 5 - Remote operations (v2.1)

- [x] **T5.1 [BE]** `git_clone` in `gitops.go`: URL scheme allowlist
      (https/git/ssh/http; file:// or scheme-less local paths only when no
      sandbox is active - they bypass the sandbox read roots), optional
      `--branch`, target resolved through write containment with the parent
      as working directory. Verify: local file:// clone, sandboxed-source
      rejection, scheme rejection. Spec: GT-009.
- [x] **T5.2 [BE]** `git_push`: remote/branch name validation, optional
      `--set-upstream`; force-push deliberately NOT exposed (use
      system_exec). MEDIUM risk, approval-gated. Verify: push to a local
      bare remote. Spec: GT-010.
- [x] **T5.3 [BE]** `git_pull`: always `--ff-only` so an agent can never
      silently create merge commits; diverged branches error out for an
      explicit rebase/merge workflow. MEDIUM risk. Verify: second clone
      fast-forwards after a push. Spec: GT-011.
- [x] **T5.4 [BE]** Register the three tools in `CreateGitOpsTools` (nine
      total) + toolset-shape test update; NotARepo surfaced like `git_commit`.
      Verify: `go test ./internal/sandbox/ -run 'TestGit(Clone|Push|Pull)'`.
- [x] **T5.5 [QA]** research.md decisions D9-D12, spec GT-009..011,
      CHANGELOG. Verify: manual read.

## Phase 6 - Destructive operations (v2.2)

- [x] **T6.1 [BE]** `git_checkout` in `gitops.go`: branch switch with
      `create` (-b) and single-path restore (`checkout -- path`), mutually
      exclusive inputs, path traversal checks, never `-f` (git itself
      protects dirty trees). Verify: branch create/switch, restore from
      index, input validation. Spec: GT-012.
- [x] **T6.2 [BE]** `git_reset`: mode soft|mixed|hard (default mixed), ref
      validated as a revision (`HEAD~1`, `@{u}` allowed); `--hard` passes
      through so the gate classifies HIGH. Verify: soft keeps staged, mixed
      unstages, hard destroys a local edit. Spec: GT-013.
- [x] **T6.3 [BE]** `git_clean`: always `-f` (or `-n` dry-run), optional
      `-d`/`-x` mapped onto the existing HIGH combos, optional paths filter,
      "Removing X"/"Would remove X" parsed into `Removed`. Verify: dry-run
      lists only, remove file + dir with -d, path filtering. Spec: GT-014.
- [x] **T6.4 [BE]** Register the three tools (twelve total) + toolset-shape
      test update. Verify: `go test ./internal/sandbox/ -run
      'TestGit(Checkout|Reset|Clean)'`.
- [x] **T6.5 [QA]** research.md D13-D16, spec GT-012..014, CHANGELOG.
      Verify: manual read.

## Phase 7 - Remaining git surface (pending)

- [ ] **T7.x** `git_stash` / `git_rebase` / `git_merge` (interactive
      workflows), `git_commit --amend` / signing, `git_remote` / `git_tag` -
      currently system_exec domain per D16; revisit only with explicit
      product decisions.
