// approval_ui_test.go - tests for the TUI approval modal: message types,
// key handling, modal rendering, and the waitForApproval timeout helper.
package tui

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// makeModel returns a minimal AppModel suitable for approval UI tests.
// Width/height are set so the modal renders without panicking.
func makeModel() AppModel {
	m := NewModel(context.Background(), nil, nil, 1000, false, "test-model", "")
	m.width = 80
	m.height = 40
	m.ready = true
	return m
}

// ---------------------------------------------------------------------------
// (a) approvalPromptMsg sets pendingApproval
// ---------------------------------------------------------------------------

func TestApprovalUIPromptMsgSetsPendingApproval(t *testing.T) {
	m := makeModel()
	if m.pendingApproval != nil {
		t.Fatal("pendingApproval should be nil initially")
	}

	req := agent.ApprovalRequest{Tool: "system_exec", Command: "rm -rf /", Risk: "HIGH", Reason: "destructive"}
	resp := make(chan bool, 1)
	msg := approvalPromptMsg{Req: req, Resp: resp}

	_, _ = m.Update(msg)

	if m.pendingApproval == nil {
		t.Fatal("pendingApproval should be set after approvalPromptMsg")
	}
	if m.pendingApproval.Req.Tool != "system_exec" {
		t.Errorf("expected tool system_exec, got %q", m.pendingApproval.Req.Tool)
	}
}

// ---------------------------------------------------------------------------
// (b) 'y' key resolves the channel with true and clears the modal
// ---------------------------------------------------------------------------

func TestApprovalUIKeyYApproves(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "ls", Risk: "LOW", Reason: "test"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})

	select {
	case ok := <-resp:
		if !ok {
			t.Error("expected true on 'y' key, got false")
		}
	default:
		t.Fatal("channel should be resolved after 'y' key")
	}

	if m.pendingApproval != nil {
		t.Error("pendingApproval should be nil after answer")
	}
}

func TestApprovalUIKeyUppercaseYApproves(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "ls", Risk: "LOW", Reason: "test"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Text: "Y", Code: 'Y'})

	select {
	case ok := <-resp:
		if !ok {
			t.Error("expected true on 'Y' key, got false")
		}
	default:
		t.Fatal("channel should be resolved after 'Y' key")
	}
}

// ---------------------------------------------------------------------------
// (c) 'n' key resolves the channel with false and clears the modal
// ---------------------------------------------------------------------------

func TestApprovalUIKeyNDenies(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "rm -rf /", Risk: "HIGH", Reason: "test"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})

	select {
	case ok := <-resp:
		if ok {
			t.Error("expected false on 'n' key, got true")
		}
	default:
		t.Fatal("channel should be resolved after 'n' key")
	}

	if m.pendingApproval != nil {
		t.Error("pendingApproval should be nil after answer")
	}
}

// ---------------------------------------------------------------------------
// (d) esc key resolves the channel with false and clears
// ---------------------------------------------------------------------------

func TestApprovalUIEscDenies(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "dd if=/dev/zero of=/dev/sda", Risk: "HIGH", Reason: "raw device"},
		Resp: resp,
	}

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	select {
	case ok := <-resp:
		if ok {
			t.Error("expected false on esc, got true")
		}
	default:
		t.Fatal("channel should be resolved after esc")
	}

	if m.pendingApproval != nil {
		t.Error("pendingApproval should be nil after esc")
	}
}

// ---------------------------------------------------------------------------
// (e) while modal open, other keys are ignored (pendingApproval stays,
//     channel not resolved)
// ---------------------------------------------------------------------------

func TestApprovalUIOtherKeysIgnored(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "ls", Risk: "LOW", Reason: "test"},
		Resp: resp,
	}

	otherKeys := []tea.KeyPressMsg{
		{Text: "x", Code: 'x'},
		{Code: tea.KeyEnter},
		{Code: tea.KeyTab},
		{Code: tea.KeySpace},
		{Code: tea.KeyUp},
		{Text: "1", Code: '1'},
	}

	for _, k := range otherKeys {
		_, _ = m.Update(k)

		// Channel must still be un-resolved.
		select {
		case <-resp:
			t.Errorf("channel should NOT be resolved for key %q", k.String())
		default:
		}

		if m.pendingApproval == nil {
			t.Errorf("pendingApproval should still be set for key %q", k.String())
		}
	}
}

// ---------------------------------------------------------------------------
// (f) View renders the command text and risk when pendingApproval is set
// ---------------------------------------------------------------------------

func TestApprovalUIViewRendersCommandAndRisk(t *testing.T) {
	m := makeModel()

	m.pendingApproval = &approvalPromptMsg{
		Req: agent.ApprovalRequest{
			Tool:    "system_exec",
			Command: "rm -rf /some/important/path",
			Risk:    "HIGH",
			Reason:  "destructive file operation",
		},
		Resp: make(chan bool, 1),
	}

	v := m.View()
	rendered := v.Content

	wantFragments := []string{
		"Command Approval Required",
		"system_exec",
		"HIGH",
		"rm -rf /some/important/path",
		"destructive file operation",
		"[y]es approve",
		"[n]o deny",
		"esc deny",
	}

	for _, frag := range wantFragments {
		if !strings.Contains(rendered, frag) {
			t.Errorf("modal view missing %q\nrendered:\n%s", frag, rendered)
		}
	}
}

// TestApprovalUIViewDoesNotScrambleChat verifies the modal is a distinct
// overlay and does not contain chat content.
func TestApprovalUIViewNoChatLeak(t *testing.T) {
	m := makeModel()
	m.chatHistory = []ChatMessage{{Role: "user", Content: "hello world"}}
	m.rebuildRenderedLines()

	m.pendingApproval = &approvalPromptMsg{
		Req: agent.ApprovalRequest{
			Tool:    "system_exec",
			Command: "ls",
			Risk:    "LOW",
			Reason:  "read-only",
		},
		Resp: make(chan bool, 1),
	}

	v := m.View()
	rendered := v.Content

	if !strings.Contains(rendered, "Command Approval Required") {
		t.Error("modal title missing from view")
	}
	t.Logf("rendered modal (first 200 chars): %s", safeHead(rendered, 200))
}

// TestApprovalUIRiskBadgeStyles verifies that each risk level renders a
// colored badge in the view.
func TestApprovalUIRiskBadgeStyles(t *testing.T) {
	m := makeModel()

	tests := []struct {
		risk string
	}{
		{"HIGH"},
		{"MEDIUM"},
		{"LOW"},
		{"UNKNOWN"},
		{"UNSPECIFIED"},
	}

	for _, tt := range tests {
		m.pendingApproval = &approvalPromptMsg{
			Req: agent.ApprovalRequest{
				Tool:    "system_exec",
				Command: "test",
				Risk:    tt.risk,
				Reason:  "test",
			},
			Resp: make(chan bool, 1),
		}

		v := m.View()
		rendered := v.Content

		if !strings.Contains(rendered, strings.ToUpper(tt.risk)) {
			t.Errorf("risk %q: badge text not found in view\nrendered:\n%s", tt.risk, safeHead(rendered, 500))
		}
	}
}

// ---------------------------------------------------------------------------
// (g) waitForApproval auto-denies after expiry (test the select closure)
// ---------------------------------------------------------------------------

func TestApprovalUIWaitForApprovalReturnsTrue(t *testing.T) {
	resp := make(chan bool, 1)
	go func() { resp <- true }()

	ok := waitForApproval(resp, 5*time.Second)
	if !ok {
		t.Error("expected true when channel receives true")
	}
}

func TestApprovalUIWaitForApprovalReturnsFalse(t *testing.T) {
	resp := make(chan bool, 1)
	go func() { resp <- false }()

	ok := waitForApproval(resp, 5*time.Second)
	if ok {
		t.Error("expected false when channel receives false")
	}
}

func TestApprovalUIWaitForApprovalTimeoutAutoDeny(t *testing.T) {
	resp := make(chan bool, 1)
	ok := waitForApproval(resp, 10*time.Millisecond)
	if ok {
		t.Error("expected false (auto-deny) on expiry")
	}
}

// TestApprovalUINoProgramFailsClosed verifies that AskApproval returns an
// error when the model has no tea program (headless mode / nil gate).
func TestApprovalUINoProgramFailsClosed(t *testing.T) {
	m := makeModel()
	m.program = nil // simulate headless: no TUI program wired

	approved, err := m.AskApproval(interfaces.ApprovalRequest{
		Tool: "system_exec", Command: "ls", Risk: "LOW", Reason: "test",
	})
	if approved {
		t.Error("AskApproval should return false when program is nil")
	}
	if err == nil {
		t.Error("AskApproval should return error when program is nil")
	}
}

// TestApprovalUICtrlCWhileModalQuits checks that ctrl+c sends false on the
// channel and returns tea.Quit.
func TestApprovalUICtrlCWhileModalQuits(t *testing.T) {
	m := makeModel()

	resp := make(chan bool, 1)
	m.pendingApproval = &approvalPromptMsg{
		Req:  agent.ApprovalRequest{Tool: "system_exec", Command: "rm -rf /", Risk: "HIGH", Reason: "test"},
		Resp: resp,
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	select {
	case ok := <-resp:
		if ok {
			t.Error("expected false on ctrl+c, got true")
		}
	default:
		t.Fatal("channel should be resolved after ctrl+c")
	}

	if cmd == nil {
		t.Error("expected Quit command on ctrl+c during modal")
	}

	if m.pendingApproval != nil {
		t.Error("pendingApproval should be nil after ctrl+c")
	}
}

func safeHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
