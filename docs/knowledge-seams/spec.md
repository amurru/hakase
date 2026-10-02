# Spec: Knowledge model-seam rewiring (KS)

Restores two model-backed knowledge callbacks that have been silently
dead since the phase0-wave3 DI migration. Issue
[#69](https://github.com/amurru/hakase/issues/69).

## KS-001 Findings (evidence, not assumption)

- `knowledge.ExpandQueryFn` (`internal/knowledge/knowledge.go:569`) is
  read by `ExpandSearchQuery` (`:575`, nil-guarded silent fallback) and
  consumed by the `search_knowledge` tool when `search_expansion` is on.
  Production assigns it nowhere: `SetupRunner`
  (`internal/agent/agent.go:2149`) sets only `deps.ExpandQueryFn`.
- `knowledge.EnrichKnowledgeFn` (`knowledge.go:1171`) is read by
  `modelEnrichKnowledge` (`:1263`, nil-guarded silent fallback) and
  consumed by `save_knowledge` auto-enrichment. Production assigns it
  nowhere: `SetupRunner` (`agent.go:2143`) sets only
  `deps.EnrichKnowledgeFn`.
- Both `deps.*` fields (`internal/agent/deps.go:60-61`) are written once
  and read nowhere — pure dead stores (repo-wide grep, Oct 2026).
- Tests stub the package vars directly (`score_test.go:172-190`), so the
  prod bridge was never exercised.
- History: pre-migration the root package shared one lowercase
  `expandQueryFn` between setup and tools, so expansion worked. The
  ed53393 package split (Aug 10) set the Deps fields and left the
  package vars nil; c2668a7 deleted the bridge aliases. Dead for
  ~7 weeks. Both vars' doc comments ("set in setupRunner") describe the
  pre-migration world.
- The hybrid-retrieval `knowledge.EmbedFn` (added Oct 2026) does NOT have
  this defect: `SetupRunner` assigns the package var directly.

## KS-002 Scope

Rewire both seams so live runs match long-documented behavior; delete
the dead `Deps` fields so the trap cannot recur; correct the two lying
doc comments. Single source of truth: the `knowledge` package vars,
assigned from one unit-testable helper called by `SetupRunner`.

## KS-003 Non-goals

No ranking/fusion changes; no new config gates (faithful restore of
pre-migration behavior — see cost note); no CLI changes (CLI stays
model-free, deterministic paths only); no sleep/web changes (both are
BM25/deterministic by design).

## KS-004 Cost decision (evaluated, closed)

Re-enabling enrichment costs one model call per `save_knowledge`
(summary model else primary). Measured against the code, not estimated:
input is capped — note content truncated to 4000 chars (~1000 tokens)
plus at most 60 candidate lines of title/slug/summary (~1800 tokens)
plus template (~300 tokens) — worst case ~3000 in / ~200 out tokens,
typically far less, on the cheapest configured model. Saves happen a
handful of times per session at most: sub-cent per save, noise against
any real session. Decision: restore unconditionally (pre-migration
behavior, documented contract). A `knowledge_enrichment` kill-switch
was considered and rejected — an unnecessary knob for noise-level
cost; the fail-open fallback already covers outages. Expansion cost is
opt-in (`search_expansion`, default off) and needs no decision.

## KS-006 Evaluation record (Oct 2026, pre-implementation review)

- Sibling sweep: every other `Deps` callback field has live readers
  (`NewMCPServerManagerFn`, `DiscoverMarkdownSkillsFn`,
  `CreateKnowledgeToolsFn`, `CreateCronjobToolFn`,
  `StartCronSchedulerFn`, `Build/ParseQueryExpansion*Fn`,
  `ResolveVisionProviderFn`, `CreateMediaToolsFn` — all ≥1 reader);
  `skill.EvolveMutateFn` is assigned directly in `SetupRunner` (the
  healthy pattern). The two knowledge seams are the sole outliers.
- Alternative rejected: threading the callbacks through
  `CreateKnowledgeTools` parameters instead of package vars. The
  consumers (`ExpandSearchQuery`, `modelEnrichKnowledge`) read the
  package vars, as do tests and the sleep-adjacent paths; re-plumbing
  them would churn 3 bridges + tool signatures for zero benefit.
- `EmbedFn` (hybrid retrieval) evaluated and left unchanged: already
  directly assigned, conditional by design, no defect.
- Deletion safety: repo-wide grep shows zero readers of the two
  `Deps` fields including tests — deletion cannot break callers.
- Concurrency: assignment happens once per process inside
  `SetupRunner`; no re-assignment path exists (SIGHUP reload does not
  touch these). Reads are concurrent tool calls against a stable
  func value — safe; `-race` in QA confirms.

## KS-005 Failure semantics (unchanged)

Both call sites keep their fail-open fallbacks (nil callback, timeout,
unparseable reply → deterministic path, best-effort log). The rewire
adds no new error paths; static config validation is untouched.
A post-wiring smoke (stub endpoint or recorded reply) must prove each
seam fires, since today's tests prove only the stubs.
