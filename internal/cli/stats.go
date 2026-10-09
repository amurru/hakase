// stats.go - `hakase stats`: FinOps usage summary from the local ledger
// (FO-004). Reads only; never contacts a provider.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"amurru/hakase/internal/finops"
)

// RunStatsCLI dispatches `hakase stats` and returns the process exit code.
func RunStatsCLI(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "session":
			return runStatsSession(args[1:])
		case "budgets":
			return runStatsBudgets(args[1:])
		case "help", "-h", "--help":
			statsCLIUsage()
			return 0
		}
	}
	return runStatsSummary(args)
}

func statsCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase stats [--format table|json] [--since 24h] [--by model|session|day]")
	fmt.Fprintln(os.Stderr, "       hakase stats session <id> [--format table|json]")
	fmt.Fprintln(os.Stderr, "       hakase stats budgets [--format table|json]")
}

func statsLedgerPath() string {
	if s := finops.Active(); s != nil {
		return s.Path
	}
	return finops.DefaultPath()
}

func parseStatsFlags(args []string) (format, by string, since time.Duration, rest []string, code int) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&format, "format", "table", "output format (table or json)")
	fs.StringVar(&by, "by", "model", "breakdown (model, session, or day)")
	fs.DurationVar(&since, "since", 0, "window (e.g. 24h, 168h, 720h; default all)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", "", 0, nil, 0
		}
		return "", "", 0, nil, 2
	}
	if format != "table" && format != "json" {
		fmt.Fprintf(os.Stderr, "format must be table|json\n")
		return "", "", 0, nil, 2
	}
	return format, by, since, fs.Args(), 0
}

func runStatsSummary(args []string) int {
	format, by, since, rest, code := parseStatsFlags(args)
	if code != 0 {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n\n", rest)
		statsCLIUsage()
		return 2
	}
	filter := finops.Filter{}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	return emitStats(filter, by, format)
}

func runStatsSession(args []string) int {
	format, _, since, rest, code := parseStatsFlags(args)
	if code != 0 {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprintf(os.Stderr, "Usage: hakase stats session <id>\n")
		return 2
	}
	filter := finops.Filter{SessionID: rest[0]}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	return emitStats(filter, "day", format)
}

func emitStats(filter finops.Filter, by, format string) int {
	rows, _ := finops.Read(statsLedgerPath(), filter)
	sum := finops.Summarize(rows)
	if sum.Turns == 0 {
		if format == "json" {
			fmt.Println(`{"turns":0,"tokens":0,"cost_usd":0}`)
			return 0
		}
		fmt.Println("No usage recorded yet.")
		if finops.Active() == nil {
			fmt.Println("Enable FinOps recording with \"finops\": {\"enabled\": true} in config.json.")
		}
		return 0
	}
	if format == "json" {
		out := map[string]any{
			"turns":     sum.Turns,
			"tokens":    sum.Tokens,
			"estimated": sum.Estimated,
			"usage": map[string]any{
				"prompt_tokens":     sum.Prompt,
				"completion_tokens": sum.Candidates + sum.Thoughts,
				"total_tokens":      sum.Tokens,
			},
			"cost_details": map[string]any{"total": sum.CostUSD},
			"by_model":     statsBreakdown(sum.ByModel),
			"by_session":   statsBreakdown(sum.BySession),
			"by_day":       statsBreakdown(sum.ByDay),
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("%d turns · %s tokens · $%.4g%s\n", sum.Turns, formatCount(sum.Tokens), sum.CostUSD, estimatedSuffix(sum.Estimated))
	var groups map[string]*finops.Summary
	switch by {
	case "session":
		groups = sum.BySession
	case "day":
		groups = sum.ByDay
	default:
		groups = sum.ByModel
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		fmt.Printf("\n%-30s %6s %12s %10s\n", by, "turns", "tokens", "cost")
		for _, k := range keys {
			g := groups[k]
			name := k
			if name == "" {
				name = "(unknown)"
			}
			if len(name) > 30 {
				name = name[:27] + "..."
			}
			fmt.Printf("%-30s %6d %12s $%.4g\n", name, g.Turns, formatCount(g.Tokens), g.CostUSD)
		}
	}
	return 0
}

func runStatsBudgets(args []string) int {
	fs := flag.NewFlagSet("budgets", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var format string
	fs.StringVar(&format, "format", "table", "output format (table or json)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	st := finops.CheckBudget("")
	if !st.Enabled {
		if format == "json" {
			fmt.Println(`{"enabled":false}`)
			return 0
		}
		fmt.Println("No budgets configured.")
		fmt.Println("Set finops.budgets in config.json (daily_usd, weekly_usd, monthly_usd, per_session_usd).")
		return 0
	}
	if format == "json" {
		out := map[string]any{
			"enabled":  true,
			"enforce":  st.Enforce,
			"spend":    map[string]float64{"daily": st.Spend.Daily, "weekly": st.Spend.Weekly, "monthly": st.Spend.Monthly},
			"breached": st.Breached,
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("enforce: %s\n", st.Enforce)
	row := func(name string, have, cap float64) {
		state := "ok"
		if cap > 0 && have >= cap {
			state = "EXCEEDED"
		} else if cap <= 0 {
			fmt.Printf("  %-8s $%.4g (no cap)\n", name, have)
			return
		}
		fmt.Printf("  %-8s $%.4g / $%.4g  %s\n", name, have, cap, state)
	}
	row("daily", st.Spend.Daily, st.Caps.DailyUSD)
	row("weekly", st.Spend.Weekly, st.Caps.WeeklyUSD)
	row("monthly", st.Spend.Monthly, st.Caps.MonthlyUSD)
	return 0
}

func statsBreakdown(m map[string]*finops.Summary) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		name := k
		if name == "" {
			name = "(unknown)"
		}
		out[name] = map[string]any{"turns": v.Turns, "tokens": v.Tokens, "cost_usd": v.CostUSD}
	}
	return out
}

func estimatedSuffix(n int) string {
	if n > 0 {
		return fmt.Sprintf(" (%d tokens-only)", n)
	}
	return ""
}

func formatCount(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
