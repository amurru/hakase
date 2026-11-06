# Agentic development workflow: gated trunk with local agents

Status: Proposed  
Date: 2026-11-06  
Owner: Ammar Zerouk  
Tracking: implementation tasks live in `docs/agent-workflow/tasks.md`; each phase opens one GitHub issue before work starts.

## 1. Summary

Replace the `develop` -> `main` promotion flow with a single protected trunk (`main`) and short-lived feature branches. Optimize the repository for 2-3 local coding agents working in parallel, with a human as reviewer and merger.

The model is deliberately narrow: local agents only, no cloud-agent PR pipeline, no stacked-PR tooling, and no release-please migration in the first iteration. The gate is CI, risk labels, and a human review pass, because a solo maintainer cannot approve their own pull request.

## 2. Current state

Evidence collected on 2026-11-06.

| Area | Finding | Evidence |
|---|---|---|
| Integration branch | `develop` is fully contained in `main`: 0 unique commits, 9 behind at audit time | `git rev-list --left-right --count develop...origin/main` |
| Branch instructions | Active docs still require branching off `develop` | `AGENTS.md:68`, `CONTRIBUTING.md:35`, `SECURITY.md:15`, `.agents/prompts/jules.md:30`, `.github/ISSUE_TEMPLATE/feature_request.yml:8` |
| CI triggers | Tests run on pushes to `main` and `develop`, and on every pull request | `.github/workflows/test.yml:9` |
| Protection | `protect-main` ruleset only blocks deletion and non-fast-forward pushes; no PR, check, or review requirement | GitHub API, ruleset `20210676` |
| Merge settings | Merge commit, squash, and rebase all enabled; auto-merge off; delete-branch-on-merge off; update-branch off | GitHub repository settings |
| Branches | 23 non-Dependabot remote branches besides `main`: 9 feature, 3 docs, 2 fix, 5 release, 2 Jules, 1 research, and `develop`. No open feature PRs | `git ls-remote --heads origin` |
| Open work | 3 Dependabot PRs, all CI-green; 1 open issue (#151) | GitHub API |
| Release gap | Latest release `v0.1.0` (2026-10-13); `main` has 45 commits since the tag, all user-facing changes still under `CHANGELOG [Unreleased]` | `git log v0.1.0..origin/main --oneline` |
| Agent context | Root `AGENTS.md` is 71 lines; no nested `AGENTS.md`; 25 committed skills | repository tree |

The important conclusion is that `develop` is already a vestige. It is not staging or stabilizing anything; it adds a promotion PR, merge commits, and a second branch to keep in sync while every feature branch still targets it.

## 3. Goals

- One long-lived branch: `main`.
- Short-lived feature branches with a predictable name and a traceable issue.
- 2-3 local agents can work concurrently without stepping on each other's files.
- `main` is protected by required CI, not by convention.
- Agent work carries enough context in the issue and task files to be executed without re-deriving the design.
- Releases are cut from `main` on a predictable cadence.
- Fewer than 5 stale remote branches at any time.

## 4. Non-goals

- Cloud coding agents (Jules, Copilot coding agent, Codex cloud) opening PRs. They may still be used interactively, but the workflow does not assume them.
- Stacked-PR tooling (`gh-stack`, Graphite) in the first iteration.
- A custom task dispatcher with file locks.
- CODEOWNERS-driven review. There is one human; CODEOWNERS would block merges without adding a reviewer.
- Mutation testing, release-please, or a metrics dashboard.
- Rewriting historical QA reports and design docs that mention `develop`. Only active guidance is updated.

## 5. Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Integration model | `main` only | `develop` is contained in `main`; long-lived integration branches increase merge and sync work |
| Agent execution | Local only, 2-3 concurrent | Matches the maintainer's environment; cloud approval gates and bot identities are unnecessary |
| Gate | Required CI + risk labels + human review | A solo maintainer cannot satisfy a required human approval rule |
| Merge strategy | Squash only | Keeps `main` linear and makes Conventional Commit titles the release-note source |
| Merge queue | Enable if available; fall back to auto-merge + up-to-date | Queue tests the merged result, not a stale branch; availability on a personal public repository must be validated |
| Release pipeline | Keep existing tag-triggered `release.yml` | Already provides SLSA provenance, deb/rpm, Windows zip, and smoke tests |
| Release automation | Manual tag from `main` for now | Avoids a release-please migration while the workflow is changing |
| Worktrees | One per agent, outside the repository | Prevents agents from sharing an index or `node_modules` tree |

## 6. Operating model

### 6.1 Branch types and lifetime

| Work | Branch pattern | Owner |
|---|---|---|
| Agent task | `ai/<tool>/<issue>-<slug>` | Local agent |
| Human feature/fix/docs/chore | `feat/`, `fix/`, `docs/`, or `chore/` followed by `<issue>-<slug>` | Maintainer |
| Release backport (exceptional) | `release/<minor>` | Maintainer |

Rules:

- Every branch carries the issue number.
- A branch MUST target `main`.
- A branch SHOULD live less than 2 days and SHOULD stay under approximately 400 changed lines. Larger work is split into stacked PRs or landed behind a flag.
- A branch MUST be deleted after merge. The repository setting `delete_branch_on_merge` enforces this for merged PRs.
- Force-pushing a feature branch is allowed; force-pushing `main` is forbidden by the ruleset.

### 6.2 Local agent isolation

Each concurrent agent gets its own worktree and branch:

```bash
git fetch origin
git worktree add ../hakase-wt/<issue> -b ai/<tool>/<issue>-<slug> origin/main
```

- Worktrees live in a sibling directory, `../hakase-wt/<issue>`, not inside the repository.
- One agent owns one worktree. Two agents MUST NOT share a worktree or an index.
- Symlink or share `webui/node_modules` and the Go build cache where practical; do not copy them per worktree.
- Remove the worktree after merge: `git worktree remove ../hakase-wt/<issue>`.
- A `make wt ISSUE=<issue> TOOL=<tool>` target SHOULD wrap creation and print the follow-up commands.

### 6.3 Dispatch and file-set ownership

- One issue maps to one agent, one branch, and one PR.
- The issue body MUST list target files or directories. If two open issues list overlapping targets, they MUST NOT be dispatched concurrently.
- Shared contracts land first: types, interfaces, migrations, API schemas, and test fixtures go in their own PR before parallel implementation starts.
- At most 3 open agent PRs at a time. When the cap is reached, finish or close work before starting more.
- An agent MUST NOT modify files outside its issue's target list without updating the issue first.

### 6.4 Landing

- Merge method is squash only.
- The squash title comes from the PR title; the body comes from the PR body.
- Auto-merge MAY be enabled after required checks pass.
- When the merge queue is available, all merges go through it. Queue settings: squash merge, all-green grouping, small batch (1-3 entries), 60-minute check timeout.
- If the queue is unavailable for this repository, the fallback is auto-merge plus a rule requiring the branch to be up to date before merging.
- High-risk PRs (Section 8.6) MUST NOT use auto-merge.

## 7. Agent contract

### 7.1 Issue form

Add `.github/ISSUE_TEMPLATE/agent_task.yml` with these required fields:

| Field | Purpose |
|---|---|
| Context | What is broken or missing, with file:line evidence |
| Acceptance criteria | Observable outcomes that define done |
| Target files | Exact paths or package directories the agent may change |
| Test plan | Commands and new tests expected |
| Risk tier | `low`, `med`, or `high` |
| Dependencies | Other issues or contracts that must land first |
| Out of scope | Work the agent must not do |

Existing bug and feature-request forms keep their fields; only the ROADMAP link in the feature-request form changes in Phase 0.

### 7.2 Labels

Add `agent:ready`, `agent:running`, `agent:blocked`, `needs:human`, `risk:low`, `risk:med`, `risk:high`. Keep the existing type labels (`bug`, `enhancement`, `tech-debt`).

The labels are operational signals, not enforcement. Enforcement comes from the ruleset and from the maintainer not enabling auto-merge on high-risk PRs.

### 7.3 Task files

New features use the existing `docs/<feature>/tasks.md` convention with these columns:

| Column | Meaning |
|---|---|
| `id` | Stable task identifier |
| `files` | Files or directories the task may touch |
| `depends_on` | Task IDs that must land first |
| `acceptance` | One-sentence done condition |
| `status` | `todo`, `doing`, `done` |
| `agent` | Agent or human assigned |

Existing task files are not migrated. The schema applies to new features and to phase work created by this spec.

### 7.4 Instruction files

- Root `AGENTS.md` stays short (roughly 100-150 lines) and keeps commands first.
- Add nested `AGENTS.md` files for `webui/`, `internal/agent/`, `cmd/hakase/`, and `.github/`. The nearest file wins, so each file should contain only that subtree's commands, invariants, and boundaries.
- Each nested file MUST state: build/test commands, import-direction or wiring rules, and the paths that require human review.
- Update the root file's branch instructions to target `main` and to use the `ai/<tool>/<issue>-<slug>` pattern.
- Update `.agents/prompts/jules.md` to target `main`, even though Jules is not part of the primary workflow.

## 8. CI and merge gates

### 8.1 Triggers

`.github/workflows/test.yml` MUST trigger on:

- `pull_request`
- `push` to `main` only
- `merge_group`

Add:

```yaml
concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

Add `timeout-minutes` to each job. Keep the full Linux and Windows runs. This is a public repository, so standard runner minutes are free; correctness on both platforms is worth more than the saved time.

### 8.2 Required check

Add a `ci-required` job that:

- runs with `if: always()`
- declares `needs: [linux, windows]`
- fails unless both dependencies succeeded
- reports success only when both succeeded

Make `ci-required` the required status check in the `main` ruleset. Do not require the individual `linux` and `windows` jobs as well.

A path-filtered or conditionally skipped required check stays pending forever and blocks the merge. The aggregator exists so future path filters cannot create that failure mode.

### 8.3 Advisory checks

- CodeQL remains advisory. Default setup does not reliably report on `merge_group`, and making it required would block the queue.
- Add a conventional-PR-title check. It is advisory rather than required because a PR-title check has no meaningful result on a `merge_group` event.
- CodeRabbit remains advisory.

### 8.4 Ruleset

The `main` ruleset MUST require:

- a pull request before merging
- the `ci-required` status check
- conversation resolution
- linear history
- no force pushes
- no branch deletion
- an empty bypass list, including administrators

It MUST NOT require an approving review, because the sole maintainer cannot approve their own PR. This is an explicit trade-off: CI and the human review pass are the gate.

If the merge queue is available, add the queue rule to the same ruleset. If GitHub rejects the rule for this repository, record that in the implementation issue and use the Section 6.4 fallback.

### 8.5 Repository settings

- Allow squash merge only.
- Enable auto-merge.
- Enable delete-branch-on-merge.
- Enable update-branch.
- Set the squash commit title to the PR title and the body to the PR body.
- Add a tag ruleset for `v*` that blocks updates and deletions, so release tags are immutable.

### 8.6 Review and risk policy

| Risk | Examples | Gate |
|---|---|---|
| `low` | docs, tests only, dependency patch/minor, isolated UI copy | Required CI; auto-merge allowed |
| `med` | ordinary feature or fix in one package | Required CI; human review before merge; auto-merge optional |
| `high` | `internal/auth`, `internal/sandbox`, `internal/agent`, `internal/permissions`, `.github/workflows/**`, `go.mod`, release or install paths | Required CI; human review; no auto-merge |

High-risk PRs additionally require:

- a test that fails before the change and passes after, or a written explanation of why that is impractical
- explicit review of test-file diffs, so tests are not weakened to make CI green
- a rollback or mitigation note in the PR body

### 8.7 Security

- `test.yml` actions MUST be pinned to commit SHAs, matching `release.yml`.
- Workflows keep `permissions: contents: read` unless a job needs more.
- No `pull_request_target` with repository secrets.
- Local agents use a scoped push credential that cannot change repository settings or rulesets, such as a write-enabled deploy key or a fine-grained token limited to contents write. The admin `gh` session stays in the human shell. The server-side ruleset prevents an agent from pushing to `main`; scoping the credential prevents an agent from modifying the ruleset itself.

## 9. Release and hotfix

### 9.1 Release pipeline

Keep `.github/workflows/release.yml`. Add a verification step before the build:

- the tagged commit MUST be an ancestor of `origin/main`
- `CHANGELOG.md` MUST contain either a non-empty `[Unreleased]` section or a `[<tag-version>]` section, so a release commit that promotes the unreleased notes still passes

A failed check stops the release. The existing build, nfpm packaging, SLSA provenance, Windows zip, and smoke tests stay unchanged.

### 9.2 Cadence

Cut a release when `[Unreleased]` contains user-facing changes and `main` is green, at minimum every two weeks. Update `CHANGELOG.md`, commit, then tag from `main`:

```bash
git switch main
git pull --ff-only
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

### 9.3 Hotfix

Fix on `main` first, with a regression test, then tag a patch release. Do not create a release branch unless an older minor is genuinely supported. If a backport is required, cherry-pick the fix to `release/<minor>` and tag from there; that branch receives no new development.

## 10. Migration

### Phase 0 - Freeze and prune

1. Update active references from `develop` to `main`: `AGENTS.md`, `CONTRIBUTING.md`, `SECURITY.md`, `.agents/prompts/jules.md`, `.github/ISSUE_TEMPLATE/feature_request.yml`, and `.github/workflows/test.yml`.
2. Verify `develop` is an ancestor of `origin/main`.
3. Delete `develop` from origin and locally.
4. Delete every remote branch whose PR is merged. For branches without a PR association, verify the patch landed in `main` before deleting. Keep `main` and any branch with genuinely unmerged work.
5. Reset local `main` to `origin/main` and remove merged local branches.
6. Delete the merged `chore/release-*` branches and the research branch if its content is in `main`.

Acceptance: `git ls-remote --heads origin` lists `main` plus only branches with open or unmerged work; no active document tells a contributor to branch off `develop`.

### Phase 1 - Gate `main`

1. Apply the ruleset from Section 8.4.
2. Apply repository settings from Section 8.5.
3. Update CI per Sections 8.1-8.3.
4. Pin actions in `test.yml`.
5. Open a throwaway PR with a trivial docs change and verify: direct push is rejected, `ci-required` is required, `merge_group` runs, and the merge queue (if enabled) lands the PR.
6. Enable auto-merge after the throwaway PR proves the gate.

Acceptance: a direct push to `main` is rejected; a PR with a failing `ci-required` cannot merge; the queue or fallback path merges a green PR; a tag rule blocks tag updates.

### Phase 2 - Agent contract

1. Add the agent issue form and labels.
2. Add nested `AGENTS.md` files.
3. Add the tasks-file schema to the workflow documentation.
4. Add `make wt`.
5. Pilot two agents on two non-overlapping issues and land both PRs through the normal gate.
6. Record friction in the implementation issue; fix the instructions rather than working around them.

Acceptance: two concurrent agents complete without touching the same files; both PRs land without manual rebase; worktrees are removed after merge.

### Phase 3 - Measure and tune

Record the baseline before Phase 1 and compare after two weeks:

- PR cycle time (open to merge)
- review time (first review to merge)
- merge-queue wait time
- CI duration for `linux`, `windows`, and `ci-required`
- revert rate and escaped defects
- open agent PR count
- stale branch count

Only after this review decide whether stacked PRs, release-please, or a dispatcher are worth adding.

## 11. Success criteria

After four weeks:

- zero direct pushes to `main`
- at least 80% of agent PRs land without a manual rebase
- median PR cycle time under 24 hours
- no more than 3 open agent PRs at any time
- fewer than 5 stale remote branches
- release gap under 14 days
- no release tagged from a commit outside `main`

## 12. Risks and mitigations

| Risk | Mitigation |
|---|---|
| CI is the only automated gate | Spec/plan approval before code; test-diff review; high-risk paths require manual review; separate reviewer pass rather than author self-review |
| Merge queue unavailable on a personal public repository | Validate in Phase 1; fall back to auto-merge plus up-to-date requirement |
| Auto-merge lands weak or deleted tests | High-risk paths excluded from auto-merge; test diffs reviewed; require a failing-before/passing-after test |
| Agents create conflicts by sharing files | Target-file lists in issues; contract-first landing; 3-PR cap |
| Branch pile returns | Delete-on-merge; weekly prune; stale-branch metric |
| Worktrees consume disk or confuse tooling | Sibling directory; documented cleanup; no worktree inside the repo |
| Ruleset bypass by an agent with admin credentials | Empty bypass list; agents use a scoped push credential; admin `gh` auth stays in the human shell; audit ruleset changes |
| Documentation drift | One workflow spec plus nested `AGENTS.md`; update both in the same PR as policy changes |

## 13. References

- DORA, trunk-based development capability: <https://dora.dev/capabilities/trunk-based-development/>
- Trunk Based Development: <https://trunkbaseddevelopment.com/>
- AGENTS.md standard: <https://agents.md/>
- Conventional Branch: <https://conventional-branch.github.io/>
- GitHub merge queue and `merge_group` requirement: <https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue>
- GitHub Copilot runtime Rust migration, many small PRs to `main`: <https://github.blog/ai-and-ml/generative-ai/migrating-the-github-copilot-runtime-to-rust-using-copilot/>
- GitHub Spec Kit, spec-driven development: <https://github.blog/ai-and-ml/generative-ai/spec-driven-development-with-ai-get-started-with-a-new-open-source-toolkit/>
- GitHub Docs, workflow trigger behavior and pending checks: <https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow>

Vendor-reported productivity numbers were not used as hard requirements. The workflow is justified by branch mechanics, CI behavior, and the repository's own evidence.
