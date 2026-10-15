// mcp_ui.go - the interactive /mcp server-list modal, modeled on the
// session chooser modal (centered overlay with a cursor-following window).
// The modal is keyboard-driven; while open it renders as a centered overlay
// and swallows keys in Update.
//
// The statuses and state fields live on AppModel (declared in ui.go); the
// view and key handling live here to keep mcp_* concerns in one place.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcp "amurru/hakase/internal/mcp"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// mcpModalRefreshInterval re-snapshots the server list while the modal is
// open so counts follow turns running behind it. ListServers never blocks
// on network I/O, so the tick is cheap.
const mcpModalRefreshInterval = 2 * time.Second

// mcpTickMsg re-snapshots the open modal's server list.
type mcpTickMsg struct{}

// mcpProbeDoneMsg carries the result of a background reconnect probe.
type mcpProbeDoneMsg struct {
	name string
	err  error
}

func tickMCPModal() tea.Cmd {
	return tea.Tick(mcpModalRefreshInterval, func(time.Time) tea.Msg {
		return mcpTickMsg{}
	})
}

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
	return tickMCPModal()
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
	case "pgup":
		m.mcpListIndex -= m.mcpVisibleRows()
		if m.mcpListIndex < 0 {
			m.mcpListIndex = 0
		}
		return nil
	case "pgdown":
		m.mcpListIndex += m.mcpVisibleRows()
		if m.mcpListIndex > len(m.mcpListFiltered)-1 {
			m.mcpListIndex = len(m.mcpListFiltered) - 1
		}
		if m.mcpListIndex < 0 {
			m.mcpListIndex = 0
		}
		return nil
	case "home", "g":
		m.mcpListIndex = 0
		return nil
	case "end", "G":
		m.mcpListIndex = len(m.mcpListFiltered) - 1
		if m.mcpListIndex < 0 {
			m.mcpListIndex = 0
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
	if !disable {
		// A newly enabled server is idle until something dials it:
		// probe now so the modal shows the live result.
		return m.probeServerCmd(srv.Name)
	}
	return nil
}

// mcpReconnectSelected dials the highlighted server in the background and
// reports the live result; the modal stays open throughout.
func (m *AppModel) mcpReconnectSelected() tea.Cmd {
	if mcp.MCPManager == nil || len(m.mcpListFiltered) == 0 || m.mcpListIndex >= len(m.mcpListFiltered) {
		return nil
	}
	return m.probeServerCmd(m.mcpListFiltered[m.mcpListIndex].Name)
}

// probeServerCmd runs a live dial off the UI loop; the done message
// refreshes the modal, so the row flips from idle to connected/failed
// with the real count instead of waiting for the next turn.
func (m *AppModel) probeServerCmd(name string) tea.Cmd {
	m.AppendLog(fmt.Sprintf("Reconnecting MCP server %q…", name))
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		err := mcp.MCPManager.ProbeServer(ctx, name)
		return mcpProbeDoneMsg{name: name, err: err}
	}
}

// mcpVisibleRows mirrors the session chooser: the visible window follows
// the cursor so long server lists scroll instead of clipping.
func (m *AppModel) mcpVisibleRows() int {
	rows := m.height - 14 // modal margins + title/divider + footer
	if rows < 3 {
		rows = 3
	}
	if rows > 12 {
		rows = 12
	}
	return rows
}

// mcpModalView renders the /mcp server list as a centered modal.
func (m *AppModel) mcpModalView() tea.View {
	box := chooserBoxStyle.Render(m.mcpListView())
	footer := helpFooterStyle.Render("↑↓ move · pgup/pgdn jump · e enable · d disable · r reconnect · esc close")
	content := lipgloss.JoinVertical(lipgloss.Center, box, footer)
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content))
	v.MouseMode = tea.MouseModeCellMotion
	v.AltScreen = true
	return v
}

// mcpListView renders the /mcp modal box content: a titled list with the
// selected row highlighted, windowed scrolling that follows the cursor,
// and names/errors truncated to the box width. Glyph widths are measured
// display-cell-aware so the box edges stay aligned.
func (m *AppModel) mcpListView() string {
	boxW := m.width - 4
	if boxW > 76 {
		boxW = 76
	}
	if boxW < 30 {
		boxW = 30
	}
	innerW := boxW - 6 // border + padding on both sides

	var b strings.Builder
	b.WriteString(chooserTitleStyle.Render(fmt.Sprintf("MCP Servers (%d)", len(m.mcpListFiltered))))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", innerW))
	b.WriteString("\n")

	if len(m.mcpListFiltered) == 0 {
		b.WriteString("  (no MCP servers configured)\n")
		return b.String()
	}

	rows := m.mcpVisibleRows()
	start := m.mcpListIndex - rows + 1
	if start < 0 {
		start = 0
	}
	if maxStart := len(m.mcpListFiltered) - rows; maxStart > 0 && start > maxStart {
		start = maxStart
	}
	if start > m.mcpListIndex {
		start = m.mcpListIndex
	}
	for i := start; i < len(m.mcpListFiltered) && i < start+rows; i++ {
		s := m.mcpListFiltered[i]
		// Only connected servers have a meaningful count; anything else
		// names its state instead of printing a misleading zero.
		tools := "idle"
		switch s.Status {
		case "connected":
			tools = fmt.Sprintf("%d tools", s.ToolCount)
		case "disabled":
			tools = "off"
		case "failed":
			tools = "failed"
		default:
			if s.Status != "" && s.Status != "idle" {
				tools = s.Status
			}
		}
		detail := s.Transport + "  " + tools
		if s.Status == "failed" && s.Error != "" {
			detail += "  (" + truncateRunes(s.Error, 40) + ")"
		}
		nameMax := innerW - 2 - 1 - 1 - lipgloss.Width(detail) - 2
		if nameMax < 8 {
			nameMax = 8
		}
		name := truncateRunes(s.Name, nameMax)
		if pad := nameMax - lipgloss.Width(name); pad > 0 {
			name += strings.Repeat(" ", pad)
		}
		// Cursor marker selects the row; no highlight wash so the
		// status glyph colors stay readable on the selection.
		marker := "  "
		if i == m.mcpListIndex {
			marker = "> "
		}
		row := marker + mcpStatusGlyph(s) + " " + name + "  " + detail
		if pad := innerW - lipgloss.Width(row); pad > 0 {
			row += strings.Repeat(" ", pad)
		}
		b.WriteString(row + "\n")
	}
	if len(m.mcpListFiltered) > rows {
		b.WriteString(fmt.Sprintf("… %d of %d (scroll for more)\n", rows, len(m.mcpListFiltered)))
	}
	return b.String()
}
