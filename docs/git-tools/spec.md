# Spec: Structured Git Tools (atomic specs)

Feature: `git-tools`. Source of truth for scope. Companion: `research.md`
(decisions D1-D7), `plan.md` (phases). Specs GT-001..GT-008.
r1: v1 scope = read-only `status`/`diff`/`log`/`branch` + mutating
`stage`/`commit`; push/pull/clone/checkout/reset/clean deferred to v2 (D4).

Verification baseline: `go build ./...`, `go test ./...`,
`cd webui && pnpm test`. Remember `make build-frontend` on fresh clones
before Go commands.

---

## GT-001: gitops scaffolding (`runGit` executor + repo-dir resolution)

- **Objective**: Provide the shared execution path every git tool uses, so
  policy, approval, auditing, confinement, and output bounding are defined
  once.
- **Affected components**: `internal/sandbox/gitops.go` (new),
  `internal/sandbox/gitops_test.go` (new).
- **Contracts**:

  ```go
  const gitMaxOutputBytes = 256 * 1024 // combined stdout+stderr capture cap
  const gitMaxDiffLines   = 20000
  const gitMaxLogDefault  = 20
  const gitMaxLogCap      = 100
  const gitExecTimeout    = 120 * time.Second

  // resolveRepoDir resolves the tool's repo_dir input through the sandbox.
  // write=true for mutating tools (stricter containment). Empty input
  // defaults to the agent working directory, or the sandbox workspace root
  // when the sandbox pins one.
  func resolveRepoDir(repoDir string, write bool) (string, error)

  // gitResult is what every tool's runGit returns: bounded combined output
  // plus the exit-status summary.
  type gitResult struct {
      Stdout    string
      Stderr    string
      ExitCode  int
      TimedOut  bool
      Truncated bool // true when combined capture hit gitMaxOutputBytes
  }

  // runGit executes "git <args...>" in repoDir through BuildExecCommand,
  // which applies the gate, approval, path audit, env scrub, and bwrap.
  func runGit(ctx context.Context, repoDir string, args []string, write bool, log interfaces.LogFunc) (gitResult, error)
  ```

- **Security**:
  - argv is always the explicit form `["git", ...args]` (never a shell
    string), so no shell metacharacters can be injected.
  - repoDir is passed as `workingDir` — never `-C` — so `classifyGitRisk`
    sees the subcommand in `argv[1]` (research D2).
  - env always includes `GIT_TERMINAL_PROMPT=0` so credential prompts fail
    fast instead of hanging (D5). Env scrubbing of `HAKASE_*`/`AWS_*`/
    `GITHUB_*`/`OPENAI_*` comes free from `BuildExecCommand`.
  - Sync execution mirrors `systemexec.go`: `runtime.LockOSThread` around
    `Start`/`Wait`, `gitExecTimeout` with `AfterFunc` tree-kill,
    `attachProcessTree`/`releaseProcessTree`.
  - Combined stdout+stderr capture is capped at `gitMaxOutputBytes`; the
    reader stops writing past the cap and sets `Truncated`.
  - Nonzero exit returns an error embedding the bounded stderr (wrapped via
    `wrapUntrustedData`); exit 0 returns raw bounded output.
- **Verify**: `go test ./internal/sandbox/ -run TestGit...` with a
  temp-dir repo: runGit success, nonzero exit error, truncation cap,
  `GIT_TERMINAL_PROMPT` present in a captured env (via a stub
  `BuildExecCommand`-adjacent hook only if needed; prefer asserting behavior
  through the public runGit), sandbox write/read confinement via
  `ResolveScopedPath` on a fake `CurrentSandbox`.

## GT-002: `git_status` tool

- **Objective**: Let the model see the working tree state as a structured
  list instead of eyeballing `porcelain` output.
- **Contracts**:

  ```go
  type GitStatusInput struct {
      RepoDir   string `json:"repo_dir,omitempty"   doc:"Repository directory (defaults to the working directory)"`
      Untracked *bool  `json:"untracked,omitempty"  doc:"Include untracked files (defaults to true)"`
  }

  type GitStatusEntry struct {
      Status  string `json:"status"   doc:"Two-character porcelain status code, e.g. ' M' or '??'"`
      Staged  bool   `json:"staged"   doc:"True when the change is staged (first char not space and not ?)"`
      Path    string `json:"path"     doc:"Repository-relative path (renames: target path)"`
      From    string `json:"from,omitempty" doc:"Source path for renames/copies"`
  }

  type GitStatusOutput struct {
      RepoDir  string            `json:"repo_dir"`
      Branch   string            `json:"branch,omitempty"`
      Ahead    int               `json:"ahead,omitempty"`
      Behind   int               `json:"behind,omitempty"`
      Entries  []GitStatusEntry  `json:"entries"`
      NotARepo bool              `json:"not_a_repo,omitempty" doc:"True when the directory is not a git repository"`
      Stderr   string            `json:"stderr,omitempty"`
  }
  ```

- **Behavior**: runs `git status --porcelain=v1 -b` (plus `--untracked-files=no`
  when `untracked=false`). The `## branch...upstream [ahead N] [behind M]`
  header yields Branch/Ahead/Behind; each remaining line parses as
  `XY path` or `XY old -> new`; the `repo is in a git repository` / `fatal`
  stderr forms map to `NotARepo=true` (no error). Parsed strings are wrapped
  via `wrapUntrustedData` (D7); unparseable lines are passed through as raw
  `Status` with `Staged=false`.
- **Verify**: fresh repo shows branch + empty entries; a created file shows
  `??`; staged file shows `A` staged; rename shows From; outside-repo dir
  yields `NotARepo`.

## GT-003: `git_diff` tool

- **Objective**: Show pending changes (or staged-only) as a unified diff,
  bounded.
- **Contracts**:

  ```go
  type GitDiffInput struct {
      RepoDir string `json:"repo_dir,omitempty"  doc:"Repository directory (defaults to the working directory)"`
      Staged  *bool  `json:"staged,omitempty"    doc:"Show staged changes (git diff --staged; defaults to false)"`
      Path    string `json:"path,omitempty"      doc:"Limit the diff to one repository-relative path or directory"`
  }
  type GitDiffOutput struct {
      RepoDir   string `json:"repo_dir"`
      Diff      string `json:"diff"       doc:"Unified diff (git diff --no-color), wrapped as untrusted data"`
      Truncated bool   `json:"truncated"  doc:"True when the diff exceeded gitMaxDiffLines"`
      NotARepo  bool   `json:"not_a_repo,omitempty"`
  }
  ```

- **Behavior**: argv `git diff --no-color [--staged] [-- path]`. Line-count
  cap `gitMaxDiffLines` (20k); beyond it stop appending and set Truncated.
  Empty stdin-style output (no changes) returns empty Diff, no error.
- **Verify**: unstaged vs staged selection; path filter; truncation at cap;
  no-changes empty output.

## GT-004: `git_log` tool

- **Objective**: Recent commit history as a structured list.
- **Contracts**:

  ```go
  type GitLogInput struct {
      RepoDir string `json:"repo_dir,omitempty"  doc:"Repository directory (defaults to the working directory)"`
      Limit   int    `json:"limit,omitempty"     doc:"Number of commits (defaults to 20, capped at 100)"`
      Path    string `json:"path,omitempty"      doc:"Only commits touching this repository-relative path"`
  }
  type GitLogEntry struct {
      Sha     string `json:"sha"     doc:"Abbreviated commit hash"`
      Author  string `json:"author"  doc:"Commit author name"`
      Date    string `json:"date"    doc:"Commit date (YYYY-MM-DD)"`
      Subject string `json:"subject" doc:"Commit subject line"`
  }
  type GitLogOutput struct {
      RepoDir  string         `json:"repo_dir"`
      Commits  []GitLogEntry  `json:"commits"`
      NotARepo bool           `json:"not_a_repo,omitempty"`
  }
  ```

- **Behavior**: argv
  `git log --pretty=format:%h%x09%an%x09%ad%x09%s --date=short -n N [-- path]`
  (tab-separated to survive subject content). N clamped to `[1,100]`, default
  20. Entries wrapped (D7); extra-fields lines degrade per entry.
- **Verify**: 3-commit repo lists newest first with correct fields; path
  filter; cap clamping.

## GT-005: `git_branch` tool

- **Objective**: List local (or all) branches with the current one marked.
- **Contracts**:

  ```go
  type GitBranchInput struct {
      RepoDir string `json:"repo_dir,omitempty"  doc:"Repository directory (defaults to the working directory)"`
      All     *bool  `json:"all,omitempty"       doc:"Include remote-tracking branches (git branch --all; defaults to false)"`
  }
  type GitBranchEntry struct {
      Name    string `json:"name" doc:"Branch name (remote-tracking branches keep the 'remotes/origin/x' form)"`
      Current bool   `json:"current" doc:"True for the checked-out branch"`
  }
  type GitBranchOutput struct {
      RepoDir  string            `json:"repo_dir"`
      Branches []GitBranchEntry  `json:"branches"`
      NotARepo bool              `json:"not_a_repo,omitempty"`
  }
  ```

- **Behavior**: `git branch --list [--all]`; leading `*` marks Current,
  `  ` for others. Detached HEAD: the `* (HEAD detached at ...)` line is
  surfaced as an entry with `Current=true` and the raw name.
- **Verify**: two local branches, current marking, `--all` includes remotes.

## GT-006: `git_stage` tool (mutating, MEDIUM)

- **Objective**: Stage (or unstage) specific paths with the user's approval.
- **Contracts**:

  ```go
  type GitStageInput struct {
      RepoDir string   `json:"repo_dir"           doc:"Repository directory (defaults to the working directory)"`
      Paths   []string `json:"paths"              doc:"Repository-relative paths to stage (at least one required; '.' stages everything)"`
      Unstage *bool    `json:"unstage,omitempty"  doc:"Unstage instead of stage (git rm --cached; defaults to false)"`
  }
  type GitStageOutput struct {
      RepoDir string   `json:"repo_dir"`
      Paths   []string `json:"paths"     doc:"Paths the operation was applied to"`
      Message string   `json:"message"   doc:"Bounded git output (wrapped as untrusted data)"`
  }
  ```

- **Behavior**: stage → `git add -- paths...`; unstage → `git rm --cached -- paths...`.
  Empty `paths` is an argument error (no execution). MEDIUM risk → approval
  ask via `ApproveFunc` (D1/D3). On denial, the tool returns the
  not-approved error verbatim from `BuildExecCommand`.
- **Verify**: stage makes `git_status` show staged; unstage reverts; empty
  paths errors; approval-denied stub returns error mentioning approval.

## GT-007: `git_commit` tool (mutating, MEDIUM)

- **Objective**: Create a commit with a message, optionally staging all
  changes first.
- **Contracts**:

  ```go
  type GitCommitInput struct {
      RepoDir     string `json:"repo_dir"                doc:"Repository directory (defaults to the working directory)"`
      Message     string `json:"message"                 doc:"Commit message (required)"`
      StageAll    *bool  `json:"stage_all,omitempty"     doc:"Stage all changes first (git add -A; defaults to false)"`
      AllowEmpty  *bool  `json:"allow_empty,omitempty"   doc:"Allow committing with no changes (--allow-empty; defaults to false)"`
  }
  type GitCommitOutput struct {
      RepoDir   string `json:"repo_dir"`
      Sha       string `json:"sha"        doc:"Full commit hash"`
      ShortSha  string `json:"short_sha"  doc:"Abbreviated commit hash"`
      Subject   string `json:"subject"    doc:"Commit subject line"`
      NotARepo  bool   `json:"not_a_repo,omitempty"`
      Stderr    string `json:"stderr,omitempty"`
  }
  ```

- **Behavior**: optional `git add -A -- .` first (one extra exec, still
  MEDIUM), then `git commit -m <message> [--allow-empty]`. Empty message is
  an argument error. After success the full sha is read back via
  `git rev-parse HEAD` (read-only, LOW) and the subject via
  `git log -1 --pretty=%s`. Nonzero commit (e.g. nothing to commit) returns
  an error embedding bounded stderr rather than a fake success.
- **Verify**: commit created with message; `stage_all` pre-stages; empty
  message errors; nothing-to-commit produces the git error, not success.

## GT-008: wiring + docs

- **Objective**: Expose the six tools and record the feature.
- **Affected components**: `internal/agent/agent.go` (`SetupRunner`),
  `CHANGELOG.md`, `README.md`, `docs/DEVELOPMENT.md` deferred section,
  `docs/git-tools/*`.
- **Contracts**:
  - `gitTools, err := sandbox.CreateGitOpsTools(interfaces.LogFunc(log))`
    built next to `systemExecTools`; appended to `orchestratorTools` after
    `systemExecTools` and to `generalPurposeAgent`'s tools after
    `fileOpsTools`.
  - The tools do not re-enter the sandbox package from agent (import
    direction stays `agent → sandbox`).
  - CHANGELOG Unreleased gains a `Structured git tools` bullet; README
    capability list gains a line; this directory is linked from
    DEVELOPMENT.md deferred items in place of "git tools" TODOs.
- **Verify**: `go build ./...`, `go test ./...` green; `go vet` clean;
  `grep -r git_status webui || true` shows no frontend surface needed.
