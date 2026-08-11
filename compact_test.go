package main

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/tui"
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// modelWithSession builds a TUI model backed by a real session service and a
// HistoryBuilder wired as the current builder, so compactSession exercises
// the production compaction path.
func modelWithSession(t *testing.T) (*tui.AppModel, *hctx.HistoryBuilder, *session.SessionService) {
	t.Helper()
	b, svc := newTestBuilderForCompact(t)
	old := currentHistoryBuilder
	currentHistoryBuilder = b
	t.Cleanup(func() { currentHistoryBuilder = old })
	// Also set on the tui package so CompactSession can find it.
	tui.CurrentHistoryBuilder = b

	m := tui.NewModel(context.Background(), nil, svc, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	mm := model.(*tui.AppModel)
	return mm, b, svc
}

// newTestBuilderForCompact creates a HistoryBuilder backed by a temp session store.
func newTestBuilderForCompact(t *testing.T) (*hctx.HistoryBuilder, *session.SessionService) {
	t.Helper()
	store, err := session.NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("session.NewSessionStore: %v", err)
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		t.Fatalf("session.NewSessionService: %v", err)
	}
	b := hctx.NewHistoryBuilder(svc)
	b.SetModelInfo(&interfaces.ModelInfo{ContextWindow: 100_000, MaxInputTokens: 90_000})
	return b, svc
}

func TestCompactSessionSnipsToLastTwoTurns(t *testing.T) {
	mm, _, svc := modelWithSession(t)
	for _, r := range []struct {
		role, content string
	}{
		{"user", "q1"}, {"agent", "a1"},
		{"user", "q2"}, {"agent", "a2"},
		{"user", "q3"}, {"agent", "a3"},
	} {
		if err := svc.RecordUsage(r.role, r.content, "", 5); err != nil {
			t.Fatal(err)
		}
	}

	cmd := mm.CompactSession("")
	if cmd != nil {
		t.Fatalf("CompactSession returned unexpected cmd %v", cmd)
	}

	session, err := svc.GetActiveSession()
	if err != nil || session == nil {
		t.Fatalf("session: %v %v", session, err)
	}
	// Only the last 2 user turns (q2..a3) survive in context.
	inContext := 0
	for i, msg := range session.Messages {
		if msg.InContext {
			inContext++
			if i < 2 {
				t.Fatalf("message %d (%s) must be evicted", i, msg.Content)
			}
		}
	}
	if inContext != 4 {
		t.Fatalf("in-context messages = %d, want 4 (q2,a2,q3,a3)", inContext)
	}

	// A confirmation line must have been logged.
	logLines := mm.LogLines()
	found := false
	for _, l := range logLines {
		if strings.Contains(l, "compacted:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("CompactSession must log a confirmation, got %v", logLines)
	}
}

func TestCompactSessionNothingToCompact(t *testing.T) {
	mm, _, _ := modelWithSession(t)
	cmd := mm.CompactSession("")
	if cmd != nil {
		t.Fatalf("unexpected cmd %v", cmd)
	}
	logLines := mm.LogLines()
	found := false
	for _, l := range logLines {
		if strings.Contains(l, "nothing to compact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty session must log 'nothing to compact', got %v", logLines)
	}
}

func TestCompactSessionUnavailableWithoutBuilder(t *testing.T) {
	// Use tui.NewModel directly - sessionService and historyBuilder are nil.
	m := newTestModelForRoot(t)
	cmd := m.CompactSession("")
	if cmd != nil {
		t.Fatalf("unexpected cmd %v", cmd)
	}
	logLines := m.LogLines()
	found := false
	for _, l := range logLines {
		if strings.Contains(l, "compaction unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing builder must log 'compaction unavailable', got %v", logLines)
	}
}

// newTestModelForRoot creates a minimal TUI model for root tests.
func newTestModelForRoot(t *testing.T) *tui.AppModel {
	t.Helper()
	m := tui.NewModel(context.Background(), nil, nil, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*tui.AppModel)
}
