package finops

import (
	"path/filepath"
	"testing"
	"time"
)

func testSettings(dir string, caps BudgetCaps) Settings {
	return Settings{
		Enabled: true,
		Path:    filepath.Join(dir, "usage.jsonl"),
		PerTool: true,
		Table:   DefaultPriceTable(),
		Budgets: caps,
	}
}

func recordCost(t *testing.T, session, model string, usd float64, at time.Time) {
	t.Helper()
	appendEntry(current.Load().Path, LedgerEntry{
		Timestamp: at,
		SessionID: session,
		Model:     model,
		Reason:    ReasonMain,
		Usage:     UsageRecord{Prompt: 1000, Total: 1000},
		CostUSD:   usd,
	})
}

func TestBudgetDisabled(t *testing.T) {
	Configure(Settings{})
	defer Configure(Settings{})
	if st := CheckBudget("s"); st.Enabled {
		t.Fatal("unconfigured check must be disabled")
	}
	Configure(testSettings(t.TempDir(), BudgetCaps{}))
	defer Configure(Settings{})
	if st := CheckBudget("s"); st.Enabled {
		t.Fatal("no caps must stay disabled")
	}
}

func TestBudgetWarnVsBlock(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// $6 spend today against a $5 daily cap.
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 5, Enforce: "warn"}))
	defer Configure(Settings{})
	recordCost(t, "sess-a", "m", 6, now)

	st := CheckBudget("sess-a")
	if !st.Enabled || st.Block {
		t.Fatalf("warn mode must not block: %+v", st)
	}
	if len(st.Breached) != 1 || st.Breached[0] != "daily" {
		t.Fatalf("expected daily breach: %+v", st)
	}
	if st.Warn == "" {
		t.Fatal("warn mode must carry a message")
	}

	// Same spend with enforce=block stops the turn.
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 5, Enforce: "block"}))
	st = CheckBudget("sess-a")
	if !st.Block {
		t.Fatalf("block mode must stop: %+v", st)
	}
	if d := st.Denial(); d == "" {
		t.Fatal("block mode must carry a denial line")
	}

	// Under cap: clean.
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 600, Enforce: "block"}))
	st = CheckBudget("sess-a")
	if st.Block || len(st.Breached) != 0 {
		t.Fatalf("under cap must pass: %+v", st)
	}
}

func TestBudgetResetSemantics(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 5, WeeklyUSD: 10, MonthlyUSD: 20}))
	defer Configure(Settings{})

	// $100 spent 60 days ago: aged out of every window.
	recordCost(t, "sess-old", "m", 100, now.Add(-60*24*time.Hour))
	st := CheckBudget("sess-old")
	if len(st.Breached) != 0 {
		t.Fatalf("aged spend must reset: %+v", st)
	}
	// $8 spent 3 days ago: inside weekly/monthly only.
	recordCost(t, "s", "m", 8, now.Add(-72*time.Hour))
	st = CheckBudget("other")
	if len(st.Breached) != 0 {
		t.Fatalf("3-day-old $8 must breach nothing at 5/10/20: %+v", st)
	}
	if st.Spend.Daily != 0 || st.Spend.Weekly != 8 || st.Spend.Monthly != 8 {
		t.Fatalf("window accounting wrong: %+v", st.Spend)
	}
}

func TestBudgetPerSessionScope(t *testing.T) {
	dir := t.TempDir()
	Configure(testSettings(dir, BudgetCaps{PerSessionUSD: 2}))
	defer Configure(Settings{})
	now := time.Now()
	recordCost(t, "sess-a", "m", 1.5, now)
	recordCost(t, "sess-b", "m", 1.5, now)

	if st := CheckBudget("sess-a"); len(st.Breached) != 0 {
		t.Fatalf("sess-a under cap: %+v", st)
	}
	recordCost(t, "sess-a", "m", 1.0, now)
	st := CheckBudget("sess-a")
	if len(st.Breached) != 1 || st.Breached[0] != "session" {
		t.Fatalf("expected session breach: %+v", st)
	}
	if st.Spend.Session != 2.5 {
		t.Fatalf("session spend = %v, want 2.5", st.Spend.Session)
	}
	// Other sessions unaffected.
	if st := CheckBudget("sess-b"); len(st.Breached) != 0 {
		t.Fatalf("sess-b must pass: %+v", st)
	}
}

func TestBudgetCountersFile(t *testing.T) {
	dir := t.TempDir()
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 100}))
	defer Configure(Settings{})
	recordCost(t, "s", "m", 3, time.Now())

	st := CheckBudget("s")
	if !st.Enabled {
		t.Fatal("expected enabled")
	}
	c, ok := ReadCounters(current.Load().Path)
	if !ok {
		t.Fatal("counters file must exist after check")
	}
	if c.DailyUSD != 3 || c.WeeklyUSD != 3 || c.MonthlyUSD != 3 {
		t.Fatalf("bad counters: %+v", c)
	}
}

func TestCostOfRespectsActive(t *testing.T) {
	Configure(Settings{})
	if _, unknown := CostOf(UsageRecord{Prompt: 10, Total: 10}); !unknown {
		t.Fatal("disabled CostOf must be unknown")
	}
	dir := t.TempDir()
	Configure(testSettings(dir, BudgetCaps{}))
	defer Configure(Settings{})
	cost, unknown := CostOf(UsageRecord{Prompt: 1_000_000, Total: 1_000_000, Model: "gemini-2.5-flash"})
	if unknown || cost != 0.30 {
		t.Fatalf("CostOf = %v, %v; want 0.30, false", cost, unknown)
	}
}

func TestCacheWarn(t *testing.T) {
	rec := UsageRecord{Prompt: 1000, Cached: 100, Total: 1500}
	if ok, _ := CacheWarn(rec, 0); ok {
		t.Error("zero ratio must disable the guard")
	}
	if ok, _ := CacheWarn(rec, 0.5); !ok {
		t.Error("6.7% cached below 50% must warn")
	}
	if ok, _ := CacheWarn(rec, 0.05); ok {
		t.Error("6.7% cached above 5% must not warn")
	}
	if ok, _ := CacheWarn(UsageRecord{}, 0.5); ok {
		t.Error("zero total must not warn")
	}
	if ok, msg := CacheWarn(rec, 0.5); !ok || msg == "" {
		t.Errorf("warn must carry a message: %v %q", ok, msg)
	}
}

func TestBudgetPct(t *testing.T) {
	Configure(Settings{})
	if got := BudgetPct(); got != 0 {
		t.Fatalf("disabled BudgetPct = %d, want 0", got)
	}
	dir := t.TempDir()
	Configure(testSettings(dir, BudgetCaps{DailyUSD: 10}))
	defer Configure(Settings{})
	if got := BudgetPct(); got != 0 {
		t.Fatalf("no spend BudgetPct = %d, want 0", got)
	}
	recordCost(t, "s", "m", 4, time.Now())
	CheckBudget("s") // refresh counters
	if got := BudgetPct(); got != 40 {
		t.Fatalf("BudgetPct = %d, want 40", got)
	}
}
