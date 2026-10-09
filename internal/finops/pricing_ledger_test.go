package finops

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCostSeedModel(t *testing.T) {
	u := UsageRecord{Prompt: 1000, Candidates: 400, Cached: 600, Thoughts: 100, Total: 1500}
	usd, unknown := Cost(u, "gemini-2.5-flash", DefaultPriceTable(), nil)
	if unknown {
		t.Fatal("seed model should be known")
	}
	// input=(1000-600)=400 @0.30, cached=600 @0.075, output=(400+100)=500 @2.50
	want := 400.0/1e6*0.30 + 600.0/1e6*0.075 + 500.0/1e6*2.50
	if usd != want {
		t.Fatalf("cost = %v, want %v", usd, want)
	}
}

func TestCostTieredAboveThreshold(t *testing.T) {
	table := DefaultPriceTable()
	big := UsageRecord{Prompt: 150_000, Candidates: 60_000, Total: 210_000}
	small := UsageRecord{Prompt: 150_000, Candidates: 40_000, Total: 190_000}
	bigUSD, _ := Cost(big, "gemini-2.5-pro", table, nil)
	smallUSD, _ := Cost(small, "gemini-2.5-pro", table, nil)
	// Above-threshold output rate (15.00) exceeds base (10.00): per-output-token
	// cost must be higher on the big turn.
	if bigUSD/60000 <= smallUSD/40000 {
		t.Fatalf("tiered rate not applied: big=%v small=%v", bigUSD, smallUSD)
	}
	// No tiered entry for flash: same per-token cost above and below.
	fBig, _ := Cost(UsageRecord{Prompt: 200_000, Candidates: 10_000, Total: 210_000}, "gemini-2.5-flash", table, nil)
	fSmall, _ := Cost(UsageRecord{Prompt: 100_000, Candidates: 10_000, Total: 110_000}, "gemini-2.5-flash", table, nil)
	if fBig-fSmall <= 0 {
		t.Fatalf("expected cost to grow with input: big=%v small=%v", fBig, fSmall)
	}
}

func TestCostUnknownTokensOnly(t *testing.T) {
	u := UsageRecord{Prompt: 1000, Candidates: 500, Total: 1500}
	usd, unknown := Cost(u, "mystery-model-9000", DefaultPriceTable(), nil)
	if !unknown || usd != 0 {
		t.Fatalf("unknown model should be tokens-only, got %v, %v", usd, unknown)
	}
}

func TestCostOverrideWins(t *testing.T) {
	u := UsageRecord{Prompt: 1_000_000, Candidates: 0, Total: 1_000_000}
	overrides := map[string]PriceEntry{
		"gemini-2.5-flash": {InputPer1M: 1.00, OutputPer1M: 1.00, CachedPer1M: 1.00},
	}
	usd, unknown := Cost(u, "Gemini-2.5-Flash", DefaultPriceTable(), overrides)
	if unknown || usd != 1.00 {
		t.Fatalf("override should win (case-insensitive), got %v, %v", usd, unknown)
	}
}

func TestCostNegativeGuard(t *testing.T) {
	// Corrupt record where cached exceeds prompt: input floors at zero.
	u := UsageRecord{Prompt: 10, Cached: 50, Candidates: 10, Total: 70}
	usd, unknown := Cost(u, "gemini-2.5-flash", DefaultPriceTable(), nil)
	if unknown {
		t.Fatal("model known")
	}
	want := 50.0/1e6*0.075 + 10.0/1e6*2.50 // input floored, cached+output billed
	if usd != want {
		t.Fatalf("cost = %v, want %v", usd, want)
	}
}

func TestLedgerRoundTripAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	Configure(Settings{Enabled: true, Path: path, PerTool: true})
	defer Configure(Settings{})

	Record("sess-a", UsageRecord{Prompt: 100, Candidates: 50, Total: 150, Model: "gemini-2.5-flash", Reason: ReasonMain}, nil)
	Record("sess-b", UsageRecord{Prompt: 200, Candidates: 100, Total: 300, Model: "mystery-model", Reason: ReasonSummarize},
		[]ToolDelta{{Tool: "search", CallID: "c1", Usage: UsageRecord{Prompt: 50, Total: 50}}})
	Record("sess-a", UsageRecord{}, nil) // zero record skipped

	rows, err := Read(path, Filter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].CostEstimated || rows[0].CostUSD <= 0 {
		t.Errorf("known model should have cost: %+v", rows[0])
	}
	if !rows[1].CostEstimated || rows[1].CostUSD != 0 {
		t.Errorf("unknown model should be tokens-only: %+v", rows[1])
	}
	if len(rows[1].Tools) != 1 || rows[1].Tools[0].Tool != "search" {
		t.Errorf("per-tool deltas not persisted: %+v", rows[1].Tools)
	}
	if rows[0].PriceTable != PriceTableVersion {
		t.Errorf("missing price table stamp: %+v", rows[0])
	}

	onlyA, _ := Read(path, Filter{SessionID: "sess-a"})
	if len(onlyA) != 1 {
		t.Fatalf("session filter: got %d rows", len(onlyA))
	}
	onlySummarize, _ := Read(path, Filter{Reason: ReasonSummarize})
	if len(onlySummarize) != 1 {
		t.Fatalf("reason filter: got %d rows", len(onlySummarize))
	}
	since := time.Now().Add(time.Hour)
	future, _ := Read(path, Filter{Since: since})
	if len(future) != 0 {
		t.Fatalf("since filter: got %d rows", len(future))
	}
}

func TestReestimateZeroCostRow(t *testing.T) {
	entry := LedgerEntry{
		Model:         "new-model",
		Usage:         UsageRecord{Prompt: 1_000_000, Candidates: 500_000, Total: 1_500_000},
		CostUSD:       0,
		CostEstimated: true,
	}
	table := DefaultPriceTable()
	if _, unknown := Cost(entry.Usage, entry.Model, table, nil); !unknown {
		t.Fatal("setup: model should be unknown")
	}
	withPrice := map[string]PriceEntry{
		"new-model": {InputPer1M: 2.00, OutputPer1M: 8.00, CachedPer1M: 0.50},
	}
	usd, unknown := Reestimate(entry, table, withPrice)
	if unknown || usd != 1_000_000.0/1e6*2.00+500_000.0/1e6*8.00 {
		t.Fatalf("re-estimate = %v, %v", usd, unknown)
	}
}

func TestSummarize(t *testing.T) {
	entries := []LedgerEntry{
		{SessionID: "a", Model: "m1", Usage: UsageRecord{Prompt: 100, Total: 100}, CostUSD: 0.01},
		{SessionID: "a", Model: "m1", Usage: UsageRecord{Prompt: 200, Total: 200}, CostUSD: 0.02},
		{SessionID: "b", Model: "mX", Usage: UsageRecord{Prompt: 50, Total: 50}, CostUSD: 0, CostEstimated: true},
	}
	sum := Summarize(entries)
	if sum.Turns != 3 || sum.Tokens != 350 || sum.Estimated != 1 {
		t.Fatalf("bad summary: %+v", sum)
	}
	if sum.BySession["a"].Turns != 2 || sum.ByModel["mX"].Estimated != 1 {
		t.Fatalf("bad breakdown: %+v", sum)
	}
	if sum.CostUSD != 0.03 {
		t.Fatalf("cost = %v, want 0.03", sum.CostUSD)
	}
}

func TestRecordDisabledNoop(t *testing.T) {
	Configure(Settings{})
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	Configure(Settings{Enabled: false, Path: path})
	defer Configure(Settings{})
	Record("s", UsageRecord{Prompt: 10, Total: 10, Model: "m"}, nil)
	rows, _ := Read(path, Filter{})
	if len(rows) != 0 {
		t.Fatalf("disabled recording should write nothing, got %d", len(rows))
	}
}
