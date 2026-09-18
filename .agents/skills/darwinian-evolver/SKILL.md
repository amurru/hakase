---
name: darwinian-evolver
description: 'Use when the user asks to auto-improve saved skills, evolve a failing skill from its eval failures, run the nightly skill-evolution pass, or review evolution reports. Runs hakase native evolution loop over the Python skill library (skills/skills.json): evaluate, mutate, select via A/B gate, report to outputs/cron/. Also drives the SkillOpt-Sleep nights (Go port of Microsoft SkillOpt, MIT) that evolve markdown SKILL.md documents offline: harvest sessions, mine tasks, replay, gate, stage, adopt.'
license: MIT
metadata:
  author: 'Bihruze (Asahi0x), Hermes Agent (MIT); reimplemented natively in hakase Go (upstream imbue-ai/darwinian_evolver is AGPL-3.0 and is NOT imported)'
  version: 1.0.0
  source: https://github.com/NousResearch/hermes-agent/tree/main/optional-skills/research/darwinian-evolver
allowed-tools: read_file, write_file, patch, search_files, system_exec, python_interpreter, load_markdown_skill, delegate_task, cronjob, save_skill
---

# Darwinian Evolver (native hakase evolution layer)

Run hakase's built-in skill-evolution loop - a darwinian-evolver-style
mutate -> eval -> select cycle over the Python skill library
(`skills/` + `skills/skills.json`), driven by the existing cron scheduler.

**Status: native engine (evolver.go + internal/sleep).** Unlike the Hermes
upstream skill, which wrapped Imbue's AGPL-3.0 `darwinian_evolver` CLI,
hakase reimplements the tripartite contract (organism / evaluator / mutator /
selection) in Go with NO external dependency and NO AGPL import. No `uv`,
no OpenRouter key, no separate cache dir. The markdown-skill half is a
Go-native port of Microsoft SkillOpt (MIT) - see the attribution note in the
SkillOpt-Sleep section below.

## When to Use

- The user asks to "evolve", "auto-improve", or "self-optimize" the saved
  Python skills.
- A saved skill keeps failing its eval cases and the user wants it fixed
  automatically.
- Reviewing the nightly evolution report in `outputs/cron/evolve-*.md`.
- Setting up the nightly evolution cron job.
- The user asks to improve a **markdown** skill (`SKILL.md`) from real
  usage - the SkillOpt-Sleep night below (`hakase sleep run`).

Do **not** use this when:
- The user wants a quick manual fix to one skill - just edit the `.py`
  directly (or rewrite via `save_skill`).
- The skill has no eval set yet - the evolver skips skills that cannot be
  scored objectively (see "Eval sets" below).

## How the loop works

1. **Organism** - each entry in `skills/skills.json` (name, source `.py`,
   optional `skills/<name>.eval.json`).
2. **Evaluator** - runs the skill's entry function against its eval cases
   via the venv python. Score is 0-1. Cases are split trainable (visible to
   the mutator) vs holdout (used only to detect overfitting). Skills without
   an eval set, or whose module fails to load at all (broken seed), are
   skipped - the loop never evolves from a broken organism.
3. **Mutator** - for a skill with trainable failures, the configured model
   is prompted with the current source + the failure cases and asked to
   propose a fixed implementation. A reply with no code block is a no-op.
4. **Selection (A/B gate)** - a candidate is promoted only when it beats the
   incumbent by >=5% on the trainable score AND shows zero regressions on
   the holdout score. Promoted incumbents are preserved as `<name>.py.bak`.
   Rejected candidates are discarded.
5. **Deprecation** - skills whose eval hit rate falls below 30% are
   auto-marked `deprecated: true` in `skills/skills.json`.
6. **Report** - every pass writes an auditable markdown report to
   `outputs/cron/` for human review. No live self-modification: the pass
   only runs when explicitly triggered (cron job or CLI).

## Eval sets (skills/<name>.eval.json)

To make a skill evolvable, add an eval set next to its `.py`:

```json
{
  "cases": [
    {"name": "basic", "input": {"width": 8}, "expected": "result", "match": "contains", "train": true},
    {"name": "edge",  "input": {"width": 0}, "expected": "error",   "match": "regex",   "train": false}
  ]
}
```

- `input` - passed to the skill's entry function. A JSON object becomes
  `entry(**input)`; anything else is passed positionally.
- `expected` + `match` - `contains` (default), `exact`, or `regex`.
- `train` - `true` cases are shown to the mutator; `false` cases are
  holdout (overfitting guard). At least one trainable case is required for
  mutation.

Entry-point resolution: `main`, then `run`, `generate`, `solve`, then the
first public callable defined in the module.

## Running a pass

### CLI (evaluation-only by default)

```bash
# Evaluation-only: score every skill, deprecate <30% hit rate, write report
hakase skill evolve

# With mutation (requires a configured model in config.json)
hakase skill evolve --mutate

# Custom dir / report path
hakase skill evolve --dir ./skills --report outputs/cron/evolve-manual.md
```

### Nightly cron job (native, headless, no LLM session needed for scoring)

Create the evolution job with the `cronjob` tool (or `hakase cron`):

```json
{
  "action": "create",
  "name": "nightly evolution",
  "native": "evolve",
  "schedule": "every 24h"
}
```

The job runs the pass headless (mutations enabled), writes the report to
`outputs/cron/`, and marks the job completed. Review the report before
deleting any `.bak` files - the `.bak` is your one-command rollback.

## Reading the report

`outputs/cron/evolve-*.md` lists:
- Promoted mutations with train/holdout score deltas
- Rejected mutations with the rejection reason (gain below threshold,
  holdout regression, parse failure)
- Auto-deprecated skills (hit rate < 30%)
- Skipped skills (no eval set / broken seed)

## Hyperparameters and guards

| Guard | Value | Why |
|-------|-------|-----|
| Promotion threshold | >= 5% relative gain | only meaningful improvements win |
| Holdout regression | 0 allowed | overfitting is rejected outright |
| Deprecation threshold | < 30% hit rate | a skill failing most of its own cases is retired |
| Eval timeout | 90s per skill | a hung skill cannot block the pass |
| Mutator timeout | 60s per call | silent no-op on timeout |

## Pitfalls

1. **No eval set = skipped.** The evolver cannot score a skill without
   `skills/<name>.eval.json`. Write one before expecting evolution.
2. **Broken seeds are never mutated.** A skill whose module fails to load is
   skipped, not "fixed" - fix the syntax error manually first.
3. **Mutations are only as good as the eval set.** If the eval cases are
   wrong, the "improved" skill is wrong. Keep holdout cases representative.
4. **`.bak` is the rollback.** After a promoted mutation, `<name>.py.bak`
   holds the incumbent. Copy it back to revert.
5. **No live self-modification.** The pass never runs on its own; it runs
   from a cron job or `hakase skill evolve`. Human review of the report is
   the intended workflow.

## Markdown-skill nights (SkillOpt-Sleep)

The `.py` loop above evolves code skills. Markdown `SKILL.md` documents
evolve through a separate, more disciplined offline loop. **Attribution:**
the design is a Go-native port of Microsoft's **SkillOpt** (MIT,
https://github.com/microsoft/skillopt) - specifically the SkillOpt-Sleep
deployment workflow (harvest -> mine -> replay -> consolidate behind a
held-out gate -> stage -> adopt), reimplemented in hakase's `internal/sleep`
with no Python dependency and no code import; only the mechanisms (bounded
learned-block edits, validation gate, staged proposals pinned to a live-file
hash) are carried over. The full port plan lives at
`docs/skillopt-sleep/plan.md`. The verbs:

```bash
# Read-only counts: what would tonight mine? (no model call, no writes)
hakase sleep dry-run

# One full night: harvest -> mine -> consolidate behind the val gate -> stage
hakase sleep run

# From a specific reviewed task file instead of harvesting
hakase sleep run --tasks outputs/sleep/tasks.json

# Install a staged proposal after review (hash+realpath verified, .bak kept)
hakase sleep adopt --dir outputs/sleep/<ts>/<skill>
```

Data boundary and review gate:
- Harvest is local read-only; session excerpts are redacted and truncated
  BEFORE any model call, attachment bytes are never extracted.
- Task files mined from sessions carry a `generated_at` marker and are
  refused by real-backend runs until a human signs them:
  `hakase sleep review --tasks <file> --reviewer <name>`.
- Proposals land in `outputs/sleep/<night>/<skill>/` - nothing touches a
  live `SKILL.md` without `sleep adopt` (or `--auto-adopt`, which only ever
  applies to sleep-managed skills carrying a learned block).
- The native cron `sleep` job is created only via
  `hakase sleep schedule --at "0 3 * * *"` (CLI-only; the `cronjob` tool
  refuses privileged native jobs). `cron tick` fires it opportunistically
  while hakase runs; a true nightly guarantee needs `hakase cron tick` in
  the OS crontab.

Optimizer maturity knobs (off by default):
- **Learning-rate schedule** (`sleep.lr_scheduler` = constant | linear |
  cosine, decaying `edit_budget` toward `lr_floor` across `lr_horizon`
  nights). When the optimizer over-proposes past the learning rate, a
  rank-and-select model call picks the top edits; without one the pool is
  truncated. Every edit's fate lands in `ranking_details` in the report.
- **Slow update**: the state file's per-skill score history classifies each
  skill's trend (improved | regressed | persistent_fail | stable_success)
  and writes a protected `SLOW_UPDATE` guidance block onto the baseline
  plus a `references/optimizer-memory.md` sidecar next to the skill that is
  prepended to future reflect prompts. The sidecar is optimizer memory -
  never deployed with the skill, and step edits can never touch protected
  blocks.
- **Skill-aware reflection** (`sleep.skill_aware_reflection`, default
  off): the reflector classifies failures as `skill_defect` (gated
  learned-block edit) or `execution_lapse` (the guidance existed but was
  not followed). Lapse reminders land in the protected appendix, bypass
  the gate by design, are capped per night, and ship only with an accepted
  proposal; the report logs the bypass.
- **Recall & dream rollouts** (`sleep.recall_k`, `sleep.dream_rollouts` +
  `sleep.dream_factor`): recall pulls the top-k knowledge-base lessons
  (BM25, no embeddings) into the reflector context; dream rollouts
  synthesize contrastive training tasks that are quarantined to the train
  slice (they can sharpen reflection, never validate). Counts and
  estimated tokens appear in the night report.
- **Judge separation** (plan H2): set `sleep.judge_backend`/`sleep.judge_model`
  so rubric judging runs on a model distinct from the optimizer/target.
  `sleep run` refuses a shared judge unless `--allow-shared-judge`; the
  night report always names target, judge and optimizer models plus the
  `shared_backend` flag.
- **Claims need intervals** (`hakase sleep evalkit`): a paired A/B of a
  baseline vs candidate skill over one reviewed task manifest with
  McNemar's exact test and a seeded bootstrap CI. Single-seed deltas under
  1.5 points are noise by rule; wins inside that band are marked
  `[noise-range]` in reports - treat them as unproven, not celebrated.

```bash
# Prove a candidate before adopting: paired A/B with a confidence interval
hakase sleep evalkit --baseline <skill>/SKILL.md --candidate <skill>/SKILL.md.bak \
  --tasks outputs/sleep/tasks.json --metric mixed
```

## References

- Imbue's darwinian-evolver research post (the contract the `.py` loop follows):
  https://imbue.com/research/2026-02-27-darwinian-evolver/
- PromptBreeder (the underlying prompt-evolution idea):
  https://arxiv.org/abs/2309.16797
- Microsoft SkillOpt (MIT) - the research engine the markdown-skill
  SkillOpt-Sleep loop is ported from
  (skillopt_sleep/: harvest, mine, replay, consolidate, gate, stage, adopt):
  https://github.com/microsoft/skillopt
- hakase engine source: `evolver.go` (native Go, MIT - no AGPL import) and
  `internal/sleep/` (SkillOpt-Sleep port, MIT)

---

*Ported to hakase from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT). Original: darwinian-evolver skill by Bihruze (Asahi0x) and Hermes Agent (MIT). hakase reimplements the evolution loop natively in Go (evolver.go); the upstream imbue-ai/darwinian_evolver (AGPL-3.0) is referenced for its contract only and is never imported or wrapped. The markdown-skill SkillOpt-Sleep section is a Go-native port of Microsoft SkillOpt (MIT, https://github.com/microsoft/skillopt) - design port only, no code imported.*
