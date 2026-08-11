// tui_test_bridge.go - Test helper bridge for root tests that need to
// access TUI package internals during migration.
package main

import (
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/tui"
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyMsgForRoot creates a tea.KeyPressMsg for root tests.
func keyMsgForRoot(key string) tea.KeyPressMsg {
	k := []rune(key)
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "escape":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	if len(k) == 1 {
		return tea.KeyPressMsg{Text: key, Code: k[0]}
	}
	return tea.KeyPressMsg{Text: key}
}

// newModelWithSvcForRoot builds a TUI model backed by a real session service.
func newModelWithSvcForRoot(t *testing.T) *tui.AppModel {
	t.Helper()
	store, err := session.NewSessionStore(t.TempDir() + "/sessions")
	if err != nil {
		t.Fatalf("session.NewSessionStore: %v", err)
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		t.Fatalf("session.NewSessionService: %v", err)
	}
	m := tui.NewModel(context.Background(), nil, svc, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*tui.AppModel)
}
