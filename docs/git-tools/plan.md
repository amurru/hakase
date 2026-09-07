# Execution Plan: Structured Git Tools

Feature: `git-tools`
Source of truth: `spec.md` (atomic specs GT-001..GT-008, r1).
Date: 2026-09-01 (r1). Scope: v1 = read-only `git_status` / `git_diff` /
`git_log` / `git_branch` + mutating `git_stage` / `git_commit`; no push/pull/
clone, no checkout/reset/clean (deferred to v2 per research D4).

## Phases

### Phase 1 - Foundation (serial)

**GT-001** Repo-dir resolution + `runGit` executor in `internal/sandbox/gitops.go`.

- Exit: `resolveRepoDir(repoDir, write)` respects `CurrentSandbox.ResolveScopedPath`
  (write flag by mutability), defaults to working dir / sandbox workspace root;
  `runGit(ctx, repoDir, args, write, log)` builds argv `["git", ...args...]`,
  passes repoDir as `workingDir` to `BuildExecCommand` with
  `GIT_TERMINAL_PROMPT=0`, runs sync with timeout + LockOSThread, caps combined
  output at `gitMaxOutputBytes` (256 KiB) with a `gitTruncated` flag, wraps
  stdout/stderr as untrusted via `wrapUntrustedData` on a raw fallback path,
  maps nonzero exit to an error carrying stderr. Copy tests reference the
  existing systemexec sync pattern.

### Phase 2 - Read-only tools (parallelizable)

- [ ] **GT-002** `git_status` — `git status --porcelain=v1 -b` + parse. LOW/allow.
- [ ] **GT-003** `git_diff` — `git diff [--staged] [-- path]`, 20k-line cap. LOW/allow.
- [ ] **GT-004** `git_log` — `git log --pretty=format:%h|%an|%ad|%s --date=short -n N [-- path]`,
      N default 20 max 100. LOW/allow.
- [ ] **GT-005** `git_branch` — `git branch --list [--all]`, parse `*`. LOW/allow.

### Phase 3 - Mutating tools (approval-gated)

- [ ] **GT-006** `git_stage` — `git add -- paths...`, paths required, `unstage`
      switch maps to `git rm --cached -- paths...`. MEDIUM/ask.
- [ ] **GT-007** `git_commit` — optional `stage_all` (`git add -A`), commit with
      `-m message`, `allow_empty` adds `--allow-empty`, returns full + short sha.
      MEDIUM/ask.

### Phase 4 - Wiring & docs

- [ ] **GT-008** Build `CreateGitOpsTools(log)` in `internal/sandbox/gitops.go`
      returning the six tools; append to `orchestratorTools` and to
      `generalPurposeAgent` tools in `internal/agent/agent.go` `SetupRunner`;
      CHANGELOG Unreleased entry; README capability list mention; this directory.

Exit: `go build ./...`, `go test ./...`, `cd webui && pnpm test` untouched
(frontend unaffected), `go test ./internal/sandbox/ -run Git` green including
the delegated-path gyration (per-task repo under a delegated sandbox root).

## Verification baseline

`go build ./...`, `go test ./...` (remember `make build-frontend` on fresh
clones before Go commands). Remember `logs/` dirs are runtime artifacts and
must not be committed.
