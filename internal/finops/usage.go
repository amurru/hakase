// Package finops captures model usage for cost metering (FO-001).
//
// Phase 1 is capture only: UsageRecord preserves every counter the provider
// reports (prompt, candidates, cached, thoughts, tool-use) plus the model,
// provider, and reason labels that pricing and budgets need later. It does
// not price, persist a ledger, or enforce budgets; those land in Phase 2-3.
package finops

import (
	"google.golang.org/genai"
)

// Reason labels for single-prompt model calls that bypass the main turn loop
// (spec FO-001: summarize/HyDE/sleep/sidekick are otherwise invisible).
const (
	ReasonMain      = "main"
	ReasonSingle    = "single"
	ReasonSummarize = "summarize"
	ReasonHyde      = "hyde"
	ReasonSleep     = "sleep"
	ReasonSidekick  = "sidekick"
	ReasonEvolve    = "evolve"
)

// UsageRecord is the full per-turn usage capture. Counts are int64 so
// provider int32 values never overflow in deltas and sums.
type UsageRecord struct {
	Prompt     int64  `json:"prompt"`
	Candidates int64  `json:"candidates"`
	Cached     int64  `json:"cached"`
	Thoughts   int64  `json:"thoughts"`
	ToolUse    int64  `json:"tool_use"`
	Total      int64  `json:"total"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// FromGenai builds a UsageRecord from provider metadata. Nil-safe: a nil
// input yields a zero record so callers never need a nil check before
// TotalTokens. Model/provider/reason are filled by the caller when known.
func FromGenai(m *genai.GenerateContentResponseUsageMetadata) UsageRecord {
	if m == nil {
		return UsageRecord{}
	}
	return UsageRecord{
		Prompt:     int64(m.PromptTokenCount),
		Candidates: int64(m.CandidatesTokenCount),
		Cached:     int64(m.CachedContentTokenCount),
		Thoughts:   int64(m.ThoughtsTokenCount),
		ToolUse:    int64(m.ToolUsePromptTokenCount),
		Total:      int64(m.TotalTokenCount),
	}
}

// TotalTokens returns the turn total, falling back to prompt+candidates when
// the provider omits the total (the legacy truncation path). Cached,
// thoughts, and tool-use tokens are already included in the prompt/total
// accounting per the genai docs, so they are never added on top here.
func (u UsageRecord) TotalTokens() int64 {
	if u.Total > 0 {
		return u.Total
	}
	return u.Prompt + u.Candidates
}

// IsZero reports whether the record carries no counts at all.
func (u UsageRecord) IsZero() bool {
	return u.Prompt == 0 && u.Candidates == 0 && u.Cached == 0 &&
		u.Thoughts == 0 && u.ToolUse == 0 && u.Total == 0
}

// Sub returns the per-field delta u-prev floored at zero, for per-tool
// attribution: snapshot at tool start, diff at tool end. Model/provider/
// reason carry over from the newer record.
func (u UsageRecord) Sub(prev UsageRecord) UsageRecord {
	floor := func(a, b int64) int64 {
		if d := a - b; d > 0 {
			return d
		}
		return 0
	}
	return UsageRecord{
		Prompt:     floor(u.Prompt, prev.Prompt),
		Candidates: floor(u.Candidates, prev.Candidates),
		Cached:     floor(u.Cached, prev.Cached),
		Thoughts:   floor(u.Thoughts, prev.Thoughts),
		ToolUse:    floor(u.ToolUse, prev.ToolUse),
		Total:      floor(u.Total, prev.Total),
		Model:      u.Model,
		Provider:   u.Provider,
		Reason:     u.Reason,
	}
}

// Add sums per-field counts; labels carry over from the receiver.
func (u UsageRecord) Add(other UsageRecord) UsageRecord {
	return UsageRecord{
		Prompt:     u.Prompt + other.Prompt,
		Candidates: u.Candidates + other.Candidates,
		Cached:     u.Cached + other.Cached,
		Thoughts:   u.Thoughts + other.Thoughts,
		ToolUse:    u.ToolUse + other.ToolUse,
		Total:      u.Total + other.Total,
		Model:      u.Model,
		Provider:   u.Provider,
		Reason:     u.Reason,
	}
}

// ToolDelta attributes one tool call's usage: the usage growth between its
// start snapshot and its end observation.
type ToolDelta struct {
	Tool       string      `json:"tool"`
	CallID     string      `json:"call_id,omitempty"`
	Usage      UsageRecord `json:"usage"`
	DurationMs int64       `json:"duration_ms,omitempty"`
}
