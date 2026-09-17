// recall.go - lessons recall for the sleep cycle (plan Phase 3, SL-033):
// BM25 retrieval over the knowledge base reusing knowledge/score.go's
// scorer - no embeddings, no network. Session intents are reduced to
// distinctive keyword queries (the knowledge search matches whole-query
// substrings, so raw multi-sentence intents would match nothing); the
// per-keyword result sets fuse by max score and the top-k notes are
// rendered into the reflector's optimizer context.
package sleep

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"amurru/hakase/internal/knowledge"
)

// Recall query caps: at most this many keyword queries per group (each is a
// cheap in-memory BM25 pass) and this minimum token length to qualify.
const (
	MaxRecallQueries  = 16
	MinRecallTokenLen = 4
)

// recallQueries reduces intents to distinctive lowercase keyword queries.
func recallQueries(intents []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, intent := range intents {
		for _, tok := range strings.FieldsFunc(strings.ToLower(intent), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}) {
			if len(tok) < MinRecallTokenLen {
				continue
			}
			if seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
			if len(out) >= MaxRecallQueries {
				return out
			}
		}
	}
	return out
}

// RecalledNote is one knowledge note recalled for a skill group.
type RecalledNote struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Score   float64  `json:"score"`
}

// RecallNotes BM25-ranks the knowledge base against the intents and returns
// the top-k distinct notes (best score across queries wins). Best-effort:
// an unreadable or empty knowledge base recalls nothing without error.
func RecallNotes(dir string, intents []string, k int) []RecalledNote {
	if dir == "" || k <= 0 || len(intents) == 0 {
		return nil
	}
	idx, err := knowledge.BuildKnowledgeIndex(dir)
	if err != nil || idx == nil {
		return nil
	}
	queries := recallQueries(intents)
	if len(queries) == 0 {
		return nil
	}
	best := make(map[string]RecalledNote, len(queries))
	for _, q := range queries {
		for _, sn := range knowledge.SearchKnowledgeScored(idx, q, nil, false) {
			if sn.Score <= 0 {
				continue
			}
			prev, ok := best[sn.Note.Slug]
			if !ok || sn.Score > prev.Score {
				best[sn.Note.Slug] = RecalledNote{
					Slug:    sn.Note.Slug,
					Title:   sn.Note.Frontmatter.Title,
					Tags:    sn.Note.Frontmatter.Tags,
					Summary: sn.Note.Frontmatter.Summary,
					Score:   sn.Score,
				}
			}
		}
	}
	if len(best) == 0 {
		return nil
	}
	out := make([]RecalledNote, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// RenderRecallBlock renders recalled notes for the reflector's optimizer
// context. Redaction is defense-in-depth: notes are operator-authored, but
// the block leaves the process like everything else.
func RenderRecallBlock(notes []RecalledNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Recalled lessons (knowledge base)\n\n")
	b.WriteString("Prior lessons that may bear on tonight's failures:\n")
	for _, n := range notes {
		line := "- " + n.Title
		if n.Summary != "" {
			line += ": " + n.Summary
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	return b.String()
}
