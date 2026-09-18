// rank_test.go - SL-030 acceptance: an over-budget pool is ranked when a
// ranker is wired, truncation is the documented fallback, and every pool
// edit's fate is recorded.
package sleep

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

func pool(n int) []skill.TextEdit {
	edits := make([]skill.TextEdit, 0, n)
	for i := 0; i < n; i++ {
		edits = append(edits, skill.TextEdit{
			Op: "add", Content: strings.Repeat("x", 1) + "edit number content " + string(rune('a'+i)),
			Rationale: "why",
		})
	}
	return edits
}

func TestRankAndSelectUnderBudgetIsNoop(t *testing.T) {
	edits := pool(3)
	selected, details, ranked := rankAndSelect(context.Background(), edits, 4, nil)
	if len(selected) != 3 || details != nil || ranked {
		t.Fatalf("under-budget pool must pass through: %d %v %v", len(selected), details, ranked)
	}
}

func TestRankAndSelectFallbackTruncation(t *testing.T) {
	edits := pool(6)
	selected, details, ranked := rankAndSelect(context.Background(), edits, 2, nil)
	if !ranked || len(selected) != 2 || len(details) != 6 {
		t.Fatalf("fallback truncation: %d %d %v", len(selected), len(details), ranked)
	}
	if selected[0].Content != edits[0].Content || selected[1].Content != edits[1].Content {
		t.Error("fallback keeps the first budget edits in pool order")
	}
	kept, dropped := 0, 0
	for _, d := range details {
		if d.Selected {
			kept++
		} else {
			dropped++
			if !strings.Contains(d.Reason, "fallback truncation") {
				t.Errorf("dropped edit must record the fallback reason, got %q", d.Reason)
			}
		}
	}
	if kept != 2 || dropped != 4 {
		t.Errorf("details selected/dropped = %d/%d, want 2/4", kept, dropped)
	}
}

func TestRankAndSelectWithRanker(t *testing.T) {
	edits := pool(6)
	ranker := func(_ context.Context, pool []skill.TextEdit, budget int) ([]skill.TextEdit, []RankingDetail, error) {
		// Rank picks indices 5 and 2 (out of order) as the top two.
		indices := []int{5, 2}
		var selected []skill.TextEdit
		details := make([]RankingDetail, len(pool))
		for i, e := range pool {
			details[i] = RankingDetail{Index: i, Op: e.Op}
		}
		for rank, idx := range indices {
			if rank >= budget {
				break
			}
			selected = append(selected, pool[idx])
			details[idx].Selected = true
		}
		return selected, details, nil
	}
	selected, details, ranked := rankAndSelect(context.Background(), edits, 2, ranker)
	if !ranked || len(selected) != 2 {
		t.Fatalf("ranker selection failed: %d %v", len(selected), ranked)
	}
	if selected[0].Content != edits[5].Content || selected[1].Content != edits[2].Content {
		t.Errorf("rank order must be preserved: %s, %s", selected[0].Content, selected[1].Content)
	}
	selectedCount := 0
	for _, d := range details {
		if d.Selected {
			selectedCount++
		}
	}
	if selectedCount != 2 {
		t.Errorf("details must mark exactly the selected edits, got %d", selectedCount)
	}
}

func TestRankAndSelectRankerErrorFallsBack(t *testing.T) {
	edits := pool(5)
	ranker := func(_ context.Context, _ []skill.TextEdit, _ int) ([]skill.TextEdit, []RankingDetail, error) {
		return nil, nil, context.DeadlineExceeded
	}
	selected, details, ranked := rankAndSelect(context.Background(), edits, 2, ranker)
	if !ranked || len(selected) != 2 || len(details) != 5 {
		t.Fatalf("ranker failure must fall back to truncation: %d %v", len(selected), ranked)
	}
}

func TestParseRankReply(t *testing.T) {
	got, ok := ParseRankReply("```json\n{\"selected\": [2, 0, 2, 9]}\n```", 3)
	if !ok || len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Errorf("wrapped parse with dedup+range filtering: %v %v", got, ok)
	}
	got, ok = ParseRankReply("[1]", 3)
	if !ok || len(got) != 1 || got[0] != 1 {
		t.Errorf("bare array parse: %v %v", got, ok)
	}
	if _, ok = ParseRankReply("no json here", 3); ok {
		t.Error("prose must not parse")
	}
	if _, ok = ParseRankReply("", 3); ok {
		t.Error("empty reply must not parse")
	}
}

func TestDefaultRanker(t *testing.T) {
	calls := 0
	call := func(_ context.Context, prompt string) (string, error) {
		calls++
		if !strings.Contains(prompt, "at most 2") || !strings.Contains(prompt, "Edit 0") {
			t.Errorf("rank prompt malformed: %s", prompt)
		}
		return "{\"selected\": [3, 1]}", nil
	}
	edits := pool(4)
	selected, details, err := DefaultRanker(call)(context.Background(), edits, 2)
	if err != nil || calls != 1 {
		t.Fatalf("ranker call: %v calls=%d", err, calls)
	}
	if len(selected) != 2 || selected[0].Content != edits[3].Content || selected[1].Content != edits[1].Content {
		t.Errorf("selection = %+v", selected)
	}
	if len(details) != 4 {
		t.Errorf("details must cover the pool, got %d", len(details))
	}
}

func TestBuildRankPrompt(t *testing.T) {
	prompt := BuildRankPrompt(pool(3), 2)
	for _, want := range []string{"at most 2", "Edit 0", "Edit 2", "rationale", "selected"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rank prompt missing %q", want)
		}
	}
}
