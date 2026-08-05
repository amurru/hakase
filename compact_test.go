package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// modelWithSession builds a TUI model backed by a real session service and a
// HistoryBuilder wired as the current builder, so compactSession exercises
// the production compaction path.
func modelWithSession(t *testing.T) (*appModel, *HistoryBuilder, *SessionService) {
	t.Helper()
	b, svc := newTestBuilder(t)
	old := currentHistoryBuilder
	currentHistoryBuilder = b
	t.Cleanup(func() { currentHistoryBuilder = old })

	m := newModel(context.Background(), nil, svc, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*appModel), b, svc
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

	cmd := mm.compactSession("")
	if cmd != nil {
		t.Fatalf("compactSession returned unexpected cmd %v", cmd)
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
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "compacted:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("compactSession must log a confirmation, got %v", mm.logLines)
	}
}

func TestCompactSessionNothingToCompact(t *testing.T) {
	mm, _, _ := modelWithSession(t)
	cmd := mm.compactSession("")
	if cmd != nil {
		t.Fatalf("unexpected cmd %v", cmd)
	}
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "nothing to compact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty session must log 'nothing to compact', got %v", mm.logLines)
	}
}

func TestCompactSessionUnavailableWithoutBuilder(t *testing.T) {
	m := newTestModel(t) // sessionService and currentHistoryBuilder are nil
	cmd := m.compactSession("")
	if cmd != nil {
		t.Fatalf("unexpected cmd %v", cmd)
	}
	found := false
	for _, l := range m.logLines {
		if strings.Contains(l, "compaction unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing builder must log 'compaction unavailable', got %v", m.logLines)
	}
}
