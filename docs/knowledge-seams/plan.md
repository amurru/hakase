# Plan: Knowledge model-seam rewiring (KS)

Spec: [spec.md](spec.md) — tasks: [tasks.md](tasks.md) — issue
[#69](https://github.com/amurru/hakase/issues/69).

## Phase A — testable seam helper (no behavior change)

A1. New `internal/agent/knowledge_seams.go`: `buildExpandQueryFn`
    taking explicit dependencies — `prompt func(ctx, string)
    (string, error)`, `buildPrompt func(string) string`,
    `parse func(string) []string` — returning the expansion closure
    (body moved verbatim from the `SetupRunner` inline closure).
    `ModelPromptFn` is a plain func, not a var, so the helper must take
    it as a parameter for tests to stub it.
A2. `SetupRunner` calls `wireKnowledgeModelSeams(deps)` (new, same
    file): assigns `knowledge.EnrichKnowledgeFn = ModelPromptFn`,
    `knowledge.ExpandQueryFn = buildExpandQueryFn(ModelPromptFn,
    deps.BuildQueryExpansionPromptFn, deps.ParseQueryExpansionsFn)`.
    Direct package-var assignment — the `EmbedFn` precedent — never a
    Deps field.
A3. Delete dead `Deps.EnrichKnowledgeFn` / `Deps.ExpandQueryFn`
    (repo-wide zero readers) + correct the two `knowledge.go` doc
    comments to name the helper.

## Phase B — proof it fires (the test today's suite lacks)

B1. Unit: `buildExpandQueryFn` with a stub prompt returning a canned
    JSON array → closure returns 3 phrasings; stub returning garbage →
    closure errors (fail-open preserved upstream). `EnrichKnowledgeFn`
    assignment: after `wireKnowledgeModelSeams`-equivalent call with
    stub deps... (helper takes no ModelPromptFn stub seam — see A1:
    pass prompt explicitly so tests inject failures without models).
B2. Bridge regression test: with stub prompt/build/parse fns,
    `knowledge.ExpandSearchQuery` returns expanded phrasings after
    wiring (fails on today's code — package var stays nil). Save the
    pre-fix run as the red proof.
B3. Live smoke (manual, recorded in tasks): real binary,
    `search_expansion: true`, one search → audit/log shows the
    expansion model call; `save_knowledge` → enriched frontmatter
    (summary/tags beyond deterministic extraction).

## Phase C — docs + close

C1. CHANGELOG Fixed entry (user-visible: two documented features
    actually start working), tasks ticked, #69 closed on merge.
    No roadmap change (no scope/priority shift — a bugfix restore).
C2. Full suite: `gofmt`, `go vet ./...`, `go test ./...`, `pnpm test`
    backstop. `go test -race` on agent/knowledge (package-var
    assignment at startup + concurrent reads).

## Risks

- Enrichment cost surprise (KS-004): every agent `save_knowledge`
  gains a model call. Mitigated by documenting in the CHANGELOG entry
  and the fail-open fallback; kill-switch only if review demands it.
- `wireKnowledgeModelSeams` runs at every `SetupRunner` (TUI, web,
  serve, headless): assignment is idempotent plain stores — safe.
- Concurrent search during (re)setup: package-var write races a read
  only if SetupRunner ever re-runs in-process; today it runs once per
  process. `-race` in C2 covers the suite; note any hit in tasks.
