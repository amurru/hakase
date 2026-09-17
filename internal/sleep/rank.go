// rank.go - rank_and_select for the optimizer's edit pool (plan Phase 3,
// SL-030): when the reflector over-proposes past the learning rate, an
// optimizer LLM ranks the pool and keeps the top-budget edits; with no
// ranker wired the documented fallback is truncation. Every decision lands
// in RankingDetails so the night report shows why each edit lived or died.
package sleep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"amurru/hakase/internal/skill"
)

// RankingDetail is one pool edit's fate for the report/diagnostics.
type RankingDetail struct {
	// Index is the edit's position in the reflector's original pool.
	Index     int     `json:"index"`
	Op        string  `json:"op"`
	Anchor    string  `json:"anchor,omitempty"`
	Rationale string  `json:"rationale,omitempty"`
	Score     float64 `json:"score,omitempty"`
	Selected  bool    `json:"selected"`
	// Reason explains a non-selection (truncation fallback, ranker drop).
	Reason string `json:"reason,omitempty"`
}

// Ranker selects at most budget edits from the pool. Implementations must
// not mutate the input slice. The returned details cover every pool edit.
type Ranker func(ctx context.Context, edits []skill.TextEdit, budget int) ([]skill.TextEdit, []RankingDetail, error)

// BuildRankPrompt renders the rank_and_select prompt for wiring or tests.
func BuildRankPrompt(edits []skill.TextEdit, budget int) string {
	var b strings.Builder
	b.WriteString("You are the optimizer for a markdown skill's learned procedures.\n")
	b.WriteString("A reflector proposed the candidate edits below; the learning rate allows ")
	b.WriteString(fmt.Sprintf("at most %d this epoch. Select the %d edits with the highest expected held-out improvement.\n\n", budget, budget))
	for i, e := range edits {
		fmt.Fprintf(&b, "Edit %d: op=%s\n", i, e.Op)
		if e.Anchor != "" {
			fmt.Fprintf(&b, "  anchor:    %s\n", e.Anchor)
		}
		if e.Content != "" {
			fmt.Fprintf(&b, "  content:   %s\n", e.Content)
		}
		if e.Rationale != "" {
			fmt.Fprintf(&b, "  rationale: %s\n", e.Rationale)
		}
	}
	b.WriteString("\nReply with ONLY a JSON object: {\"selected\": [<edit indices>, ...]} ")
	b.WriteString("with at most " + fmt.Sprint(budget) + " indices, highest-impact first.")
	return b.String()
}

// ParseRankReply parses a ranker reply: {"selected":[...]} (fenced or raw)
// or a bare JSON integer array. Indices out of range or duplicated are
// dropped; order follows the reply (rank order), not the pool order.
func ParseRankReply(raw string, poolLen int) ([]int, bool) {
	doc := raw
	if start := strings.Index(doc, "```"); start != -1 {
		rest := doc[start+3:]
		if nl := strings.Index(rest, "\n"); nl != -1 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end != -1 {
			doc = rest[:end]
		}
	}
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil, false
	}
	var indices []int
	var wrapped struct {
		Selected []int `json:"selected"`
	}
	if err := json.Unmarshal([]byte(doc), &wrapped); err == nil {
		indices = wrapped.Selected
	} else if err := json.Unmarshal([]byte(doc), &indices); err != nil {
		return nil, false
	}
	seen := make(map[int]bool, len(indices))
	out := make([]int, 0, len(indices))
	for _, i := range indices {
		if i < 0 || i >= poolLen || seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	return out, true
}

// DefaultRanker builds a Ranker over a model caller. A parse failure or
// caller error surfaces to the caller, which degrades to truncation.
func DefaultRanker(call ModelCaller) Ranker {
	return func(ctx context.Context, edits []skill.TextEdit, budget int) ([]skill.TextEdit, []RankingDetail, error) {
		raw, err := call(ctx, BuildRankPrompt(edits, budget))
		if err != nil {
			return nil, nil, err
		}
		indices, ok := ParseRankReply(raw, len(edits))
		if !ok {
			return nil, nil, fmt.Errorf("ranker reply did not parse")
		}
		if len(indices) > budget {
			indices = indices[:budget]
		}
		details := make([]RankingDetail, len(edits))
		for i, e := range edits {
			details[i] = RankingDetail{Index: i, Op: e.Op, Anchor: e.Anchor, Rationale: e.Rationale}
		}
		selected := make([]skill.TextEdit, 0, len(indices))
		for rank, i := range indices {
			selected = append(selected, edits[i])
			details[i].Selected = true
			details[i].Score = float64(len(indices) - rank)
		}
		for i := range details {
			if !details[i].Selected {
				details[i].Reason = "ranked below the learning rate"
			}
		}
		return selected, details, nil
	}
}

// rankAndSelect applies the budget to the edit pool: no-op at or under
// budget, ranker when wired, documented truncation fallback otherwise
// (including on ranker failure - a ranking outage must not kill the night).
func rankAndSelect(ctx context.Context, edits []skill.TextEdit, budget int, ranker Ranker) ([]skill.TextEdit, []RankingDetail, bool) {
	if len(edits) <= budget {
		return edits, nil, false
	}
	if ranker != nil {
		selected, details, err := ranker(ctx, edits, budget)
		if err == nil && len(selected) > 0 {
			return selected, details, true
		}
	}
	details := make([]RankingDetail, len(edits))
	for i, e := range edits {
		details[i] = RankingDetail{Index: i, Op: e.Op, Anchor: e.Anchor, Rationale: e.Rationale}
		if i < budget {
			details[i].Selected = true
		} else {
			details[i].Reason = "fallback truncation (no ranker)"
		}
	}
	return edits[:budget], details, true
}
