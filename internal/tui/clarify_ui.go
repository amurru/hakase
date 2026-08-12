package tui

import (
	"amurru/hakase/internal/agent"
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

	// clarifyInputStyle draws a border around the free-text answer field so
	// the user can see and edit what they type.
	clarifyInputStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("75"))
)

// clarifyMaxWidth caps the modal box so a long question can never make it
// wider than the terminal. clarifyInputPlaceholder is shown in the free-text
// answer field while it is empty.
const (
	clarifyMaxWidth         = 72
	clarifyInputPlaceholder = "Type your answer..."
)

// clarifyPromptMsg carries a clarify request from a tool handler to the TUI.
// The Resp channel is written by the Update handler when the user answers.
type clarifyPromptMsg struct {
	Req  agent.ClarifyRequest
	Resp chan agent.ClarifyResponse
}

// clarifyTimeoutMsg tells the TUI that a pending clarify question expired, so
// the modal (and any free-text input state) is cleared even though the agent
// already moved on with a timed-out answer.
type clarifyTimeoutMsg struct{}

// waitForClarify blocks on the response channel until the user answers or the
// expiry timer fires. Returns a timed-out response on expiry. Extracted so the
// select logic is unit-testable without a tea.Program.
func waitForClarify(resp chan agent.ClarifyResponse, expiry time.Duration) agent.ClarifyResponse {
	select {
	case r := <-resp:
		return r
	case <-time.After(expiry):
		return agent.ClarifyResponse{TimedOut: true}
	}
}

// clarifyBoxWidth returns the box and content widths for the clarify modal at
// the current screen size. The box fills the terminal up to clarifyMaxWidth
// with a small screen margin, so it is never clipped at the right edge.
func (m *AppModel) clarifyBoxWidth() (boxW, contentW int) {
	boxW = m.width - 8
	if boxW > clarifyMaxWidth {
		boxW = clarifyMaxWidth
	}
	if boxW < 20 {
		boxW = 20
	}
	contentW = boxW - 6 // border (2) + horizontal padding (4)
	if contentW < 1 {
		contentW = 1
	}
	return boxW, contentW
}

// openClarifyInput resets and focuses the dedicated free-text answer field.
// It is a fresh, empty input so a half-composed main message is never mistaken
// for the answer. Returns the cursor blink command so the answer field cursor
// blinks immediately.
func (m *AppModel) openClarifyInput() tea.Cmd {
	_, contentW := m.clarifyBoxWidth()
	m.clarifyInput.Reset()
	m.clarifyInput.SetWidth(contentW - 2) // inside the clarifyInputStyle border
	return m.clarifyInput.Focus()
}

// closeClarifyInput blurs the dedicated answer field so its cursor stops
// blinking once the modal is gone.
func (m *AppModel) closeClarifyInput() {
	m.clarifyInput.Blur()
}

// closeClarify clears the pending clarify modal and all its transient state.
func (m *AppModel) closeClarify() {
	m.closeClarifyInput()
	m.clarifySelection = nil
	m.pendingClarify = nil
}

// clarifyModalView renders the clarify modal overlay on top of the normal
// TUI. It shows the question text, optional answer choices with multi-select
// markers, a visible free-text input field, and keyboard hints. The box width
// is capped and every text line is wrapped to it, so long questions never
// overflow the terminal.
func (m *AppModel) clarifyModalView() tea.View {
	if m.pendingClarify == nil {
		return tea.NewView("")
	}
	req := m.pendingClarify.Req
	_, contentW := m.clarifyBoxWidth()
	var b strings.Builder

	// Wrap the question and every answer line to the content width so long
	// text wraps instead of overflowing the box (and thus the terminal).
	b.WriteString(clarifyTitleStyle.Width(contentW).Render("❓ " + req.Question))
	b.WriteString("\n\n")

	if len(req.Choices) > 0 {
		for i, c := range req.Choices {
			marker := " "
			if containsInt(m.clarifySelection, i) {
				marker = "x"
			}
			line := fmt.Sprintf("[%s] %d. %s", marker, i+1, c)
			b.WriteString(lipgloss.NewStyle().Width(contentW).Render(line))
			b.WriteString("\n")
		}
		hint := "[1-4] select  ·  [enter] confirm"
		if req.MultiSelect {
			hint = "[1-4] toggle  ·  [enter] confirm"
		}
		b.WriteString("\n" + clarifyHintStyle.Width(contentW).Render(hint+"  ·  [esc] cancel"))
	} else {
		// Free-text mode: render the dedicated answer field inside the box so
		// the user can see what they type. Keep the field width in sync with
		// the box (resizes are handled here, idempotently).
		m.clarifyInput.SetWidth(contentW - 2)
		b.WriteString(clarifyInputStyle.Render(m.clarifyInput.View()))
		b.WriteString("\n\n" + clarifyHintStyle.Width(contentW).Render("Type your answer  ·  [enter] submit  ·  [esc] cancel"))
	}

	// Cap the box height so a tiny terminal clips the modal instead of
	// lipgloss misbehaving with a negative MaxHeight.
	maxH := m.height - 4
	if maxH < 3 {
		maxH = 3
	}
	box := clarifyBoxStyle.MaxHeight(maxH).Render(b.String())
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
