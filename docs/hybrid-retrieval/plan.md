# Plan: Hybrid retrieval for knowledge (HR)

Spec: [spec.md](spec.md) — tasks: [tasks.md](tasks.md) — issue
[#67](https://github.com/amurru/hakase/issues/67).

## Phase A — config + endpoint client (no behavior change)

A1. `internal/config`: `HybridSearch bool`, `KnowledgeEmbedModel`,
    `KnowledgeEmbedBaseURL` + env overrides
    (`HAKASE_HYBRID_SEARCH`, `HAKASE_KNOWLEDGE_EMBED_MODEL`,
    `HAKASE_KNOWLEDGE_EMBED_BASE_URL`) + `Validate()` rules per HR-003
    + `config.json.example` rows. Off/empty defaults: every existing
    config loads identically.
A2. `internal/agent/provider.go`: `OpenAIProvider.EmbedTexts` (batched
    POST `{base}/embeddings`, Bearer auth, 30s timeout, index-ordered
    `[][]float32`), unit-tested against `httptest` (multi-input order,
    batch splitting at 32, non-200 error text, timeout). No interface
    change; no `GeminiProvider` change.

## Phase B — storage + ranking (pure functions, no wiring)

B1. `internal/knowledge/vectors.go`: sidecar read/write/validity
    (`vectors/<slug>.vec.json`, HR-004 schema), `docText` (2000-rune
    truncation), cosine similarity, in-memory cache keyed by dir +
    fingerprint. Validity matrix unit-tested (fresh / stale mtime /
    model change / dim mismatch / corrupt JSON → re-embed).
B2. `internal/knowledge/hybrid.go`: `HybridSearch` per HR-005 —
    BM25 branch, dense branch (top-20 cosine over gate-passing notes),
    `fuseRRF` fuse, degrade-to-single-branch. Pure-function tests with
    a stub `EmbedFn` (fixed vectors): paraphrase query recalls the
    no-token-overlap note; exact-identifier query keeps BM25 order;
    nil `EmbedFn` / disabled → byte-identical to `SearchKnowledgeScored`;
    `EmbedFn` error → BM25-only, no error.

## Phase C — wiring + surfaces

C1. `setupRunner` builds `EmbedFn` when an embed model is configured;
    `CreateKnowledgeTools` gains `SearchOptions{Expansion, Hybrid}`
    (old 3-arg form kept as wrapper); `search_knowledge` calls
    `HybridSearch` when hybrid on, description +1 sentence.
    `deps.CreateKnowledgeToolsFn` signature follows; `cmd/hakase/main.go`
    bridge updated.
C2. CLI `knowledge search --hybrid` flag (direct `HybridSearch`; embed
    config from loaded config → `EmbedFn` built in-process, same
    closure as the agent path).
C3. Sleep recall and web REST untouched (HR-001 non-goals, recorded in
    tasks as explicit deferrals, not drift).

## Phase D — QA + docs

D1. Bench: `knowledge bench` gains hybrid rows on the existing
    recall@k/MRR format where an endpoint is configured; CI rows stay
    BM25-only (no network in CI). Fixture: paraphrase-pair notes proving
    the lexical gap closes.
D2. Docs: CHANGELOG entry, `config.json.example`, roadmap Tier-2 item 2
    marked shipped on merge, issue #67 closed, #20 checkbox ticked via
    comment (it stays open for transport + HITL).
D3. Full suite: `gofmt`, `go vet ./...`, `go test ./...`,
    `pnpm test` (untouched, backstop), windows-sensitive paths
    covered (sidecar file ops use the same atomic-write pattern as
    notes; no shell-outs, no symlinks).

## Risks

- Self-hosted `/v1/embeddings` shape drift (Ollama array input): batch
  size 32 is conservative; non-200 response bodies are surfaced
  verbatim so misbehaving endpoints are diagnosable, and search still
  degrades to BM25.
- Embedding latency on cold sidecars (whole-base backfill): one batched
  call, bounded by note count; subsequent searches embed only changed
  notes. No background goroutine, no prefetch — first hybrid search pays
  it, visibly, once.
- Dimension/model drift across config edits: stored `model`+`dim` in
  every sidecar makes this a lazy full re-embed, never a silent
  cosine-garbage mix.
