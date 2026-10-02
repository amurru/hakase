# Tasks: Hybrid retrieval for knowledge (HR)

Optional dense embeddings fused with BM25 via RRF, default off.
Spec: [spec.md](spec.md) — plan: [plan.md](plan.md) — issue
[#67](https://github.com/amurru/hakase/issues/67).
House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase A — config + endpoint client

- [x] **T1.1 [BE]** `internal/config`: `HybridSearch`,
      `KnowledgeEmbedModel`, `KnowledgeEmbedBaseURL` + env overrides +
      `Validate()` (hybrid needs a model; gemini-primary needs an
      embed base URL) + `config.json.example`. Spec: HR-003.
- [x] **T1.2 [BE]** `OpenAIProvider.EmbedTexts` (batches of 32, 30s
      timeout, index-ordered vectors, verbatim non-200 errors).
      Spec: HR-002.
- [x] **T1.3 [QA]** client tests over `httptest` (order, batching,
      error text) + config tests (defaults off, validation errors,
      env overrides). Spec: HR-002, HR-003.

## Phase B — storage + ranking

- [x] **T2.1 [BE]** `internal/knowledge/vectors.go`: sidecar
      read/write/validity, `docText`, cosine, dir-keyed cache.
      Spec: HR-004.
- [x] **T2.2 [BE]** `internal/knowledge/hybrid.go`: `HybridSearch`
      (BM25 branch + top-20 dense branch over gate-passing notes +
      `fuseRRF`; degrade paths). Spec: HR-005.
- [x] **T2.3 [QA]** validity matrix + hybrid ranking with stub
      `EmbedFn` (paraphrase recall, BM25 order preserved, nil-EmbedFn
      byte-identical, error fail-open). Spec: HR-004, HR-005, HR-007.

## Phase C — wiring + surfaces

- [x] **T3.1 [BE]** `setupRunner` builds `EmbedFn`; `SearchOptions`
      struct + wrapper; `search_knowledge` hybrid path + description;
      `deps` + `main.go` bridge; CLI `--hybrid`. Spec: HR-006.
- [x] **T3.2 [QA]** wiring tests: hybrid-on tool path with stub
      embeddings end-to-end (sidecar written, fused order);
      hybrid-off byte-identical; CLI flag smoke. Spec: HR-006.

## Phase D — QA + docs

- [x] **T4.1 [QA]** bench hybrid rows (endpoint-gated, skipped in CI)
      + paraphrase fixture pair. Full suite green (`gofmt`, `vet`,
      `go test ./...`, `pnpm test` backstop). End-to-end sign-off with
      a stub OpenAI-compatible endpoint: BM25 `recall@5 0.00` on a
      no-token-overlap query vs hybrid `1.00` (bench comparison
      tables), sidecars persisted. Spec: HR-005.
- [x] **T4.2 [DOCS]** CHANGELOG, roadmap Tier-2 item 2, #20 tick via
      comment, #67 close on merge. Explicit deferrals recorded: sleep
      recall BM25-only (SL-033), web REST BM25-only, SQLite-vec
      scale-out, per-chunk vectors. Spec: HR-001.
