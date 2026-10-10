package cli

import (
	"path/filepath"
	"testing"

	"amurru/hakase/internal/finops"
)

func withStatsLedger(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	finops.Configure(finops.Settings{
		Enabled: true,
		Path:    path,
		Table:   finops.DefaultPriceTable(),
		Budgets: finops.BudgetCaps{DailyUSD: 100},
	})
	t.Cleanup(func() { finops.Configure(finops.Settings{}) })
	finops.Record("sess-a", finops.UsageRecord{Prompt: 1000, Candidates: 500, Total: 1500, Model: "gemini-2.5-flash", Reason: finops.ReasonMain}, nil)
}

func TestStatsSummaryTable(t *testing.T) {
	withStatsLedger(t)
	if code := RunStatsCLI([]string{}); code != 0 {
		t.Fatalf("stats exit = %d, want 0", code)
	}
}

func TestStatsSummaryJSON(t *testing.T) {
	withStatsLedger(t)
	if code := RunStatsCLI([]string{"--format", "json"}); code != 0 {
		t.Fatalf("stats json exit = %d, want 0", code)
	}
}

func TestStatsSessionAndBudgets(t *testing.T) {
	withStatsLedger(t)
	if code := RunStatsCLI([]string{"session", "sess-a"}); code != 0 {
		t.Fatalf("stats session exit = %d, want 0", code)
	}
	if code := RunStatsCLI([]string{"session"}); code != 2 {
		t.Fatalf("stats session without id exit = %d, want 2", code)
	}
	if code := RunStatsCLI([]string{"budgets"}); code != 0 {
		t.Fatalf("stats budgets exit = %d, want 0", code)
	}
	if code := RunStatsCLI([]string{"budgets", "--format", "json"}); code != 0 {
		t.Fatalf("stats budgets json exit = %d, want 0", code)
	}
}

func TestStatsEmptyLedger(t *testing.T) {
	finops.Configure(finops.Settings{})
	t.Cleanup(func() { finops.Configure(finops.Settings{}) })
	if code := RunStatsCLI([]string{}); code != 0 {
		t.Fatalf("empty stats exit = %d, want 0", code)
	}
}
