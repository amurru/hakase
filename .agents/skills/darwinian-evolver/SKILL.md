---
name: darwinian-evolver
description: 'Use when the user asks to auto-improve saved skills, evolve a failing skill from its eval failures, run the nightly skill-evolution pass, or review evolution reports. Runs hakase native evolution loop over the Python skill library (skills/skills.json): evaluate, mutate, select via A/B gate, report to outputs/cron/.'
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

**Status: native engine (evolver.go).** Unlike the Hermes upstream skill,
which wrapped Imbue's AGPL-3.0 `darwinian_evolver` CLI, hakase reimplements
the tripartite contract (organism / evaluator / mutator / selection) in Go
with NO external dependency and NO AGPL import. No `uv`, no OpenRouter key,
no separate cache dir.

## When to Use

- The user asks to "evolve", "auto-improve", or "self-optimize" the saved
  Python skills.
- A saved skill keeps failing its eval cases and the user wants it fixed
  automatically.
- Reviewing the nightly evolution report in `outputs/cron/evolve-*.md`.
- Setting up the nightly evolution cron job.

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
go run . skill evolve

# With mutation (requires a configured model in config.json)
go run . skill evolve --mutate

# Custom dir / report path
go run . skill evolve --dir ./skills --report outputs/cron/evolve-manual.md
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

## References

- Imbue's darwinian-evolver research post (the contract this loop follows):
  https://imbue.com/research/2026-02-27-darwinian-evolver/
- PromptBreeder (the underlying prompt-evolution idea):
  https://arxiv.org/abs/2309.16797
- hakase engine source: `evolver.go` (native Go, MIT - no AGPL import)

---

*Ported to hakase from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT). Original: darwinian-evolver skill by Bihruze (Asahi0x) and Hermes Agent (MIT). hakase reimplements the evolution loop natively in Go (evolver.go); the upstream imbue-ai/darwinian_evolver (AGPL-3.0) is referenced for its contract only and is never imported or wrapped.*
