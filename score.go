// score.go - BM25-style relevance scoring for knowledge notes (Phase 3d-1).
//
// The result SET of a search is unchanged: a note still matches only when the
// raw query is a case-insensitive substring of one of its searchable fields.
// Scoring only REORDERS the matched set (score-descending, alphabetical
// tiebreak), so existing behavior is preserved apart from the new order.
//
// The scorer is a fielded BM25: each note's searchable fields are scored
// separately against the query terms, weighted by a per-field boost, and
// summed. Title/alias/tag matches outrank summary/metadata/body, mirroring
// the qmd skill's 2x weight on original-query paths (see plan Phase 3d-1).
package main

import (
	"math"
	"sort"
	"strings"
)

// ScoredKnowledgeNote couples a knowledge note with its relevance score.
type ScoredKnowledgeNote struct {
	Note  KnowledgeNote
	Score float64
}

// searchField is a weighted searchable field of a knowledge note.
type searchField struct {
	name  string
	boost float64
}

// searchFields lists the note fields searched by SearchKnowledge, with the
// boost applied to term matches in each field during BM25 scoring.
var searchFields = []searchField{
	{"title", 3.0},
	{"aliases", 2.0},
	{"tags", 2.0},
	{"summary", 2.0},
	{"metadata", 1.5},
	{"body", 1.0},
}

const (
	// bm25K1 controls term-frequency saturation (higher = more saturation).
	bm25K1 = 1.2
	// bm25B controls length normalization (0 = none, 1 = full).
	bm25B = 0.75
	// rrfK is the k constant for Reciprocal Rank Fusion in query expansion.
	rrfK = 60.0
)

// bm25Corpus holds corpus-level statistics: document count, per-term
// document frequency (term present in ANY field of a note), and per-field
// average token length.
type bm25Corpus struct {
	numDocs     int
	docFreq     map[string]int
	avgFieldLen map[string]float64
}

// fieldText returns the raw text of a note field by name.
func fieldText(n *KnowledgeNote, name string) string {
	switch name {
	case "title":
		return n.Frontmatter.Title
	case "aliases":
		return strings.Join(n.Frontmatter.Aliases, " ")
	case "tags":
		return strings.Join(n.Frontmatter.Tags, " ")
	case "summary":
		return n.Frontmatter.Summary
	case "metadata":
		var b strings.Builder
		for _, v := range n.Frontmatter.Metadata {
			b.WriteString(v)
			b.WriteString(" ")
		}
		return b.String()
	case "body":
		return n.Body
	}
	return ""
}

// buildBM25Corpus precomputes corpus statistics over all notes in the index.
// It is called once per scored search (a cheap map iteration, bounded by the
// note count) and reused for every matched note.
func buildBM25Corpus(idx *KnowledgeIndex) *bm25Corpus {
	c := &bm25Corpus{
		docFreq:     make(map[string]int),
		avgFieldLen: make(map[string]float64),
	}
	fieldTotals := make(map[string]int)

	for _, note := range idx.BySlug {
		docTerms := make(map[string]bool)
		for _, f := range searchFields {
			counts := tokenCounts(fieldText(note, f.name))
			fieldTotals[f.name] += len(tokenize(fieldText(note, f.name)))
			for t := range counts {
				docTerms[t] = true
			}
		}
		for t := range docTerms {
			c.docFreq[t]++
		}
		c.numDocs++
	}

	for _, f := range searchFields {
		if c.numDocs > 0 {
			c.avgFieldLen[f.name] = float64(fieldTotals[f.name]) / float64(c.numDocs)
		}
	}
	return c
}

// noteFieldCounts returns, for a note, the token count per field.
func noteFieldCounts(n *KnowledgeNote) map[string]int {
	counts := make(map[string]int, len(searchFields))
	for _, f := range searchFields {
		counts[f.name] = len(tokenize(fieldText(n, f.name)))
	}
	return counts
}

// noteTermCounts returns, for a note, the occurrence count of each token in
// each field, keyed as field -> term -> count.
func noteTermCounts(n *KnowledgeNote) map[string]map[string]int {
	byField := make(map[string]map[string]int, len(searchFields))
	for _, f := range searchFields {
		byField[f.name] = tokenCounts(fieldText(n, f.name))
	}
	return byField
}

// scoreKnowledge ranks the given (already substring-matched) notes for query
// using fielded BM25. Returns scored notes sorted by score descending with
// the note title as tiebreaker. When the query tokenizes to nothing, all
// scores are 0 and the order is purely alphabetical (matches the old sort).
func scoreKnowledge(idx *KnowledgeIndex, query string, notes []KnowledgeNote) []ScoredKnowledgeNote {
	terms := tokenize(query)
	out := make([]ScoredKnowledgeNote, 0, len(notes))
	if len(terms) == 0 {
		for _, n := range notes {
			out = append(out, ScoredKnowledgeNote{Note: n, Score: 0})
		}
		sortScoredByTitle(out)
		return out
	}

	corpus := buildBM25Corpus(idx)
	type noteTokens struct {
		fieldLen  map[string]int
		termCount map[string]map[string]int
	}
	cache := make(map[string]*noteTokens, len(notes))
	for i := range notes {
		n := &notes[i]
		cache[n.Slug] = &noteTokens{
			fieldLen:  noteFieldCounts(n),
			termCount: noteTermCounts(n),
		}
	}

	for i := range notes {
		n := &notes[i]
		nt := cache[n.Slug]
		var score float64
		for _, term := range terms {
			df, ok := corpus.docFreq[term]
			if !ok || df <= 0 {
				continue
			}
			idf := math.Log1p((float64(corpus.numDocs-df) + 0.5) / (float64(df) + 0.5))
			if idf == 0 {
				continue
			}
			for _, f := range searchFields {
				tf := nt.termCount[f.name][term]
				if tf == 0 {
					continue
				}
				avg := corpus.avgFieldLen[f.name]
				if avg <= 0 {
					avg = 1
				}
				denom := float64(tf) + bm25K1*(1-bm25B+bm25B*float64(nt.fieldLen[f.name])/avg)
				tfNorm := float64(tf) * (bm25K1 + 1) / denom
				score += f.boost * idf * tfNorm
			}
		}
		out = append(out, ScoredKnowledgeNote{Note: *n, Score: score})
	}

	sortScored(out)
	return out
}

// sortScored orders scored notes by score descending, title ascending.
func sortScored(notes []ScoredKnowledgeNote) {
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].Score != notes[j].Score {
			return notes[i].Score > notes[j].Score
		}
		return notes[i].Note.Frontmatter.Title < notes[j].Note.Frontmatter.Title
	})
}

// sortScoredByTitle orders scored notes by title ascending (zero-score path).
func sortScoredByTitle(notes []ScoredKnowledgeNote) {
	sort.SliceStable(notes, func(i, j int) bool {
		return notes[i].Note.Frontmatter.Title < notes[j].Note.Frontmatter.Title
	})
}

// fuseRRF merges scored note sets from multiple query phrasings using
// Reciprocal Rank Fusion (plan Phase 3d-4). Each phrasing's rank contributes
// 1/(k + rank); notes are sorted by fused score descending.
func fuseRRF(sets [][]ScoredKnowledgeNote) []ScoredKnowledgeNote {
	fused := make(map[string]float64)
	notes := make(map[string]KnowledgeNote)
	for _, set := range sets {
		for rank, sn := range set {
			fused[sn.Note.Slug] += 1.0 / (rrfK + float64(rank+1))
			if _, ok := notes[sn.Note.Slug]; !ok {
				notes[sn.Note.Slug] = sn.Note
			}
		}
	}
	out := make([]ScoredKnowledgeNote, 0, len(fused))
	for slug, score := range fused {
		out = append(out, ScoredKnowledgeNote{Note: notes[slug], Score: score})
	}
	sortScored(out)
	return out
}
