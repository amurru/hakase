package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		text string
		name string
		args string
		ok   bool
	}{
		{"/exit", "exit", "", true},
		{"/compact focus on auth", "compact", "focus on auth", true},
		{"/compact  focus on auth  ", "compact", "focus on auth", true},
		{"/", "", "", false},
		{"/ ", "", "", false},
		{"hello", "", "", false},
		{"/ help", "help", "", true}, // leading slash then space: name "help"
		{"", "", "", false},
	}
	for _, c := range cases {
		name, args, ok := parseSlashCommand(c.text)
		if name != c.name || args != c.args || ok != c.ok {
			t.Fatalf("parseSlashCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.text, name, args, ok, c.name, c.args, c.ok)
		}
	}
}

func TestFindSlashCommandByNameAndAlias(t *testing.T) {
	if cmd := findSlashCommand("compact"); cmd == nil || cmd.Name != "compact" {
		t.Fatalf("findSlashCommand(compact) = %v", cmd)
	}
	if cmd := findSlashCommand("COMPACT"); cmd == nil || cmd.Name != "compact" {
		t.Fatalf("findSlashCommand should be case-insensitive")
	}
	if cmd := findSlashCommand("quit"); cmd == nil || cmd.Name != "exit" {
		t.Fatalf("findSlashCommand(quit) should resolve to exit via alias")
	}
	if cmd := findSlashCommand("bogus"); cmd != nil {
		t.Fatalf("findSlashCommand(bogus) = %v, want nil", cmd)
	}
}

func TestRunSlashCommandExitReturnsQuit(t *testing.T) {
	m := newTestModel(t)

	// Dispatch via the enter path: /exit must produce a command and never
	// reach the runner.
	model, cmd := m.sendInput("/exit")
	mm := model
	if cmd == nil {
		t.Fatal("/exit must return a quit command")
	}
	if mm.isProcessing {
		t.Fatal("/exit must not set isProcessing")
	}
	if len(mm.chatHistory) != 0 {
		t.Fatalf("/exit must not append to chat history")
	}
}

func TestRunSlashCommandUnknownLogsAndConsumes(t *testing.T) {
	m := newTestModel(t)
	model, cmd := m.sendInput("/bogus")
	mm := model
	if cmd != nil {
		t.Fatalf("unknown command must not return a command, got %v", cmd)
	}
	if mm.isProcessing {
		t.Fatal("unknown command must not set isProcessing")
	}
	if mm.input.Value() != "" {
		t.Fatalf("unknown command input must be consumed (reset), got %q", mm.input.Value())
	}
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "unknown command") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown command must log a hint, got %v", mm.logLines)
	}
}

func TestSlashNewClearsSessionAndHistory(t *testing.T) {
	m := newModelWithSvc(t)
	m.chatHistory = []ChatMessage{{Role: "user", Content: "old"}}
	m.attachments = []attachment{{ID: 1, Label: "[image 1]"}}

	model, cmd := m.sendInput("/new")
	mm := model
	if cmd != nil {
		t.Fatalf("/new returned unexpected cmd %v", cmd)
	}
	if len(mm.chatHistory) != 0 {
		t.Fatalf("/new must clear chat history")
	}
	if len(mm.attachments) != 0 {
		t.Fatalf("/new must clear attachments")
	}
}

func TestSlashSessionsOpensChooser(t *testing.T) {
	m := newTestModel(t)
	model, _ := m.sendInput("/sessions")
	mm := model
	if !mm.showSessionList {
		t.Fatal("/sessions must open the session chooser")
	}
}

func TestSlashHelpOpensHelpOverlay(t *testing.T) {
	m := newTestModel(t)
	model, _ := m.sendInput("/help")
	mm := model
	if !mm.showHelp {
		t.Fatal("/help must open the help overlay")
	}
}

func TestSlashCommandNotSentWhenProcessing(t *testing.T) {
	m := newTestModel(t)
	m.isProcessing = true
	// Non-exit commands are blocked with a hint while processing.
	model, cmd := m.sendInput("/new")
	mm := model
	if cmd != nil {
		t.Fatalf("/new while processing must not run, got cmd %v", cmd)
	}
	if len(mm.chatHistory) != 0 {
		t.Fatal("blocked /new must not touch history")
	}
}

func TestCommandMenuFiltersAndNavigates(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/c")
	m.input.CursorEnd()

	if !m.commandMenuOpen() {
		t.Fatal("input starting with / must open the command menu")
	}
	filtered := m.filteredCommands()
	if len(filtered) != 1 || filtered[0].Name != "compact" {
		t.Fatalf("filter /c = %v, want [compact]", commandNames(filtered))
	}

	// Tab completes the highlighted command into the input.
	model, cmd := m.Update(keyMsg("tab"))
	mm := model.(*appModel)
	if cmd != nil {
		t.Fatalf("tab completion must not return a command")
	}
	if got := mm.input.Value(); got != "/compact" {
		t.Fatalf("tab completion = %q, want /compact", got)
	}
}

func TestCommandMenuEnterRunsTypedCommand(t *testing.T) {
	m := newModelWithSvc(t)
	m.chatHistory = []ChatMessage{{Role: "user", Content: "old"}}
	model, _ := m.sendInput("/new")
	mm := model
	if len(mm.chatHistory) != 0 {
		t.Fatalf("enter with /new in command menu must run /new, not send a message")
	}
}

func TestEscInCommandMenuClearsInput(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/comp")
	m.input.CursorEnd()
	model, cmd := m.Update(keyMsg("esc"))
	mm := model.(*appModel)
	if cmd != nil {
		t.Fatalf("esc in command menu must not return a command")
	}
	if mm.input.Value() != "" {
		t.Fatalf("esc in command menu must clear the input, got %q", mm.input.Value())
	}
}

// commandNames extracts command names for assertion messages.
func commandNames(cmds []SlashCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	return out
}

// sendInput types text into the input and presses Enter through the real
// Update path, returning the resulting model and command.
func (m *appModel) sendInput(text string) (*appModel, tea.Cmd) {
	m.input.SetValue(text)
	m.input.CursorEnd()
	model, cmd := m.Update(keyMsg("enter"))
	return model.(*appModel), cmd
}

// newModelWithSvc builds a TUI model backed by a real session service (needed
// for /new and other session-touching commands).
func newModelWithSvc(t *testing.T) *appModel {
	t.Helper()
	store, err := NewSessionStore(t.TempDir() + "/sessions")
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	svc, err := NewSessionService(store)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	m := newModel(context.Background(), nil, svc, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*appModel)
}
