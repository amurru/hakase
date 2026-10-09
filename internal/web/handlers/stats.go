// stats.go - GET /api/stats: FinOps usage summary (FO-004).
package handlers

import (
	"net/http"
	"time"

	"amurru/hakase/internal/finops"
)

// GetStats handles GET /api/stats: tokens + USD from the local usage ledger,
// with optional ?since=24h|7d|30d (Go duration, default all), ?session=<id>,
// ?model=<name>, and ?by=model|session|day (default all three breakdowns).
// Disabled FinOps (no ledger) returns zeros with enabled:false, not an error.
func (api *ChatAPI) GetStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := finops.Filter{
		SessionID: q.Get("session"),
		Model:     q.Get("model"),
	}
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad since (try 24h, 168h, 720h)"})
			return
		}
		filter.Since = time.Now().Add(-d)
	}
	s := finops.Active()
	enabled := s != nil
	path := finops.DefaultPath()
	if s != nil {
		path = s.Path
	}
	rows, _ := finops.Read(path, filter)
	sum := finops.Summarize(rows)
	resp := map[string]any{
		"enabled":   enabled,
		"turns":     sum.Turns,
		"tokens":    sum.Tokens,
		"cost_usd":  sum.CostUSD,
		"estimated": sum.Estimated,
		"usage": map[string]any{
			"prompt_tokens":     sum.Prompt,
			"completion_tokens": sum.Candidates + sum.Thoughts,
			"total_tokens":      sum.Tokens,
		},
		"cost_details": map[string]any{"total": sum.CostUSD},
	}
	by := q.Get("by")
	if by == "" || by == "model" {
		resp["by_model"] = summarizeMap(sum.ByModel)
	}
	if by == "" || by == "session" {
		resp["by_session"] = summarizeMap(sum.BySession)
	}
	if by == "" || by == "day" {
		resp["by_day"] = summarizeMap(sum.ByDay)
	}
	if budget := finops.CheckBudget(filter.SessionID); budget.Enabled {
		resp["budgets"] = map[string]any{
			"enforce":  budget.Enforce,
			"spend":    map[string]float64{"daily": budget.Spend.Daily, "weekly": budget.Spend.Weekly, "monthly": budget.Spend.Monthly, "session": budget.Spend.Session},
			"breached": budget.Breached,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func summarizeMap(m map[string]*finops.Summary) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = map[string]any{
			"turns": v.Turns, "tokens": v.Tokens, "cost_usd": v.CostUSD, "estimated": v.Estimated,
		}
	}
	return out
}
