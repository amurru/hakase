// finops.go - FinOps config block (FO-003): live cost meter, budgets, ledger.
//
// Opt-in (default off, following the hybrid_search precedent): absent block
// means no recording and no budget checks, byte-identical to before FinOps.
// Enable with "finops": {"enabled": true} or HAKASE_FINOPS_ENABLED=1.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"amurru/hakase/internal/finops"
)

// FinOps price override: $/1M per bucket for one model.
type FinOpsPriceOverride = finops.PriceEntry

// FinOpsPrices carries per-model price overrides.
type FinOpsPrices struct {
	Overrides map[string]FinOpsPriceOverride `json:"overrides,omitempty"`
}

// FinOpsBudgets caps spend. Zero = no cap for that scope. Enforce is
// "warn" (toast/log, default) or "block" (pre-turn denial, Phase 3).
type FinOpsBudgets struct {
	DailyUSD       float64 `json:"daily_usd,omitempty"`
	WeeklyUSD      float64 `json:"weekly_usd,omitempty"`
	MonthlyUSD     float64 `json:"monthly_usd,omitempty"`
	PerSessionUSD  float64 `json:"per_session_usd,omitempty"`
	Enforce        string  `json:"enforce,omitempty"`
	CacheWarnRatio float64 `json:"cache_warn_ratio,omitempty"`
}

// FinOpsLedger tunes the usage ledger.
type FinOpsLedger struct {
	Path    string `json:"path,omitempty"`
	PerTool bool   `json:"per_tool,omitempty"`
}

// FinOpsConfig is the "finops" top-level block.
type FinOpsConfig struct {
	Enabled  bool          `json:"enabled,omitempty"`
	Currency string        `json:"currency,omitempty"`
	Prices   FinOpsPrices  `json:"prices,omitempty"`
	Budgets  FinOpsBudgets `json:"budgets,omitempty"`
	Ledger   FinOpsLedger  `json:"ledger,omitempty"`
}

// ApplyDefaults fills zero values. Call after load.
func (c *FinOpsConfig) ApplyDefaults() {
	if c.Currency == "" {
		c.Currency = "USD"
	}
	if c.Budgets.Enforce == "" {
		c.Budgets.Enforce = "warn"
	}
}

// Validate checks the FinOps block for sane values.
func (c *FinOpsConfig) Validate() error {
	switch c.Budgets.Enforce {
	case "", "warn", "block":
	default:
		return fmt.Errorf("finops.budgets.enforce must be warn|block, got %q", c.Budgets.Enforce)
	}
	for _, v := range []struct {
		name string
		val  float64
	}{
		{"daily_usd", c.Budgets.DailyUSD},
		{"weekly_usd", c.Budgets.WeeklyUSD},
		{"monthly_usd", c.Budgets.MonthlyUSD},
		{"per_session_usd", c.Budgets.PerSessionUSD},
	} {
		if v.val < 0 {
			return fmt.Errorf("finops.budgets.%s must be >= 0, got %v", v.name, v.val)
		}
	}
	if r := c.Budgets.CacheWarnRatio; r < 0 || r > 1 {
		return fmt.Errorf("finops.budgets.cache_warn_ratio must be in [0,1], got %v", r)
	}
	for model, o := range c.Prices.Overrides {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("finops.prices.overrides has an empty model name")
		}
		if o.InputPer1M < 0 || o.OutputPer1M < 0 || o.CachedPer1M < 0 {
			return fmt.Errorf("finops.prices.overrides[%q] rates must be >= 0", model)
		}
	}
	return nil
}

// applyFinOpsEnv applies HAKASE_FINOPS_* / HAKASE_BUDGET_* overrides (env wins).
func (c *FinOpsConfig) applyFinOpsEnv() error {
	if v := os.Getenv("HAKASE_FINOPS_ENABLED"); v != "" {
		b, err := parseEnvBool("HAKASE_FINOPS_ENABLED", v)
		if err != nil {
			return err
		}
		c.Enabled = b
	}
	if v := os.Getenv("HAKASE_FINOPS_ENFORCE"); v != "" {
		c.Budgets.Enforce = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("HAKASE_BUDGET_DAILY_USD"); v != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("HAKASE_BUDGET_DAILY_USD: %w", err)
		}
		c.Budgets.DailyUSD = f
	}
	if v := os.Getenv("HAKASE_FINOPS_LEDGER_PATH"); v != "" {
		c.Ledger.Path = v
	}
	return nil
}

// finOpsSettings converts the block to live recording settings.
func (c *FinOpsConfig) finOpsSettings() finops.Settings {
	overrides := map[string]finops.PriceEntry{}
	for k, v := range c.Prices.Overrides {
		overrides[k] = v
	}
	return finops.Settings{
		Enabled:   c.Enabled,
		Path:      c.Ledger.Path,
		PerTool:   c.Ledger.PerTool,
		Table:     finops.DefaultPriceTable(),
		Overrides: overrides,
		Budgets: finops.BudgetCaps{
			DailyUSD:       c.Budgets.DailyUSD,
			WeeklyUSD:      c.Budgets.WeeklyUSD,
			MonthlyUSD:     c.Budgets.MonthlyUSD,
			PerSessionUSD:  c.Budgets.PerSessionUSD,
			Enforce:        c.Budgets.Enforce,
			CacheWarnRatio: c.Budgets.CacheWarnRatio,
		},
	}
}
