// clarify_ui_test.go - tests for the TUI clarify modal: message types,
// key handling (choice + free-text), modal rendering, and the
// waitForClarify timeout helper.
package tui

import (
	"amurru/hakase/internal/agent"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestClarifyUIPromptMsgSetsPendingClarify(t *testing.T) {
	m := makeModel()
	if m.pendingClarify != nil {
		t.Fatal("pendingClarify should be nil initially")
	}

	req := agent.ClarifyRequest{Question: "What next?", Choices: []string{"A", "B"}}
	resp := make(chan agent.ClarifyResponse, 1)
	msg := clarifyPromptMsg{Req: req, Resp: resp}

	_, _ = m.Update(msg)

	if m.pendingClarify == nil {
		t.Fatal("pendingClarify should be set after clarifyPromptMsg")
	}
	if m.pendingClarify.Req.Question != "What next?" {
		t.Errorf("expected question 'What next?', got %q", m.pendingClarify.Req.Question)
	}
}

// ---------------------------------------------------------------------------
// Choice mode: digit key resolves the channel with the matching choice
// ---------------------------------------------------------------------------

func TestClarifyUIChoiceDigitSelects(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Choose", Choices: []string{"Alpha", "Beta", "Gamma"}},
		Resp: resp,
	}

	// Press '2' (should select "Beta", index 1)
	_, _ = m.Update(tea.KeyPressMsg{Text: "2", Code: '2'})

	select {
	case r := <-resp:
		if len(r.Answer) != 1 || r.Answer[0] != "Beta" {
			t.Errorf("expected Answer=[Beta], got %v", r.Answer)
		}
		if r.Canceled {
			t.Error("expected Canceled=false")
		}
	default:
		t.Fatal("channel should be resolved after digit key")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after answer")
	}
}

func TestClarifyUIChoiceDigitOutOfRangeIgnored(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Choose", Choices: []string{"A", "B"}},
		Resp: resp,
	}

	// Press '5' (out of range, only 2 choices)
	_, _ = m.Update(tea.KeyPressMsg{Text: "5", Code: '5'})

	// Channel must still be un-resolved.
	select {
	case <-resp:
		t.Error("channel should NOT be resolved for out-of-range digit")
	default:
	}

	if m.pendingClarify == nil {
		t.Error("pendingClarify should still be set for out-of-range digit")
	}
}

func TestClarifyUIChoiceEscCancels(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Choose", Choices: []string{"A", "B"}},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	select {
	case r := <-resp:
		if !r.Canceled {
			t.Error("expected Canceled=true on esc")
		}
	default:
		t.Fatal("channel should be resolved after esc")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after esc")
	}
}

func TestClarifyUIChoiceCtrlCQuits(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Choose", Choices: []string{"A", "B"}},
		Resp: resp,
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	select {
	case r := <-resp:
		if !r.Canceled {
			t.Error("expected Canceled=true on ctrl+c")
		}
	default:
		t.Fatal("channel should be resolved after ctrl+c")
	}

	if cmd == nil {
		t.Error("expected Quit command on ctrl+c during modal")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after ctrl+c")
	}
}

// ---------------------------------------------------------------------------
// Multi-select: digit toggles selection; enter confirms as set
// ---------------------------------------------------------------------------

func TestClarifyUIMultiSelectDigitToggles(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req: agent.ClarifyRequest{
			Question:    "Pick some",
			Choices:     []string{"A", "B", "C", "D"},
			MultiSelect: true,
		},
		Resp: resp,
	}

	// Toggle '1' (index 0): select A
	_, _ = m.Update(tea.KeyPressMsg{Text: "1", Code: '1'})
	// Channel should still be un-resolved (multi-select waits for enter).
	select {
	case <-resp:
		t.Error("channel should NOT be resolved on digit in multi-select")
	default:
	}

	// Toggle '3' (index 2): select C
	_, _ = m.Update(tea.KeyPressMsg{Text: "3", Code: '3'})

	if len(m.clarifySelection) != 2 {
		t.Errorf("expected 2 selections, got %d: %v", len(m.clarifySelection), m.clarifySelection)
	}

	// Toggle '1' again: deselect A
	_, _ = m.Update(tea.KeyPressMsg{Text: "1", Code: '1'})
	if len(m.clarifySelection) != 1 || m.clarifySelection[0] != 2 {
		t.Errorf("expected [2] after re-toggle, got %v", m.clarifySelection)
	}

	// Confirm with enter.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case r := <-resp:
		if len(r.Answer) != 1 || r.Answer[0] != "C" {
			t.Errorf("expected Answer=[C], got %v", r.Answer)
		}
	default:
		t.Fatal("channel should be resolved after enter in multi-select")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after multi-select confirm")
	}
}

// ---------------------------------------------------------------------------
// Free-text mode: enter submits input value; chars pass through
// ---------------------------------------------------------------------------

func TestClarifyUIFreeTextEnterSubmits(t *testing.T) {
	m := makeModel()

	// Set up a text answer before triggering the clarify prompt.
	m.clarifyInput.Focus()
	m.clarifyInput.SetValue("my answer here")

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Type something"},
		Resp: resp,
	}

	// Press Enter - should capture the current clarify input value.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case r := <-resp:
		if len(r.Answer) != 1 || r.Answer[0] != "my answer here" {
			t.Errorf("expected Answer=[my answer here], got %v", r.Answer)
		}
	default:
		t.Fatal("channel should be resolved after enter in free-text mode")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after free-text enter")
	}
}

func TestClarifyUIFreeTextEmptyEnterSubmitsEmpty(t *testing.T) {
	m := makeModel()
	// Ensure the clarify input is empty and focused.
	m.clarifyInput.Focus()
	m.clarifyInput.Reset()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Type something"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case r := <-resp:
		if len(r.Answer) != 1 || r.Answer[0] != "" {
			t.Errorf("expected Answer=[''], got %v", r.Answer)
		}
	default:
		t.Fatal("channel should be resolved after enter (even empty)")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after free-text enter")
	}
}

func TestClarifyUIFreeTextCharKeysNotSwallowed(t *testing.T) {
	m := makeModel()
	m.clarifyInput.Focus()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Type something"},
		Resp: resp,
	}

	// Typing 'h' should land in the clarify input, not resolve the channel.
	_, _ = m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})

	// Channel must still be un-resolved.
	select {
	case <-resp:
		t.Error("channel should NOT be resolved for a character key in free-text mode")
	default:
	}

	if got := m.clarifyInput.Value(); got != "h" {
		t.Errorf("expected clarifyInput value %q, got %q", "h", got)
	}

	if m.pendingClarify == nil {
		t.Error("pendingClarify should still be set after typing a character")
	}
}

func TestClarifyUIFreeTextEscCancels(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Type something"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	select {
	case r := <-resp:
		if !r.Canceled {
			t.Error("expected Canceled=true on esc in free-text mode")
		}
	default:
		t.Fatal("channel should be resolved after esc")
	}

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after esc")
	}
}

func TestClarifyUIFreeTextCtrlCQuits(t *testing.T) {
	m := makeModel()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Type something"},
		Resp: resp,
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	select {
	case r := <-resp:
		if !r.Canceled {
			t.Error("expected Canceled=true on ctrl+c in free-text mode")
		}
	default:
		t.Fatal("channel should be resolved after ctrl+c")
	}

	if cmd == nil {
		t.Error("expected Quit command on ctrl+c during free-text modal")
	}
}

// ---------------------------------------------------------------------------
// View renders question text and choices
// ---------------------------------------------------------------------------

func TestClarifyUIModalRendersQuestionAndChoices(t *testing.T) {
	m := makeModel()

	m.pendingClarify = &clarifyPromptMsg{
		Req: agent.ClarifyRequest{
			Question: "What is your preference?",
			Choices:  []string{"Option A", "Option B", "Option C"},
		},
		Resp: make(chan agent.ClarifyResponse, 1),
	}

	v := m.View()
	rendered := v.Content

	wantFragments := []string{
		"What is your preference?",
		"Option A",
		"Option B",
		"Option C",
		"[1-4] select",
		"[enter] confirm",
		"[esc] cancel",
	}

	for _, frag := range wantFragments {
		if !strings.Contains(rendered, frag) {
			t.Errorf("modal view missing %q\nrendered:\n%s", frag, safeHead(rendered, 500))
		}
	}
}

func TestClarifyUIModalRendersMultiSelectHints(t *testing.T) {
	m := makeModel()

	m.pendingClarify = &clarifyPromptMsg{
		Req: agent.ClarifyRequest{
			Question:    "Pick all that apply",
			Choices:     []string{"A", "B", "C"},
			MultiSelect: true,
		},
		Resp: make(chan agent.ClarifyResponse, 1),
	}

	v := m.View()
	rendered := v.Content

	if !strings.Contains(rendered, "[1-4] toggle") {
		t.Error("modal view missing 'toggle' hint for multi-select")
	}
}

func TestClarifyUIModalRendersFreeTextHint(t *testing.T) {
	m := makeModel()

	m.pendingClarify = &clarifyPromptMsg{
		Req: agent.ClarifyRequest{
			Question: "Tell me more...",
		},
		Resp: make(chan agent.ClarifyResponse, 1),
	}

	v := m.View()
	rendered := v.Content

	for _, frag := range []string{"Type your answer", "[enter] submit", "[esc] cancel"} {
		if !strings.Contains(rendered, frag) {
			t.Errorf("modal view missing %q\nrendered:\n%s", frag, safeHead(rendered, 500))
		}
	}
	// The answer field (with its placeholder) is rendered inside the modal.
	// ansi.Strip is needed: the placeholder's first char is styled with the
	// cursor-line color, so escape codes sit between the characters.
	if !strings.Contains(ansi.Strip(rendered), clarifyInputPlaceholder) {
		t.Errorf("modal view missing answer field placeholder %q", clarifyInputPlaceholder)
	}
}

// ---------------------------------------------------------------------------
// Free-text input lifecycle
// ---------------------------------------------------------------------------

func TestClarifyUIFreeTextPromptOpensInput(t *testing.T) {
	m := makeModel()
	m.clarifyInput.SetValue("leftover")
	m.clarifyInput.Blur()

	_, _ = m.Update(clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Q"},
		Resp: make(chan agent.ClarifyResponse, 1),
	})

	if !m.clarifyInput.Focused() {
		t.Error("clarifyInput should be focused in free-text mode")
	}
	if m.clarifyInput.Value() != "" {
		t.Errorf("clarifyInput should start empty, got %q", m.clarifyInput.Value())
	}
}

func TestClarifyUIFreeTextPromptDoesNotTouchMainInput(t *testing.T) {
	m := makeModel()
	m.input.SetValue("draft message")

	_, _ = m.Update(clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Q"},
		Resp: make(chan agent.ClarifyResponse, 1),
	})

	// The user's half-composed main message must survive a clarify prompt.
	if got := m.input.Value(); got != "draft message" {
		t.Errorf("main input was clobbered: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Timeout clears the modal
// ---------------------------------------------------------------------------

func TestClarifyUITimeoutClearsModal(t *testing.T) {
	m := makeModel()
	m.clarifyInput.Focus()

	resp := make(chan agent.ClarifyResponse, 1)
	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: "Q"},
		Resp: resp,
	}

	_, _ = m.Update(clarifyTimeoutMsg{})

	if m.pendingClarify != nil {
		t.Error("pendingClarify should be nil after timeout msg")
	}
	if m.clarifyInput.Focused() {
		t.Error("clarifyInput should be blurred after timeout msg")
	}
}

// ---------------------------------------------------------------------------
// Modal wrapping: long questions never overflow the terminal width
// ---------------------------------------------------------------------------

func TestClarifyUIModalWrapsLongQuestion(t *testing.T) {
	m := makeModel() // 80x40 from makeModel

	// A question far wider than any terminal. The unique tail proves the full
	// text is present (wrapped), not clipped at the screen edge.
	marker := "UNIQUE_TAIL_MARKER_xyzzy"
	question := strings.Repeat("word ", 200) + marker

	m.pendingClarify = &clarifyPromptMsg{
		Req:  agent.ClarifyRequest{Question: question},
		Resp: make(chan agent.ClarifyResponse, 1),
	}

	v := m.View()
	rendered := v.Content

	if !strings.Contains(rendered, marker) {
		t.Errorf("long question was clipped; unique tail missing\nrendered:\n%s", safeHead(rendered, 500))
	}
	for i, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line %d exceeds screen width %d: width=%d", i, m.width, w)
		}
	}
}

func TestClarifyUIModalWrapsLongChoices(t *testing.T) {
	m := makeModel()

	m.pendingClarify = &clarifyPromptMsg{
		Req: agent.ClarifyRequest{
			Question: "Pick one",
			Choices:  []string{strings.Repeat("choice ", 100), "short"},
		},
		Resp: make(chan agent.ClarifyResponse, 1),
	}

	v := m.View()
	rendered := v.Content

	if !strings.Contains(rendered, "short") {
		t.Errorf("short choice lost after wrapping long sibling\nrendered:\n%s", safeHead(rendered, 500))
	}
	for i, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line %d exceeds screen width %d: width=%d", i, m.width, w)
		}
	}
}

// ---------------------------------------------------------------------------
// waitForClarify expiry
// ---------------------------------------------------------------------------

func TestWaitForClarifyReturnsResponse(t *testing.T) {
	resp := make(chan agent.ClarifyResponse, 1)
	go func() { resp <- agent.ClarifyResponse{Answer: []string{"yes"}} }()

	r := waitForClarify(resp, 5*time.Second)
	if len(r.Answer) != 1 || r.Answer[0] != "yes" {
		t.Errorf("expected Answer=[yes], got %v", r.Answer)
	}
}

func TestWaitForClarifyReturnsCanceled(t *testing.T) {
	resp := make(chan agent.ClarifyResponse, 1)
	go func() { resp <- agent.ClarifyResponse{Canceled: true} }()

	r := waitForClarify(resp, 5*time.Second)
	if !r.Canceled {
		t.Error("expected Canceled=true")
	}
}

func TestWaitForClarifyTimeout(t *testing.T) {
	resp := make(chan agent.ClarifyResponse, 1)
	r := waitForClarify(resp, 10*time.Millisecond)
	if !r.TimedOut {
		t.Error("expected TimedOut=true on expiry")
	}
}
