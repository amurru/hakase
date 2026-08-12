package agent

import (
	"amurru/hakase/internal/config"
	"strings"
)

// Defaults for the anti-degeneration guardrails. Applied when the config
// leaves a knob at zero.
const (
	// defaultMaxOutputTokens caps provider maxOutputTokens when unset, so a
	// degenerate run cannot generate for minutes into a full output window.
	defaultMaxOutputTokens int32 = 8192
	// defaultRepetitionLimit aborts after this many consecutive identical
	// non-thought chunks (the "stuck in a loop" symptom).
	defaultRepetitionLimit = 8
	// defaultMaxTextWithoutTool aborts a text-only bloat (many runes, zero
	// tool calls) - the "frozen then spinning" refusal loop.
	defaultMaxTextWithoutTool = 20000
)

// LoopGuardConfig returns the effective guard settings, filling zero values
// with the defaults above.
func LoopGuardConfig(c config.LoopGuardConfig) config.LoopGuardConfig {
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = defaultMaxOutputTokens
	}
	if c.RepetitionLimit <= 0 {
		c.RepetitionLimit = defaultRepetitionLimit
	}
	if c.MaxTextWithoutTool <= 0 {
		c.MaxTextWithoutTool = defaultMaxTextWithoutTool
	}
	return c
}

// DegenerationGuard watches a streaming agent run for two failure modes and
// reports when the run should be aborted:
//
//  1. Repetition loop - the model emits the same non-thought sentence over and
//     over (RepetitionLimit consecutive identical chunks).
//  2. Text-only bloat - the model streams a large volume of non-thought text
//     with zero tool calls, i.e. it narrates intent without acting.
//
// A zero-value DegenerationGuard is effectively disabled (limits <= 0), so
// callers may construct one directly. feed is safe to call from a single
// goroutine (the run-loop goroutine).
type DegenerationGuard struct {
	// limits, all applied as-protected; <=0 disables that watchdog.
	maxOutputTokens    int32
	repetitionLimit    int
	maxTextWithoutTool int

	runs            int    // consecutive identical chunk counter
	lastText        string // last seen non-thought text (trimmed)
	textWithoutTool int    // runes streamed with zero tool calls so far
	everSawToolCall bool
}

// GuardDefaults builds a DegenerationGuard from the effective config settings
// (zero-value loops are enabled with the package defaults).
func GuardDefaults(c config.LoopGuardConfig) DegenerationGuard {
	c = LoopGuardConfig(c)
	return DegenerationGuard{
		maxOutputTokens:    c.MaxOutputTokens,
		repetitionLimit:    c.RepetitionLimit,
		maxTextWithoutTool: c.MaxTextWithoutTool,
	}
}

// feed records one streamed unit. isToolCall marks a function-call part;
// text is the non-thought text chunk (empty for tool parts). It returns a
// short reason string ("repetition_loop", "no_tool_call", "output_cap") when
// the guard wants the run aborted, or "" to keep going.
func (g *DegenerationGuard) Feed(isToolCall bool, text string) string {
	if g == nil {
		return ""
	}
	if isToolCall {
		g.everSawToolCall = true
		// A real tool call resets the text-only accumulation.
		g.textWithoutTool = 0
		g.runs = 0
		g.lastText = ""
		return ""
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	if g.repetitionLimit > 0 {
		if trimmed == g.lastText {
			g.runs++
			if g.runs >= g.repetitionLimit {
				return "repetition_loop"
			}
		} else {
			g.runs = 1
			g.lastText = trimmed
		}
	}

	// No-tool-call watchdog: only counts text emitted while no tool call has
	// happened yet in this run. Once the model acts, this no longer applies.
	if !g.everSawToolCall && g.maxTextWithoutTool > 0 {
		g.textWithoutTool += len([]rune(trimmed))
		if g.textWithoutTool >= g.maxTextWithoutTool {
			return "no_tool_call"
		}
	}
	return ""
}

// reasonLog maps a guard reason to a human-facing log line.
func GuardReasonLog(reason string) string {
	switch reason {
	case "repetition_loop":
		return "Aborted run: model repeated the same output (degeneration loop)."
	case "no_tool_call":
		return "Aborted run: model emitted large text-only output with no tool calls."
	case "output_cap":
		return "Aborted run: response reached the max output token cap."
	default:
		return "Aborted run: " + reason
	}
}
