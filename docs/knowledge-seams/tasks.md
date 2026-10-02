# Tasks: Knowledge model-seam rewiring (KS)

Restore two model-backed callbacks dead since the Aug DI migration.
Spec: [spec.md](spec.md) — plan: [plan.md](plan.md) — issue
[#69](https://github.com/amurru/hakase/issues/69).
House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase A — testable seam helper

- [x] **T1.1 [BE]** `internal/agent/knowledge_seams.go`:
      `buildExpandQueryFn(prompt, buildPrompt, parse)` (closure body
      moved verbatim from `SetupRunner`) +
      `wireKnowledgeModelSeams(prompt, buildPrompt, parse)` assigning
      both `knowledge` package vars directly. Spec: KS-002.
- [x] **T1.2 [BE]** `SetupRunner` calls the helper; delete dead
      `Deps.EnrichKnowledgeFn` / `Deps.ExpandQueryFn`; correct the two
      lying doc comments in `knowledge.go`. Spec: KS-002.

## Phase B — proof it fires

- [x] **T2.1 [QA]** helper unit tests (canned-array success, garbage
      failure, nil-parse path) + bridge regression test (wired
      `ExpandSearchQuery` expands). Red proof verified: with the two
      assignment lines neutered, `TestWireKnowledgeModelSeams` fails
      ("must be assigned by wiring"); restored, it passes. Residual,
      accepted: the test pins the helper, not the one-line
      `SetupRunner` call itself — same exposure as every other
      `SetupRunner` line (incl. the `EmbedFn` wiring); no
      `SetupRunner`-level harness exists. Spec: KS-005.
- [ ] **T2.2 [QA]** manual live smoke, recorded here: expansion model
      call visible with `search_expansion: true`; enriched
      frontmatter on `save_knowledge`. Status: DEFERRED — no model
      credentials on this host (no keys, no config.json), so no live
      model call is possible here; needs a keyed environment. Full
      suite green (`gofmt`, `vet`, `go test ./...`, `-race` on
      agent/knowledge, `pnpm` backstop). Spec: KS-005.

## Phase C — docs + close

- [x] **T3.1 [DOCS]** CHANGELOG Fixed entry (incl. the enrichment
      cost note), #69 close on merge. No roadmap change (bugfix
      restore, not scope shift).
