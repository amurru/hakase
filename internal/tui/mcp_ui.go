// mcp_ui.go - the interactive /mcp server-list modal, modeled on the
// session-list modal pattern (ui.go toggleSessionList / handleSessionListKey /
// sessionListView). The modal is keyboard-driven; while open it renders as an
// inline overlay below the main content and swallows keys in Update.
//
// The statuses and state fields live on AppModel (declared in ui.go); the
// view and key handling live here to keep mcp_* concerns in one place.
package tui

import (
	"fmt"
	"strings"

	mcp "amurru/hakase/internal/mcp"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// toggleVerb returns the verb for enable/disable log messages.
func toggleVerb(disable bool) string {
	if disable {
		return "disable"
	}
	return "enable"
}

// mcpStatusGlyph returns a colored glyph for an MCP server's status.
func mcpStatusGlyph(s mcp.MCPServerStatus) string {
	switch s.Status {
	case "connected":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("●")
	case "failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("●")
	case "disabled":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("○")
	}
}

// ToggleMCPList opens or closes the interactive MCP server list modal.
func (m *AppModel) ToggleMCPList() tea.Cmd {
	if m.showMCPList {
		m.showMCPList = false
		return nil
	}
	m.showMCPList = true
	m.showSessionList = false // modals are mutually exclusive
	m.mcpListIndex = 0
	m.RefreshMCPList()
	return nil
}

// refreshMCPList reloads server statuses from the live manager. Safe to call
// from slash handlers and modal keys; no-op when the manager is unavailable.
func (m *AppModel) RefreshMCPList() {
	if mcp.MCPManager == nil {
		m.mcpListFiltered = nil
		return
	}
	m.mcpListFiltered = mcp.MCPManager.ListServers()
	if m.mcpListIndex >= len(m.mcpListFiltered) {
		m.mcpListIndex = len(m.mcpListFiltered) - 1
		if m.mcpListIndex < 0 {
			m.mcpListIndex = 0
		}
	}
}

// handleMCPListKey processes key presses while the /mcp modal is open.
func (m *AppModel) handleMCPListKey(key string) tea.Cmd {
	switch key {
	case "esc", "q":
		m.showMCPList = false
		return nil
	case "up", "k":
		if m.mcpListIndex > 0 {
			m.mcpListIndex--
		}
		return nil
	case "down", "j":
		if m.mcpListIndex < len(m.mcpListFiltered)-1 {
			m.mcpListIndex++
		}
		return nil
	case "e":
		return m.mcpToggleSelected(false)
	case "d":
		return m.mcpToggleSelected(true)
	case "r":
		return m.mcpReconnectSelected()
	}
	return nil
}

// mcpToggleSelected enables (disable=false) or disables the highlighted server.
func (m *AppModel) mcpToggleSelected(disable bool) tea.Cmd {
	if mcp.MCPManager == nil || len(m.mcpListFiltered) == 0 || m.mcpListIndex >= len(m.mcpListFiltered) {
		return nil
	}
	srv := m.mcpListFiltered[m.mcpListIndex]
	if err := mcp.MCPManager.SetDisabled(srv.Name, disable); err != nil {
		m.AppendLog(fmt.Sprintf("⚠ failed to %s %q: %v", toggleVerb(disable), srv.Name, err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("MCP server %q %s", srv.Name, toggleVerb(disable)+"d"))
	m.RefreshMCPList()
	return nil
}

// mcpReconnectSelected forces a reconnect of the highlighted server.
func (m *AppModel) mcpReconnectSelected() tea.Cmd {
	if mcp.MCPManager == nil || len(m.mcpListFiltered) == 0 || m.mcpListIndex >= len(m.mcpListFiltered) {
		return nil
	}
	srv := m.mcpListFiltered[m.mcpListIndex]
	if err := mcp.MCPManager.Reconnect(srv.Name); err != nil {
		m.AppendLog(fmt.Sprintf("⚠ failed to reconnect %q: %v", srv.Name, err))
		return nil
	}
	m.AppendLog(fmt.Sprintf("Reconnecting MCP server %q on next tool fetch", srv.Name))
	m.RefreshMCPList()
	return nil
}

// mcpListView renders the /mcp modal as an inline overlay box, matching the
// session-list modal's fixed-width box style.
func (m *AppModel) mcpListView() string {
	if len(m.mcpListFiltered) == 0 {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2).
			Render("  (no MCP servers configured)  ")
	}

	var b strings.Builder
	b.WriteString("┌─ MCP Servers ───────────────────────────────────────┐\n")
	b.WriteString("│ Esc/q: close │ e: enable │ d: disable │ r: reconnect │\n")
	b.WriteString("├──────────────────────────────────────────────────────┤\n")

	maxLines := m.chatViewport.Height() - 6
	if maxLines < 1 {
		maxLines = 1
	}

	for i, s := range m.mcpListFiltered {
		if i >= maxLines {
			b.WriteString("│  ... (more)                                          │\n")
			break
		}
		marker := " "
		if i == m.mcpListIndex {
			marker = "❯"
		}
		tools := "0 tools"
		if s.Status == "connected" {
			tools = fmt.Sprintf("%d tools", s.ToolCount)
		}
		line := fmt.Sprintf("│ %s %s %s  %s  %s", marker, mcpStatusGlyph(s), s.Name, s.Transport, tools)
		if s.Status == "failed" && s.Error != "" {
			line += fmt.Sprintf(" (%s)", s.Error)
		}
		// Pad to fill the box width.
		for len(line) < 58 {
			line += " "
		}
		line += "│"
		b.WriteString(line + "\n")
	}

	b.WriteString("└──────────────────────────────────────────────────────┘")
	return b.String()
}
