// budget.go - pre-turn budget check (FO-003).
//
// Spend is computed from the ledger (source of truth) over rolling windows:
// daily = last 24h, weekly = last 7d, monthly = last 30d, per-session =
// session lifetime. Rolling windows give reset semantics without cron: old
// rows age out on their own. Mid-turn overrun is reported, not killed: the
// check runs at the turn boundary only (no tool-kill mid-call).
package finops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"amurru/hakase/internal/util"
)

// BudgetCaps mirrors the configured caps. Zero disables that scope.
type BudgetCaps struct {
	DailyUSD      float64
	WeeklyUSD     float64
	MonthlyUSD    float64
	PerSessionUSD float64
	Enforce       string // "warn" (default) | "block"
	// CacheWarnRatio is the prompt-cache guard threshold (FO-005): warn
	// when cached/total falls below it. Zero disables the guard.
	CacheWarnRatio float64
}

// BudgetSpend is spend per scope in USD as recorded in the ledger.
type BudgetSpend struct {
	Daily   float64
	Weekly  float64
	Monthly float64
	Session float64
}

// BudgetStatus is one pre-turn verdict.
type BudgetStatus struct {
	Enabled bool
	Enforce string
	Spend   BudgetSpend
	Caps    BudgetCaps
	// Breached names scopes at or over cap ("daily", "weekly", "monthly", "session").
	Breached []string
	// Block stops the turn (enforce=block and any breach).
	Block bool
	// Warn is the human line for warn mode (empty when nothing breached).
	Warn string
}

// CountersFileName is the cached window totals file under the hakase home dir.
// Phase 4 surfaces read it instead of scanning the ledger.
const CountersFileName = "budget-counters.json"

// Counters caches the global window totals.
type Counters struct {
	UpdatedAt  time.Time `json:"updated_at"`
	DailyUSD   float64   `json:"daily_usd"`
	WeeklyUSD  float64   `json:"weekly_usd"`
	MonthlyUSD float64   `json:"monthly_usd"`
}

// CountersPath returns the counters file path next to the ledger.
func CountersPath(ledgerPath string) string {
	if ledgerPath == "" {
		ledgerPath = DefaultPath()
	}
	return filepath.Join(filepath.Dir(ledgerPath), CountersFileName)
}

// CheckBudget renders the pre-turn verdict for sessionID. Pure read path:
// scans the ledger, compares against caps, refreshes the counters file
// best-effort. Disabled (or no caps set) returns Enabled=false.
func CheckBudget(sessionID string) BudgetStatus {
	s := Active()
	if s == nil {
		return BudgetStatus{}
	}
	caps := s.Budgets
	if caps.DailyUSD <= 0 && caps.WeeklyUSD <= 0 && caps.MonthlyUSD <= 0 && caps.PerSessionUSD <= 0 {
		return BudgetStatus{}
	}
	enforce := caps.Enforce
	if enforce == "" {
		enforce = "warn"
	}
	rows, _ := Read(s.Path, Filter{})
	now := time.Now()
	var spend BudgetSpend
	for _, e := range rows {
		age := now.Sub(e.Timestamp)
		if age < 0 {
			age = 0
		}
		if age < 24*time.Hour {
			spend.Daily += e.CostUSD
		}
		if age < 7*24*time.Hour {
			spend.Weekly += e.CostUSD
		}
		if age < 30*24*time.Hour {
			spend.Monthly += e.CostUSD
		}
		if sessionID != "" && e.SessionID == sessionID {
			spend.Session += e.CostUSD
		}
	}
	st := BudgetStatus{Enabled: true, Enforce: enforce, Spend: spend, Caps: caps}
	if caps.DailyUSD > 0 && spend.Daily >= caps.DailyUSD {
		st.Breached = append(st.Breached, "daily")
	}
	if caps.WeeklyUSD > 0 && spend.Weekly >= caps.WeeklyUSD {
		st.Breached = append(st.Breached, "weekly")
	}
	if caps.MonthlyUSD > 0 && spend.Monthly >= caps.MonthlyUSD {
		st.Breached = append(st.Breached, "monthly")
	}
	if caps.PerSessionUSD > 0 && spend.Session >= caps.PerSessionUSD {
		st.Breached = append(st.Breached, "session")
	}
	if len(st.Breached) > 0 {
		if enforce == "block" {
			st.Block = true
		} else {
			st.Warn = formatBudgetLine(st)
		}
	}
	writeCounters(s.Path, Counters{
		UpdatedAt:  now.UTC(),
		DailyUSD:   spend.Daily,
		WeeklyUSD:  spend.Weekly,
		MonthlyUSD: spend.Monthly,
	})
	return st
}

// formatBudgetLine renders the warn-mode line naming breached scopes.
func formatBudgetLine(st BudgetStatus) string {
	return "budget exceeded (" + scopesLine(st) + ")"
}

// Denial renders the block-mode denial line naming breached scopes.
func (st BudgetStatus) Denial() string {
	return "budget block: turn stopped pre-turn (" + scopesLine(st) + ")"
}

func scopesLine(st BudgetStatus) string {
	line := ""
	for i, scope := range st.Breached {
		if i > 0 {
			line += ", "
		}
		var have, cap float64
		switch scope {
		case "daily":
			have, cap = st.Spend.Daily, st.Caps.DailyUSD
		case "weekly":
			have, cap = st.Spend.Weekly, st.Caps.WeeklyUSD
		case "monthly":
			have, cap = st.Spend.Monthly, st.Caps.MonthlyUSD
		case "session":
			have, cap = st.Spend.Session, st.Caps.PerSessionUSD
		}
		line += scope + " $" + trimUSD(have) + "/$" + trimUSD(cap)
	}
	return line + ")"
}

// trimUSD trims trailing zeros for the human line ($5 not $5.000000).
func trimUSD(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// writeCounters persists window totals next to the ledger (0600, flock).
// Best-effort: never breaks the turn.
func writeCounters(ledgerPath string, c Counters) {
	path := CountersPath(ledgerPath)
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	recordMu.Lock()
	defer recordMu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	lock, err := os.OpenFile(filepath.Join(dir, "usage.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if ferr := util.FlockExclusive(lock); ferr == nil {
			defer func() {
				_ = util.FlockUnlock(lock)
				_ = lock.Close()
			}()
		} else {
			_ = lock.Close()
		}
	}
	_ = os.WriteFile(path, append(b, '\n'), 0o600)
}

// ReadCounters loads the cached window totals. ok=false when absent.
func ReadCounters(ledgerPath string) (Counters, bool) {
	b, err := os.ReadFile(CountersPath(ledgerPath))
	if err != nil {
		return Counters{}, false
	}
	var c Counters
	if err := json.Unmarshal(b, &c); err != nil {
		return Counters{}, false
	}
	return c, true
}

// CostOf prices one record against the active settings. Returns 0, true when
// recording is disabled (no table to price against).
func CostOf(rec UsageRecord) (float64, bool) {
	s := Active()
	if s == nil {
		return 0, true
	}
	return Cost(rec, rec.Model, s.Table, s.Overrides)
}

// BudgetPct returns the highest window fill vs its cap (0-100) from the
// cached counters: the live number the SSE payload and bars show. Cheap
// (one small file read); 0 when disabled or uncapped.
func BudgetPct() int {
	s := Active()
	if s == nil {
		return 0
	}
	c, ok := ReadCounters(s.Path)
	if !ok {
		return 0
	}
	pct := 0
	at := func(have, cap float64) {
		if cap <= 0 {
			return
		}
		if p := int(have * 100 / cap); p > pct {
			pct = p
		}
	}
	at(c.DailyUSD, s.Budgets.DailyUSD)
	at(c.WeeklyUSD, s.Budgets.WeeklyUSD)
	at(c.MonthlyUSD, s.Budgets.MonthlyUSD)
	if pct > 100 {
		pct = 100
	}
	return pct
}

// CacheWarn reports whether the turn's cached/total ratio falls below the
// configured cache_warn_ratio (FO-005 prompt-cache guard). A low ratio hints
// at a cache-buster (per-request-unique bytes in the prefix, OpenClaw
// date-field class); the day-keyed buildTimeReminder pattern is the fix
// template. Ratio <= 0 disables the guard.
func CacheWarn(rec UsageRecord, ratio float64) (bool, string) {
	if ratio <= 0 {
		return false, ""
	}
	total := rec.TotalTokens()
	if total <= 0 {
		return false, ""
	}
	got := float64(rec.Cached) / float64(total)
	if got >= ratio {
		return false, ""
	}
	return true, "low prompt-cache hit: " + trimPct(got*100) + "% cached below warn ratio " + trimPct(ratio*100) + "%"
}

func trimPct(v float64) string {
	return trimUSD(v)
}
