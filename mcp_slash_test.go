// mcp_slash_test.go - tests for the /mcp slash command (mcp_slash.go) and the
// interactive server-list modal (mcp_ui.go): command discovery, arg-form
// list/enable/disable, unknown-subcommand handling, and modal open/navigate.
package main

import (
	"amurru/hakase/internal/config"
	"strings"
	"testing"
)

func TestFindSlashCommandMCP(t *testing.T) {
	cmd := findSlashCommand("mcp")
	if cmd == nil || cmd.Name != "mcp" {
		t.Fatalf("findSlashCommand(mcp) = %v", cmd)
	}
}

func TestMCPSlashNoArgsOpensModal(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/mcp")
	mm := model
	if cmd != nil {
		t.Fatalf("/mcp returned unexpected cmd %v", cmd)
	}
	if !mm.showMCPList {
		t.Fatal("/mcp with no args must open the server list modal")
	}
	view := mm.mcpListView()
	if !strings.Contains(view, "MCP Servers") {
		t.Fatalf("modal view must show the server list header, got:\n%s", view)
	}
	if !strings.Contains(view, "github") {
		t.Fatalf("modal view must list the github server, got:\n%s", view)
	}
}

func TestMCPSlashModalNavigation(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	m2, _ := m.sendInput("/mcp")
	mm := m2
	if len(mm.mcpListFiltered) == 0 {
		t.Fatal("modal must have filtered servers")
	}

	// Navigate down then disable the highlighted server through the real
	// Update path.
	mm2, _ := mm.Update(keyMsg("down"))
	mm = mm2.(*appModel)
	if mm.mcpListIndex != 1 {
		t.Fatalf("expected index 1 after down, got %d", mm.mcpListIndex)
	}
	mm3, cmd := mm.Update(keyMsg("d"))
	mm = mm3.(*appModel)
	if cmd != nil {
		t.Fatalf("disable returned unexpected cmd %v", cmd)
	}
	selected := mm.mcpListFiltered[mm.mcpListIndex]
	st, _ := currentMCPManager.ServerStatus(selected.Name)
	if !st.Disabled {
		t.Fatalf("server %q should be disabled after modal 'd', got %+v", selected.Name, st)
	}
	// The modal stays open and the list refreshes (status glyph changes).
	if !mm.showMCPList {
		t.Fatal("modal should stay open after a toggle")
	}
	if !strings.Contains(mm.mcpListView(), "○") {
		t.Fatalf("disabled server should show the disabled glyph, got:\n%s", mm.mcpListView())
	}
}

func TestMCPSlashListLogsServers(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/mcp list")
	mm := model
	if cmd != nil {
		t.Fatalf("/mcp list returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "MCP Servers") {
		t.Fatalf("/mcp list must log the header, got:\n%s", joined)
	}
	if !strings.Contains(joined, "github") {
		t.Fatalf("/mcp list must list the github server, got:\n%s", joined)
	}
}

func TestMCPSlashDisableArgForm(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/mcp disable github")
	mm := model
	if cmd != nil {
		t.Fatalf("/mcp disable returned unexpected cmd %v", cmd)
	}
	if !strings.Contains(strings.Join(mm.logLines, "\n"), "disabled") {
		t.Fatalf("/mcp disable must log the result, got: %v", mm.logLines)
	}
	st, ok := currentMCPManager.ServerStatus("github")
	if !ok || !st.Disabled {
		t.Fatalf("github should be disabled after /mcp disable, got %+v", st)
	}

	// Re-enable via the arg form.
	mm2, _ := mm.Update(keyMsg("escape")) // no-op guard; keep modal-free path clean
	_ = mm2
	model2, cmd2 := mm.sendInput("/mcp enable github")
	mm = model2
	if cmd2 != nil {
		t.Fatalf("/mcp enable returned unexpected cmd %v", cmd2)
	}
	st, _ = currentMCPManager.ServerStatus("github")
	if st.Disabled {
		t.Fatal("github should be re-enabled after /mcp enable")
	}
}

func TestMCPSlashUnknownSubcommand(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	model, _ := m.sendInput("/mcp bogus")
	mm := model
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "unknown /mcp subcommand") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("/mcp bogus must log an unknown-subcommand hint, got %v", mm.logLines)
	}
}

// setupTestMCPManager installs a manager with two servers (github stdio,
// slack disabled) as currentMCPManager.
func setupTestMCPManager(t *testing.T) {
	t.Helper()
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"github": {
			Type:    "stdio",
			Command: []string{"npx", "-y", "@github/mcp-server"},
		},
		"slack": {
			Type:     "stdio",
			Command:  []string{"npx", "-y", "@slack/mcp"},
			Disabled: true,
		},
	})
	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	currentMCPManager = m
	t.Cleanup(func() { currentMCPManager = nil })
}
