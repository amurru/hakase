// pricing.go - static price table and cost computation (FO-002).
//
// Prices are $/1M tokens in the LiteLLM style. The table is a seed with an
// explicit version: it rots, so unknown models warn and record tokens-only,
// and every ledger entry keeps its usage so costs can be re-estimated when
// the table updates. Per-model overrides come from config
// (finops.prices.overrides) and win over the seed.
package finops

import (
	"strings"
)

// PriceTableVersion identifies the seed table below. Bumped whenever seed
// rates change; ledger entries stamp it so re-estimates are explainable.
const PriceTableVersion = "2026-10-09-seed"

// DefaultTierThreshold is the context size above which tiered rates apply
// (the common >200K GPT/Gemini tier boundary). Explicit, not buried.
const DefaultTierThreshold int64 = 200_000

// PriceEntry is $/1M for each billable bucket. Reasoning tokens bill as
// output (Langfuse parity); see Cost.
type PriceEntry struct {
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
	CachedPer1M float64 `json:"cached_per_1m"`
}

// PriceTable is the seed plus tier policy. Overrides live in config and are
// passed to Cost separately so this table stays a pure static.
type PriceTable struct {
	Version string `json:"version"`
	// Entries maps normalized model name to base rates.
	Entries map[string]PriceEntry `json:"entries"`
	// TierThreshold is the total-tokens boundary for Above rates.
	TierThreshold int64 `json:"tier_threshold"`
	// Above maps normalized model name to above-threshold rates.
	Above map[string]PriceEntry `json:"above,omitempty"`
}

// normalizeModel lowercases and trims so "Gemini-2.5-Flash " joins the table.
func normalizeModel(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// DefaultPriceTable returns the seed table. Rates are snapshots of public
// provider pricing pages at PriceTableVersion; verify against your contract
// and override via finops.prices.overrides. Unknown models are NOT guessed.
func DefaultPriceTable() PriceTable {
	return PriceTable{
		Version:       PriceTableVersion,
		TierThreshold: DefaultTierThreshold,
		Entries: map[string]PriceEntry{
			"gemini-2.5-flash": {InputPer1M: 0.30, OutputPer1M: 2.50, CachedPer1M: 0.075},
			"gemini-2.5-pro":   {InputPer1M: 1.25, OutputPer1M: 10.00, CachedPer1M: 0.3125},
		},
		Above: map[string]PriceEntry{
			"gemini-2.5-pro": {InputPer1M: 2.50, OutputPer1M: 15.00, CachedPer1M: 0.625},
		},
	}
}

// Lookup returns the entry for model: override first, then tiered when the
// total exceeds the threshold, then base. ok=false for unknown models.
func (t PriceTable) Lookup(model string, total int64, overrides map[string]PriceEntry) (PriceEntry, bool) {
	name := normalizeModel(model)
	if o, ok := overrides[name]; ok {
		return o, true
	}
	// Config overrides keyed by display name: try raw key too.
	for k, o := range overrides {
		if normalizeModel(k) == name {
			return o, true
		}
	}
	if total > t.TierThreshold {
		if a, ok := t.Above[name]; ok {
			return a, true
		}
	}
	e, ok := t.Entries[name]
	return e, ok
}

// Cost prices one usage record. input = prompt-cached (never negative) plus
// tool-use (returned to the model as input); cached bills at the cached
// rate; output = candidates + thoughts (reasoning bills as output).
// Unknown models return 0 USD and unknown=true: warn and keep tokens-only.
func Cost(u UsageRecord, model string, table PriceTable, overrides map[string]PriceEntry) (usd float64, unknown bool) {
	entry, ok := table.Lookup(model, u.TotalTokens(), overrides)
	if !ok {
		return 0, true
	}
	input := u.Prompt - u.Cached + u.ToolUse
	if input < 0 {
		input = 0
	}
	cached := u.Cached
	if cached < 0 {
		cached = 0
	}
	output := u.Candidates + u.Thoughts
	if output < 0 {
		output = 0
	}
	return float64(input)/1e6*entry.InputPer1M +
		float64(cached)/1e6*entry.CachedPer1M +
		float64(output)/1e6*entry.OutputPer1M, false
}
