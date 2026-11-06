# Agentic development workflow: implementation plan

Spec: [spec.md](spec.md) — tasks: [tasks.md](tasks.md). House rule: tick boxes in the same PR that lands the work. Each phase opens one GitHub issue before work starts.

Status: Proposed  
Date: 2026-11-06  
Branch convention for this work: human branches off `main` named `docs/<issue>-<slug>` (the `develop`-based convention is what this plan retires).

## Goal

Land spec.md in four phases: prune `develop` and stale branches, gate `main` with required CI, add the local-agent contract, then measure and tune.

## Architecture

No production code changes. This plan edits workflow files (GitHub Actions, issue templates, instruction files, Makefile), applies repository settings through the GitHub API or web UI, and deletes merged branches. The only executable logic added is a `make wt` worktree helper, a `ci-required` aggregator job, a PR-title check, and a release-tag guard step.

## File map

| File | Phase | Change |
|---|---|---|
| `AGENTS.md:66-71` | 0 | Workflow section targets `main`, `ai/<tool>/<issue>-<slug>` branches |
| `CONTRIBUTING.md:26-40` | 0 | Branches and PRs target `main` |
| `SECURITY.md:15` | 0 | Test against latest `main`, not `develop` |
| `.agents/prompts/jules.md:30-32` | 0 | Branch off `main`, PRs target `main` |
| `.github/ISSUE_TEMPLATE/feature_request.yml:8` | 0 | ROADMAP link `blob/develop` → `blob/main` |
| `.github/workflows/test.yml` | 0 + 1 | Drop `develop` trigger; add `merge_group`, `concurrency`, `timeout-minutes`, `ci-required`, PR-title check, pinned SHAs |
| `.github/workflows/release.yml` | 1 | Pre-build guard: tag on `main`, CHANGELOG non-empty |
| `.github/ISSUE_TEMPLATE/agent_task.yml` | 2 | New agent issue form (create) |
| `webui/AGENTS.md` | 2 | New nested instructions (create) |
| `internal/agent/AGENTS.md` | 2 | New nested instructions (create) |
| `cmd/hakase/AGENTS.md` | 2 | New nested instructions (create) |
| `.github/AGENTS.md` | 2 | New nested instructions (create) |
| `Makefile` | 2 | New `wt` target + `.PHONY` entry |
| `docs/agent-workflow/` | 0-3 | `spec.md` (done), this file, `tasks.md` |
| GitHub ruleset `protect-main` (id `20210676`) | 1 | Add PR + checks + linear history + conversation resolution via web UI or API |
| Repository merge settings | 1 | Squash-only, auto-merge, delete-on-merge, update-branch via API |
| Tag ruleset `v*` | 1 | Block update/delete via web UI |

## Phase 0 — Freeze and prune

T0.1 edits six files plus `test.yml` triggers only (full CI rework is Phase 1). Exact replacements:

- `AGENTS.md` Workflow section becomes:

```markdown
## Workflow

- Branch off `main`, PRs target `main`. Agent branches use `ai/<tool>/<issue>-<slug>`. One logical change per PR.
- Issues first: every unit of work has an issue with file-colon-line evidence. Substantial features follow the house convention `docs/<feature>/` with spec plus plan plus tasks files.
- Commits follow Conventional Commits (`feat(scope): ...`, `fix(scope): ...`, etc.) and reference issues with `Closes #N`.
- One issue maps to one agent, one branch, and one PR. The issue lists target files; two open issues with overlapping targets MUST NOT run concurrently. At most 3 open agent PRs.
- Jules tasks: Jules reads this file automatically. For PR work, also read `.agents/prompts/jules.md` and follow it - fetch inline review comments first and treat unresolved findings as in-scope.
```

- `CONTRIBUTING.md:35-36` becomes: `Fork (or branch) off \`main\`; PRs target \`main\`. Agent branches use \`ai/<tool>/<issue>-<slug>\`. Keep PRs to one logical change.`
- `SECURITY.md:15` becomes: `` `main` branch before reporting. ``
- `.agents/prompts/jules.md:30-31` becomes: `> - Repo conventions still apply: branch off main, PRs target` / `>   main, one logical change per PR, verify with go vet and tests`
- `feature_request.yml:8` becomes: `Check [docs/ROADMAP.md](https://github.com/amurru/hakase/blob/main/docs/ROADMAP.md)`
- `test.yml:7-10` becomes:

```yaml
on:
  push:
    branches: [main]
  pull_request:
```

T0.2 verifies `develop` is fully merged before anything is deleted:

```bash
git fetch origin --prune
git merge-base --is-ancestor origin/develop origin/main && echo DEVELOP-MERGED
git rev-list --left-right --count origin/main...origin/develop
```

Expected: prints `DEVELOP-MERGED` and `0	<N>` (left side zero means `develop` adds nothing). If the left side is non-zero, stop and re-plan.

T0.3 deletes remote branches only after per-branch verification. Re-derive the list at execution time (`git ls-remote --heads origin`), then for each candidate confirm a merged PR or landed patch:

```bash
gh pr list --state all --head "<branch>" --json number,state,title
git merge-base --is-ancestor "origin/<branch>" origin/main && echo LANDED
```

Delete only branches where the PR state is `MERGED` or the patch check prints `LANDED`. Deletion is irreversible, so the task requires explicit per-branch confirmation in the Phase 0 issue before running:

```bash
git push origin --delete <branch>
```

At spec time the delete candidates were the four `chore/release-2026-10-2*` branches, the merged `docs/*`, `feat/*`, and `fix/*` branches, the two `jules-*` branches after patch verification, and `research/durable-resume` if its content is in `main`. `develop` is deleted last. Dependabot branches are left for Dependabot to manage.

T0.4 cleans the local clone after the remote deletions:

```bash
git switch main
git pull --ff-only
git branch -d <each merged local branch>
git worktree list
```

`git branch -d` (not `-D`) refuses to delete unmerged work. Any branch it refuses needs individual review.

T0.5 verifies the prune:

```bash
git ls-remote --heads origin | awk '{print $2}'
grep -rn --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist --exclude=pnpm-lock.yaml --exclude=CHANGELOG.md '\bdevelop\b' AGENTS.md CONTRIBUTING.md SECURITY.md .agents/prompts/jules.md .github/ISSUE_TEMPLATE/ .github/workflows/
```

Expected: only `main` plus branches with genuinely unmerged work remain; the grep returns nothing in active guidance (historical `docs/qa/*` and shipped design docs are exempt).

## Phase 1 — Gate `main`

T1.1 applies the `main` ruleset. Preferred path is the web UI (Settings → Rules → `protect-main`), because the API payload is brittle. Required rules: pull request before merging, required status check `ci-required`, conversation resolution, linear history, block force pushes, block deletion, empty bypass list including administrators, no approval count. Verify with:

```bash
gh api repos/amurru/hakase/rulesets/20210676 --jq '{name, enforcement, rules: [.rules[].type]}'
```

T1.2 applies repository merge settings:

```bash
gh api repos/amurru/hakase -X PATCH \
  -F allow_squash_merge=true \
  -F allow_merge_commit=false \
  -F allow_rebase_merge=false \
  -F allow_auto_merge=true \
  -F delete_branch_on_merge=true \
  -F allow_update_branch=true \
  -F squash_merge_commit_title=COMMIT_OR_PR_TITLE \
  -F squash_merge_commit_message=COMMIT_MESSAGES
gh api repos/amurru/hakase --jq '{allow_squash_merge, allow_merge_commit, allow_rebase_merge, allow_auto_merge, delete_branch_on_merge, allow_update_branch}'
```

Verify the merge queue separately: if Settings → Rules will not accept a queue rule for this repository, record the rejection in the Phase 1 issue and use the fallback (auto-merge plus require-branches-up-to-date). Do not treat the queue as optional silently.

T1.3 rewrites `test.yml`:

```yaml
on:
  push:
    branches: [main]
  pull_request:
  merge_group:

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

Add `timeout-minutes: 30` to `linux` and `windows`. Add the aggregator:

```yaml
  ci-required:
    name: ci-required
    if: always()
    needs: [linux, windows]
    runs-on: ubuntu-latest
    steps:
      - name: Verify all CI passed
        run: |
          if [ "${{ needs.linux.result }}" != "success" ] || [ "${{ needs.windows.result }}" != "success" ]; then
            echo "linux=${{ needs.linux.result }} windows=${{ needs.windows.result }}"
            exit 1
          fi
```

Add a PR-title check as its own job (advisory, never required, because it has no meaningful result on `merge_group`):

```yaml
  pr-title:
    name: Conventional PR title
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
      # Resolve the SHA at execution time, e.g.:
      # gh api repos/amannn/action-semantic-pull-request/commits/main --jq '.sha'
      - uses: amannn/action-semantic-pull-request@<resolved-sha>
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

Pin every other action in `test.yml` to commit SHAs, matching `release.yml` where the action overlaps. Resolve each SHA at execution time with `gh api repos/<owner>/<repo>/commits/<ref> --jq '.sha'`; do not paste floating tags or guessed SHAs.

T1.4 adds a guard job at the top of `release.yml`, before `build`:

```yaml
  guard:
    name: Release guard
    runs-on: ubuntu-latest
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@<pinned-sha>
        with:
          fetch-depth: 0
          persist-credentials: false
      - name: Tag must be on main and CHANGELOG must be non-empty
        env:
          TAG: ${{ github.ref_name }}
        run: |
          set -euo pipefail
          git fetch origin main --quiet
          git merge-base --is-ancestor "$TAG" origin/main
          python3 - <<'EOF'
          import re, sys, os
          text = open("CHANGELOG.md").read()
          unreleased = re.search(r"## \[Unreleased\]\n(.*?)(?=\n## \[)", text, re.S)
          tag = os.environ.get("TAG", "").lstrip("v")
          tagged = re.search(r"## \[" + re.escape(os.environ.get("TAG", "")) + r"\]|## \[" + re.escape(tag) + r"\]", text)
          body = (unreleased.group(1).strip() if unreleased else "")
          if not body and not tagged:
              print("CHANGELOG has neither Unreleased notes nor a section for the tag")
              sys.exit(1)
          EOF
```

T1.5 adds the `v*` tag ruleset via the web UI (Settings → Rules → New ruleset → Tag): name `protect-tags`, enforcement active, target tags matching `v*`, block update and delete, empty bypass list.

T1.6 is the throwaway-PR proof, run in order: open a PR with a one-line docs change; confirm a direct push to `main` is rejected (`git push origin main` fails); confirm `ci-required` appears as the required check and both `linux`/`windows` plus `merge_group` run; confirm the queue (or fallback) merges the green PR; confirm auto-merge can be enabled; only then close the Phase 1 issue.

## Phase 2 — Agent contract

T2.1 creates `.github/ISSUE_TEMPLATE/agent_task.yml` with required fields `context`, `acceptance`, `target-files`, `test-plan`, `risk` (`low|med|high`), `dependencies`, `out-of-scope`, and labels the issue `agent:ready` by default. Create the labels:

```bash
gh label create "agent:ready" --color "0e8a16" --description "Agent task ready to dispatch" --force
gh label create "agent:running" --color "1d76db" --description "Agent actively working" --force
gh label create "agent:blocked" --color "d93f0b" --description "Agent blocked, needs human" --force
gh label create "needs:human" --color "e99695" --description "Waiting on human decision" --force
gh label create "risk:low" --color "c2e0c6" --description "Low-risk change" --force
gh label create "risk:med" --color "fef2c0" --description "Medium-risk change" --force
gh label create "risk:high" --color "ee0701" --description "High-risk change, manual review, no auto-merge" --force
```

T2.2 creates the four nested `AGENTS.md` files. Each file is short (under 60 lines) and contains only its subtree's build/test commands, invariants, and human-review paths:

- `webui/AGENTS.md`: pnpm only, `pnpm build` runs `vue-tsc -b`, vitest commands, Tailwind 4, never npm/yarn.
- `internal/agent/AGENTS.md`: ADK runner layout, provider/gate patterns, never import `internal/sleep`.
- `cmd/hakase/AGENTS.md`: entry-point wiring, `cli.RegisterCommand`, no Go files at repo root, import-cycle warning.
- `.github/AGENTS.md`: least-privilege workflow edits, SHA pinning, `merge_group` + aggregator rule, never `pull_request_target` with secrets.

T2.2 also extends `CONTRIBUTING.md` with the risk policy and tasks-file schema, appended to the "How we work" section:

```markdown
- **Risk tiers.** Label every PR `risk:low`, `risk:med`, or `risk:high`.
  `low` (docs, tests only, dependency patch/minor, isolated UI copy) may
  auto-merge after green CI. `med` (ordinary single-package feature or fix)
  needs human review before merge. `high` (`internal/auth`,
  `internal/sandbox`, `internal/agent`, `internal/permissions`,
  `.github/workflows/**`, `go.mod`, release or install paths) needs human
  review, never auto-merges, and additionally requires a test that fails
  before the change and passes after (or a written reason why that is
  impractical), explicit review of test-file diffs, and a rollback or
  mitigation note in the PR body.
- **Task files.** New `docs/<feature>/tasks.md` files use these columns:
  `id` (stable task identifier), `files` (paths the task may touch),
  `depends_on` (task IDs that must land first), `acceptance` (one-sentence
  done condition), `status` (`todo`/`doing`/`done`), `agent` (assignee).
  Existing task files are not migrated.
```

T2.3 adds the `wt` target to the `Makefile` (and to `.PHONY`):

```make
wt:
ifndef ISSUE
	$(error Usage: make wt ISSUE=<issue> TOOL=<tool> [SLUG=<slug>])
endif
ifndef TOOL
	$(error Usage: make wt ISSUE=<issue> TOOL=<tool> [SLUG=<slug>])
endif
	git fetch origin
	git worktree add ../hakase-wt/$(ISSUE) -b ai/$(TOOL)/$(ISSUE)-$(if $(SLUG),$(SLUG),work) origin/main
	@echo "worktree ready: ../hakase-wt/$(ISSUE)"
	@echo "cleanup after merge: git worktree remove ../hakase-wt/$(ISSUE)"
```

Guard: refuse to run inside an existing worktree-linked path collision (git errors naturally if the directory exists; do not force).

T2.4 is the two-agent pilot: pick two open issues with non-overlapping target files, create one worktree each, land both PRs through the normal gate, remove both worktrees. Pilot agents run under the scoped push credential from spec Section 8.7 (write-enabled deploy key or contents-write fine-grained token), never the admin `gh` session. Record every point of friction in the Phase 2 issue and fix the instructions rather than working around them.

## Phase 3 — Measure and tune

T3.1 records the baseline before Phase 1 starts (PR cycle time, review time, CI durations, revert count, stale branch count) using `gh pr list`, `gh run list`, and `git ls-remote`. T3.2 compares the same series two weeks after Phase 2 lands and writes the comparison into the Phase 3 issue. Only then decide on stacked PRs, release-please, or a dispatcher. The decision and its rationale go in the issue, not in a new spec, unless the change is large enough to need one.

## Test plan

Every code-touching PR in this plan runs the same gate locally before pushing, in order:

```bash
make build-frontend
unformatted=$(gofmt -l .); test -z "$unformatted"
go vet ./...
GOOS=darwin go build ./...
go test ./...
bash packaging/install_test.sh
pnpm --dir webui test
```

Docs-only and settings-only changes run the grep/`gh api` verifications in their task instead. CI (`linux`, `windows`, `ci-required`, plus `merge_group` on queue entries) is the binding gate.

## Rollback

- Phase 0 file edits: revert the PR. Branch deletions cannot be reverted; that is why T0.3 requires per-branch `MERGED`/`LANDED` proof first.
- Phase 1 ruleset/settings: re-apply the prior values via the same API calls (record before/after in the issue). A bad `test.yml` is reverted by merging a fix; the queue blocks rather than breaks `main`.
- Phase 2 additions are new files; remove them if the pilot rejects the contract.
