// hybrid.go - hybrid BM25 + dense retrieval fused via RRF (HR-005).
//
// The BM25 branch is the existing SearchKnowledgeScored, unchanged. The
// dense branch cosine-ranks every gate-passing note (not just substring
// matches — paraphrase recall is the point) and keeps the top denseTopK.
// The two ranked lists fuse with the existing fuseRRF (k=60, shared with
// query-expansion fusion): rank-based fusion needs no cross-scale score
// calibration. Either branch empty degrades to the other; EmbedFn nil or
// error degrades to BM25-only with a log line, never a search error.

package knowledge

import (
	"context"
	"sort"
)

// denseTopK caps the dense branch before fusion. Dense ranks below this
// contribute only RRF dust (1/(60+rank)); the cap keeps the fused set
// explainable and bounds work on large bases.
const denseTopK = 20

// HybridSearch ranks notes for query by fusing BM25 and dense retrieval.
// hybrid false, EmbedFn nil, or any embedding failure returns exactly what
// SearchKnowledgeScored returns. model names the embedding model for
// sidecar drift detection (HR-004). log may be nil (tests, CLI quiet
// paths); degrade warnings are best-effort.
func HybridSearch(ctx context.Context, log LogFunc, dir string, idx *KnowledgeIndex, query string, tags []string, includeArchived, hybrid bool, model string) []ScoredKnowledgeNote {
	bm25 := SearchKnowledgeScored(idx, query, tags, includeArchived)
	if !hybrid || EmbedFn == nil {
		return bm25
	}

	warn := func(msg string) {
		if log != nil {
			log(msg)
		}
	}

	qvecs, err := EmbedFn(ctx, []string{query})
	if err != nil {
		warn("hybrid search: query embedding failed, BM25-only: " + err.Error())
		return bm25
	}
	if len(qvecs) == 0 || len(qvecs[0]) == 0 {
		warn("hybrid search: empty query embedding, BM25-only")
		return bm25
	}

	vecs, _, err := ensureVectors(ctx, dir, idx, model, len(qvecs[0]))
	if err != nil {
		warn("hybrid search: note embeddings failed, BM25-only: " + err.Error())
		return bm25
	}

	type ranked struct {
		note  KnowledgeNote
		score float64
	}
	var dense []ranked
	for _, note := range idx.BySlug {
		if !notePassesGate(note, tags, includeArchived) {
			continue
		}
		v, ok := vecs[note.Slug]
		if !ok {
			continue // skipped slug (hostile name, vanished note)
		}
		dense = append(dense, ranked{note: *note, score: cosine(qvecs[0], v)})
	}
	sort.SliceStable(dense, func(i, j int) bool {
		if dense[i].score != dense[j].score {
			return dense[i].score > dense[j].score
		}
		return dense[i].note.Frontmatter.Title < dense[j].note.Frontmatter.Title
	})
	if len(dense) > denseTopK {
		dense = dense[:denseTopK]
	}
	denseSet := make([]ScoredKnowledgeNote, 0, len(dense))
	for _, d := range dense {
		denseSet = append(denseSet, ScoredKnowledgeNote{Note: d.note, Score: d.score})
	}

	switch {
	case len(bm25) == 0:
		return denseSet
	case len(denseSet) == 0:
		return bm25
	default:
		return fuseRRF([][]ScoredKnowledgeNote{bm25, denseSet})
	}
}
