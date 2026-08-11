// mcp_slash_test.go - tests for the /mcp slash command (mcp_slash.go) and the
// interactive server-list modal (mcp_ui.go): command discovery, arg-form
// list/enable/disable, unknown-subcommand handling, and modal open/navigate.
package tui

import (
	"amurru/hakase/internal/config"
	mcp "amurru/hakase/internal/mcp"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func init() {
	// Initialize hook vars that root normally sets so slash commands work in tests.
	RunMCPCommand = runMCPCommandStub
	RunBoardCommand = runBoardCommandStub
}

func runMCPCommandStub(m *AppModel, args string) tea.Cmd {
	// Stub that mimics the root mcp_slash.go for testing within the tui package.
	if args == "" {
		return m.ToggleMCPList()
	}
	sub, subArgs, _ := strings.Cut(args, " ")
	switch sub {
	case "list":
		m.RefreshMCPList()
		var b strings.Builder
		b.WriteString("MCP Servers:\n")
		for _, s := range m.mcpListFiltered {
			b.WriteString(fmt.Sprintf("  %s (%s) %s\n", s.Name, s.Transport, mcpStatusGlyph(s)))
		}
		m.AppendLog(b.String())
		return nil
	case "enable", "disable":
		if subArgs == "" {
			m.AppendLog("⚠ usage: /mcp " + sub + " <name>")
			return nil
		}
		if mcp.MCPManager != nil {
			_ = mcp.MCPManager.SetDisabled(subArgs, sub == "disable")
		}
		m.AppendLog("MCP server " + subArgs + " " + sub + "d")
		return nil
	default:
		m.AppendLog("⚠ unknown /mcp subcommand: " + sub)
		return nil
	}
}

func runBoardCommandStub(m *AppModel, args string) tea.Cmd {
	return nil
}

func TestFindSlashCommandMCP(t *testing.T) {
	cmd := FindSlashCommand("mcp")
	if cmd == nil || cmd.Name != "mcp" {
		t.Fatalf("FindSlashCommand(mcp) = %v", cmd)
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
	mm = mm2.(*AppModel)
	if mm.mcpListIndex != 1 {
		t.Fatalf("expected index 1 after down, got %d", mm.mcpListIndex)
	}
	mm3, cmd := mm.Update(keyMsg("d"))
	mm = mm3.(*AppModel)
	if cmd != nil {
		t.Fatalf("disable returned unexpected cmd %v", cmd)
	}
	selected := mm.mcpListFiltered[mm.mcpListIndex]
	st, _ := mcp.MCPManager.ServerStatus(selected.Name)
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
	st, ok := mcp.MCPManager.ServerStatus("github")
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
	st, _ = mcp.MCPManager.ServerStatus("github")
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

// mcpTestConfig returns a config.Config with the given project-scope MCP servers.
func mcpTestConfig(servers map[string]*config.MCPServerConfig) *config.Config {
	return &config.Config{MCPServers: config.MCPConfig{Servers: servers}}
}

// mcpTestIsolate points the user registry at a fresh temp HAKASE_HOME and
// resets the cached registry path, mirroring the persist-test convention.
func mcpTestIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	config.MCPRegistryFile = ""
	t.Cleanup(func() { config.MCPRegistryFile = "" })
	mcp.MCPManager = nil
	t.Cleanup(func() { mcp.MCPManager = nil })
}

// setupTestMCPManager installs a manager with two servers (github stdio,
// slack disabled) as mcp.MCPManager.
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
	m, err := mcp.NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	mcp.MCPManager = m
	t.Cleanup(func() { mcp.MCPManager = nil })
}
