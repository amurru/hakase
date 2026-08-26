package sidekick

import (
	"fmt"
	"strings"

	hakasesession "amurru/hakase/internal/session"
)

// RecentTranscript renders a tail-biased, budget-bounded transcript of a
// session so an ON-DEMAND ask ("what's your take?") has conversational
// context, mirroring what the watchdog consult already sees. Only in-context
// chat turns are included: plain text plus prior sidekick answers. Tool
// call/result transcripts are skipped - they can be enormous and the primary
// answer text is what follow-up questions usually target.
//
// maxChars <= 0 means unbounded. When the rendered transcript exceeds the
// budget, the OLDEST lines are dropped first (tail bias).
func RecentTranscript(sess *hakasesession.Session, maxChars int) string {
	if sess == nil {
		return ""
	}
	var lines []string
	for _, m := range sess.Messages {
		if !m.InContext || strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch {
		case m.Kind == hakasesession.MessageKindText && m.Role == "user":
			lines = append(lines, "user: "+m.Content)
		case m.Kind == hakasesession.MessageKindText && m.Role == "agent":
			lines = append(lines, "assistant: "+m.Content)
		case m.Kind == hakasesession.MessageKindSidekick && m.Role == Role:
			lines = append(lines, "sidekick (you): "+m.Content)
		}
	}
	if len(lines) == 0 {
		return ""
	}

	joined := strings.Join(lines, "\n")
	if maxChars > 0 && len(joined) > maxChars {
		// Drop oldest lines until the remainder fits (plus separator slack).
		for i := range lines {
			remaining := strings.Join(lines[i:], "\n")
			if len(remaining) <= maxChars {
				joined = remaining
				break
			}
			if i == len(lines)-1 {
				// Single oversized line: hard-truncate its head.
				joined = remaining[len(remaining)-maxChars:]
			}
		}
	}
	return joined
}

// BuildAskPrompt frames prior conversation around the user's question for an
// on-demand ask. An empty history returns the question unchanged, preserving
// the original cold-ask behavior.
func BuildAskPrompt(history, question string) string {
	history = strings.TrimSpace(history)
	if history == "" {
		return question
	}
	var sb strings.Builder
	sb.WriteString("[CONVERSATION SO FAR]\n")
	sb.WriteString(history)
	sb.WriteString("\n\n[YOUR TASK]\n")
	fmt.Fprintf(&sb, "The user asks: %s\n", strings.TrimSpace(question))
	sb.WriteString("Answer directly, using the conversation above when it is relevant.")
	return sb.String()
}
