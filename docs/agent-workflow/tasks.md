# Tasks: Agentic development workflow

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work. Each phase opens one GitHub issue before work starts; issue numbers are filled in when opened.

Legend: `[DOCS]` docs/guidance, `[CI]` workflows, `[OPS]` repository settings and branch surgery, `[TOOL]` Makefile/helper, `[QA]` verification. Depends_on uses task IDs in this file.

## Phase 0 — Freeze and prune

- [ ] **T0.1 [DOCS]** retarget active guidance from `develop` to `main`. files: `AGENTS.md:66-71`, `CONTRIBUTING.md:26-40`, `SECURITY.md:15`, `.agents/prompts/jules.md:30-32`, `.github/ISSUE_TEMPLATE/feature_request.yml:8`, `.github/workflows/test.yml:7-10` (trigger only). depends_on: none. acceptance: the T0.5 grep returns nothing in active guidance; historical `docs/qa/*` and shipped design docs exempt.
- [ ] **T0.2 [OPS]** verify `develop` is fully merged. files: none (commands only). depends_on: none. acceptance: `git merge-base --is-ancestor origin/develop origin/main` succeeds and the ahead/behind left side is `0`.
- [ ] **T0.3 [OPS]** delete merged remote branches plus `develop` last. files: none (commands only). depends_on: T0.1, T0.2. acceptance: per-branch `MERGED` or `LANDED` proof recorded in the Phase 0 issue before each `git push origin --delete`; Dependabot branches untouched.
- [ ] **T0.4 [OPS]** clean the local clone. files: none. depends_on: T0.3. acceptance: local `main` tracks `origin/main`; `git branch -d` refused nothing silently; `git worktree list` shows no workflow leftovers.
- [ ] **T0.5 [QA]** verify the prune. files: none. depends_on: T0.4. acceptance: `git ls-remote` shows `main` plus only unmerged work; Phase 0 issue closed.

## Phase 1 — Gate `main`

- [ ] **T1.1 [OPS]** apply the `main` ruleset (PR required, `ci-required`, conversation resolution, linear history, no force-push, no deletion, empty bypass). files: none (web UI or API). depends_on: T0.5. acceptance: `gh api repos/amurru/hakase/rulesets/20210676` shows the required rules; direct push to `main` rejected.
- [ ] **T1.2 [OPS]** apply merge settings and validate the merge queue. files: none (API + web UI). depends_on: T1.1. acceptance: squash-only, auto-merge on, delete-on-merge on, update-branch on; queue availability or documented fallback recorded in the Phase 1 issue.
- [ ] **T1.3 [CI]** rewrite `test.yml` triggers, concurrency, timeouts, `ci-required`, PR-title check, SHA pins. files: `.github/workflows/test.yml`. depends_on: T1.2. acceptance: `linux`, `windows`, `ci-required` run on PR, `push: main`, and `merge_group`; a green PR reports `ci-required` success.
- [ ] **T1.4 [CI]** add the release guard job. files: `.github/workflows/release.yml`. depends_on: T1.3. acceptance: tag-off-`main` and empty-CHANGELOG tags fail the guard before building.
- [ ] **T1.5 [OPS]** add the `v*` tag ruleset. files: none (web UI). depends_on: T1.2. acceptance: tag update/delete blocked.
- [ ] **T1.6 [QA]** throwaway-PR proof of the whole gate. files: none (one-line docs change). depends_on: T1.3, T1.4, T1.5. acceptance: direct push rejected, `ci-required` required, queue or fallback merges the PR, Phase 1 issue closed.

## Phase 2 — Agent contract

- [ ] **T2.1 [DOCS]** add the agent issue form and labels. files: `.github/ISSUE_TEMPLATE/agent_task.yml` (create). depends_on: T1.6. acceptance: new agent issues carry context, acceptance, target files, test plan, risk, dependencies, out-of-scope; labels exist.
- [ ] **T2.2 [DOCS]** add nested `AGENTS.md` files plus risk policy and tasks schema in `CONTRIBUTING.md`. files: `webui/AGENTS.md`, `internal/agent/AGENTS.md`, `cmd/hakase/AGENTS.md`, `.github/AGENTS.md` (all create), `CONTRIBUTING.md` (append risk tiers + task columns). depends_on: T1.6. acceptance: each file under 60 lines with subtree commands, invariants, and human-review paths; root file links the workflow spec.
- [ ] **T2.3 [TOOL]** add `make wt`. files: `Makefile`. depends_on: none. acceptance: `make wt ISSUE=0 TOOL=test SLUG=dry` fails safe without touching a real branch (run with `git worktree list` check, then remove); missing args print usage.
- [ ] **T2.4 [QA]** two-agent pilot on non-overlapping issues. files: per-issue targets. depends_on: T2.1, T2.2, T2.3. acceptance: both PRs land without manual rebase or shared-file edits; worktrees removed; pilot agents ran under the scoped push credential; friction recorded in the Phase 2 issue.

## Phase 3 — Measure and tune

- [ ] **T3.1 [QA]** record the pre-change baseline. files: none. depends_on: none (run before T1.1). acceptance: cycle time, review time, CI durations, revert count, stale branch count captured in the Phase 3 issue.
- [ ] **T3.2 [QA]** compare two weeks after Phase 2 and decide on follow-ups. files: none. depends_on: T2.4, T3.1. acceptance: comparison plus a written keep/drop decision on stacked PRs, release-please, and a dispatcher.
