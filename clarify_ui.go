package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Clarify modal styles. Uses a blue/purple palette to visually distinguish
// clarify questions from the orange/yellow approval prompts.
var (
	clarifyBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("75")).
			Padding(1, 2)

	clarifyTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("75"))

	clarifyHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Italic(true)
)

// clarifyPromptMsg carries a clarify request from a tool handler to the TUI.
// The Resp channel is written by the Update handler when the user answers.
type clarifyPromptMsg struct {
	Req  ClarifyRequest
	Resp chan ClarifyResponse
}

// waitForClarify blocks on the response channel until the user answers or the
// expiry timer fires. Returns a timed-out response on expiry. Extracted so the
// select logic is unit-testable without a tea.Program.
func waitForClarify(resp chan ClarifyResponse, expiry time.Duration) ClarifyResponse {
	select {
	case r := <-resp:
		return r
	case <-time.After(expiry):
		return ClarifyResponse{TimedOut: true}
	}
}

// clarifyModalView renders the clarify modal overlay on top of the normal
// TUI. It shows the question text, optional answer choices with multi-select
// markers, and keyboard hints.
func (m *appModel) clarifyModalView() tea.View {
	if m.pendingClarify == nil {
		return tea.NewView("")
	}
	req := m.pendingClarify.Req
	var b strings.Builder

	b.WriteString(clarifyTitleStyle.Render("❓ " + req.Question))
	b.WriteString("\n\n")

	if len(req.Choices) > 0 {
		for i, c := range req.Choices {
			marker := " "
			if containsInt(m.clarifySelection, i) {
				marker = "x"
			}
			b.WriteString(fmt.Sprintf("[%s] %d. %s\n", marker, i+1, c))
		}
		hint := "[1-4] select  ·  [enter] confirm"
		if req.MultiSelect {
			hint = "[1-4] toggle  ·  [enter] confirm"
		}
		b.WriteString("\n" + clarifyHintStyle.Render(hint+"  ·  [esc] cancel"))
	} else {
		b.WriteString(clarifyHintStyle.Render("Type your answer below  ·  [esc] cancel"))
	}

	box := clarifyBoxStyle.Render(b.String())
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box))
	v.MouseMode = tea.MouseModeCellMotion
	v.AltScreen = true
	return v
}

// containsInt reports whether the slice s contains value v.
func containsInt(s []int, v int) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// toggleInt toggles v in the slice s: removes it if present, adds it otherwise.
func toggleInt(s []int, v int) []int {
	for i, e := range s {
		if e == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return append(s, v)
}
