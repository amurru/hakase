package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func openChooserWithSessions(t *testing.T, titles ...string) (*AppModel, []string) {
	t.Helper()
	m := newModelWithSvc(t)
	var ids []string
	for _, title := range titles {
		s, err := m.sessionService.CreateSession(title)
		if err != nil {
			t.Fatalf("CreateSession(%q): %v", title, err)
		}
		ids = append(ids, s.ID)
	}
	m.toggleSessionList()
	if !m.showSessionList {
		t.Fatal("toggleSessionList should open the chooser")
	}
	return m, ids
}

func TestSessionModalHighlightsSelection(t *testing.T) {
	m, _ := openChooserWithSessions(t, "first", "second", "third")

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(rendered, "Sessions (3)") {
		t.Fatalf("modal missing session count:\n%s", rendered)
	}
	// Newest sessions sort first; exactly one row carries the cursor.
	if n := strings.Count(rendered, "> "); n != 1 {
		t.Fatalf("expected exactly 1 cursor row, got %d:\n%s", n, rendered)
	}
	if !strings.Contains(rendered, "> third") {
		t.Fatalf("cursor should sit on the first row:\n%s", rendered)
	}
}

func TestSessionModalWindowsLongList(t *testing.T) {
	titles := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		titles = append(titles, "session")
	}
	titles[0] = "alpha-first"
	titles[19] = "omega-last"
	m, _ := openChooserWithSessions(t, titles...)

	model, _ := m.Update(keyMsg("end"))
	mm := model.(*AppModel)
	rendered := ansiStrip.ReplaceAllString(mm.View().Content, "")
	// Sessions sort newest-first, so the last index is the oldest title.
	if !strings.Contains(rendered, "alpha-first") {
		t.Fatalf("window should follow the cursor to the oldest row:\n%s", rendered)
	}
	if strings.Contains(rendered, "omega-last") {
		t.Fatalf("rows outside the window must scroll away:\n%s", rendered)
	}
	if !strings.Contains(rendered, "of 20") {
		t.Fatalf("modal should note the truncated list:\n%s", rendered)
	}
}

func TestSessionModalTruncatesLongTitles(t *testing.T) {
	m, _ := openChooserWithSessions(t, strings.Repeat("x", 200))

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	for _, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("modal line exceeds terminal width (%d > %d):\n%q", w, m.width, line)
		}
	}
	if strings.Contains(rendered, strings.Repeat("x", 200)) {
		t.Fatal("overlong titles must be truncated to the box")
	}
}

func TestSessionModalNilServiceSafe(t *testing.T) {
	m := newTestModel(t)
	m.showSessionList = true

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(rendered, "(no sessions)") {
		t.Fatalf("nil-service chooser should show the empty state:\n%s", rendered)
	}
}

func TestSessionModalEnterSwitchesSession(t *testing.T) {
	m, ids := openChooserWithSessions(t, "first", "second")

	// Newest sorts first, so move down to "first" then open it.
	model, _ := m.Update(keyMsg("down"))
	model, _ = model.Update(keyMsg("enter"))
	mm := model.(*AppModel)
	if mm.showSessionList {
		t.Fatal("enter should close the chooser")
	}
	if got := mm.sessionService.ActiveSessionID(); got != ids[0] {
		t.Fatalf("enter should switch to the selected session, active=%q want %q", got, ids[0])
	}
}

func TestSessionModalFilterNarrows(t *testing.T) {
	m, _ := openChooserWithSessions(t, "alpha", "alpine", "beta")

	model, _ := m.Update(keyMsg("l"))
	mm := model.(*AppModel)
	if len(mm.sessionListFiltered) != 2 {
		t.Fatalf("filter 'l' should leave 2 sessions, got %d", len(mm.sessionListFiltered))
	}
	rendered := ansiStrip.ReplaceAllString(mm.View().Content, "")
	if strings.Contains(rendered, "beta") {
		t.Fatalf("filtered-out sessions must not render:\n%s", rendered)
	}
}

func TestSessionModalPageKeysMove(t *testing.T) {
	titles := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		titles = append(titles, "session")
	}
	m, _ := openChooserWithSessions(t, titles...)

	model, _ := m.Update(keyMsg("pgdown"))
	mm := model.(*AppModel)
	if mm.sessionListIndex <= 0 {
		t.Fatal("pgdown should advance the selection")
	}
	model, _ = mm.Update(keyMsg("home"))
	mm = model.(*AppModel)
	if mm.sessionListIndex != 0 {
		t.Fatalf("home should return to the top, got %d", mm.sessionListIndex)
	}
}

func TestSessionModalTickPicksUpNewSessions(t *testing.T) {
	m, _ := openChooserWithSessions(t, "first")
	if len(m.sessionListFiltered) != 1 {
		t.Fatalf("expected 1 session, got %d", len(m.sessionListFiltered))
	}

	// Created behind the open modal (another process, a cron run).
	if _, err := m.sessionService.CreateSession("second"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	model, cmd := m.Update(chooserTickMsg{})
	mm := model.(*AppModel)
	if cmd == nil {
		t.Fatal("open-chooser tick should chain the next refresh")
	}
	if len(mm.sessionListFiltered) != 2 {
		t.Fatalf("tick should pick up the new session, got %d", len(mm.sessionListFiltered))
	}

	mm.showSessionList = false
	if _, cmd := mm.Update(chooserTickMsg{}); cmd != nil {
		t.Fatal("closed-chooser tick must stop the chain")
	}
}
