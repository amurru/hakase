# Spec: Hybrid retrieval for knowledge (HR)

Fuses optional dense embeddings with the existing fielded BM25 via
Reciprocal Rank Fusion. Default off: zero new dependencies, zero network,
byte-identical search behavior unless the operator opts in.
Issue [#67](https://github.com/amurru/hakase/issues/67) — Tier-2 item 2
from #20.

## HR-001 Goals / non-goals

Goals: paraphrase queries recall notes with no token overlap (BM25's
blind spot) while exact-identifier queries keep BM25's precision;
operator enables it with two config keys and a local Ollama (or any
OpenAI-compatible endpoint); disabled-by-default keeps the
zero-dependency story intact.

Non-goals (explicit deferrals, not drift): sleep-cycle recall stays
BM25-only (`internal/sleep/recall.go` SL-033: no network in the sleep
loop); web REST `/knowledge/search` stays BM25-only until the tool path
proves out; no SQLite-vec/pgvector (personal knowledge bases are tens
to low-hundreds of notes — brute-force cosine in memory is ample; a
`vectors/` sidecar preserves files-as-truth without a new store);
per-chunk vectors (single vector per note; chunk pooling is future work
if long-note recall proves weak); query-embedding cache (one small call
per search; add LRU only if latency shows up).

## HR-002 Embedding endpoint client

`OpenAIProvider.EmbedTexts(ctx, apiKey, baseURL, model, texts)`
POSTs `{base}/embeddings` (`{"model", "input": [...]}`), Bearer auth,
30s timeout, returns `[][]float32` in input order. Batching: max 32
inputs per request (conservative for self-hosted shape drift; OpenAI
allows 2048), sequential batches. Non-200 → error quoting status +
body head (diagnosable endpoints). No change to `LLMProvider`;
no `GeminiProvider` change — Gemini-primary configs must point
`knowledge_embed_base_url` at an OpenAI-compatible endpoint (HR-003
fails fast otherwise). Template: the raw-HTTP `GetModelInfo` pattern
in `internal/agent/provider.go`.

## HR-003 Configuration

`knowledge.hybrid_search` (bool, default false),
`knowledge_embed_model` ("" = hybrid unavailable),
`knowledge_embed_base_url` ("" = reuse primary `base_url`),
env `HAKASE_HYBRID_SEARCH` / `HAKASE_KNOWLEDGE_EMBED_MODEL` /
`HAKASE_KNOWLEDGE_EMBED_BASE_URL`, `config.json.example` rows.
`Validate()` fails fast on: hybrid on with no embed model; gemini
primary + no embed base URL (native Gemini embeddings are out of
scope). Static misconfig never reaches search time.

## HR-004 Vector storage (sidecar, files stay truth)

`<knowledgedir>/vectors/<slug>.vec.json`:
`{model, dim, vector, note_size, note_mtime_unix_nano, embedded_at}`.
Validity = sidecar exists + parses + `model`/`dim` match current config
+ `note_size`/`mtime` match the note file; any mismatch → re-embed
lazily (batched, one call). Model/dim change therefore causes a lazy
full re-embed, never a silent cosine-garbage mix. The fingerprint walk
counts only `*.md`, so sidecars never invalidate the note index; a
dir-keyed in-memory vector cache mirrors the `GetKnowledgeIndex`
pattern. Writes reuse the atomic tmp+rename note pattern. Deleting a
note orphans its sidecar (harmless; rewrite on slug reuse is keyed by
validity, not existence).

Embedded document text (`docText`): title + aliases + summary + body,
truncated to 2000 runes (front-loads the fields BM25 already boosts;
single vector per note per the non-goals).

## HR-005 Hybrid ranking

`HybridSearch(ctx, idx, query, tags, includeArchived, hybrid bool)`:

1. BM25 branch = existing `SearchKnowledgeScored` (unchanged).
2. If `!hybrid` or `EmbedFn == nil` → return branch 1 verbatim.
3. Dense branch: embed the original query only (expansion phrasings,
   when on, stay BM25-only — bounds cost), cosine-rank **all**
   tag/archived-gate-passing notes (not just substring matches —
   paraphrase recall is the point), keep top 20.
4. Fuse the branch lists with the existing `fuseRRF` (k=60, shared
   with expansion fusion — rank-based, so no cross-scale score
   calibration is needed). Either branch empty → return the other.

`EmbedFn func(ctx, texts []string) ([][]float32, error)` is the
package-level seam (mirrors `ExpandQueryFn`); the agent setup builds it
from the embed config over `EmbedTexts`. Test seam: stub vectors.

## HR-006 Wiring

`CreateKnowledgeTools(log, dir, searchExpansion)` keeps its signature
via a `SearchOptions{Expansion, Hybrid}` struct + wrapper;
`search_knowledge` calls `HybridSearch` when hybrid is on (+1 sentence
in the tool description). `deps.CreateKnowledgeToolsFn` and the
`cmd/hakase/main.go` bridge follow. CLI `knowledge search --hybrid`
builds `EmbedFn` in-process from loaded config (same closure as the
agent path). Sleep/web paths untouched per HR-001.

## HR-007 Failure semantics (fail open to BM25)

Retrieval degradation beats retrieval errors: `EmbedFn` error/timeout
at search time → BM25-only + log line, never a tool error. Rationale:
search is advisory context, not a security gate (contrast hooks, which
fail closed). Static misconfig still fails fast at load (HR-003); only
runtime endpoint failures degrade.

## HR-008 Security

The endpoint URL + API key are operator config (same trust as the
existing `base_url`/`api_key` — no SSRF guard needed; not user input).
Keys never enter logs (error paths quote status/body only). Sidecars
are derived data (recomputable, validity-gated) — no integrity trust.
No permission or sandbox changes: embedding calls originate from the
already-networked agent/CLI process, and sleep (the offline loop) is
explicitly excluded.
