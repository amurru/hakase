// mcp_slash.go - the /mcp slash command: a TUI-facing manager for MCP servers.
// With no arguments it opens the interactive server list modal (mcp_ui.go);
// with a subcommand it runs the argument form and logs results to the log
// pane via m.appendLog (never stdout/stderr). Mutations are safe while the
// agent is processing: they persist to the user registry and apply on the
// next run (ADK snapshots tools once per run).
package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// runMCPCommand dispatches a /mcp [subcommand] [args] line.
func runMCPCommand(m *appModel, args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return m.toggleMCPList()
	}
	sub := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
	switch sub {
	case "list", "status", "ls":
		return mcpListCmd(m)
	case "enable", "on":
		return mcpSetEnabledCmd(m, rest, false)
	case "disable", "off":
		return mcpSetEnabledCmd(m, rest, true)
	case "reconnect":
		return mcpReconnectCmd(m, rest)
	case "help":
		m.appendLog("/mcp [list] | /mcp enable <name> | /mcp disable <name> | /mcp reconnect <name>")
		return nil
	default:
		m.appendLog(fmt.Sprintf("unknown /mcp subcommand %q (try: list, enable, disable, reconnect)", sub))
		return nil
	}
}

// mcpManager returns the live manager, logging a hint when it is unavailable.
func mcpManager(m *appModel) *MCPServerManager {
	if currentMCPManager == nil {
		m.appendLog("⚠ MCP manager is not available (no usable MCP config)")
		return nil
	}
	return currentMCPManager
}

// mcpListCmd mirrors the interactive modal as plain log lines.
func mcpListCmd(m *appModel) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	servers := mg.ListServers()
	if len(servers) == 0 {
		m.appendLog("No MCP servers configured. Add an \"mcp\" block to config.json or ~/.hakase/mcp.json.")
		return nil
	}
	m.appendLog("🔌 MCP Servers")
	for _, s := range servers {
		tools := "-"
		if s.Status == "connected" {
			tools = fmt.Sprintf("%d tools", s.ToolCount)
		}
		line := fmt.Sprintf("  %s %s  %s  %s", mcpStatusGlyph(s), s.Name, s.Transport, tools)
		if s.Status == "failed" && s.Error != "" {
			line += fmt.Sprintf("  (%s)", s.Error)
		}
		m.appendLog(line)
	}
	return nil
}

// mcpSetEnabledCmd enables (disabled=false) or disables a server by name.
func mcpSetEnabledCmd(m *appModel, args string, disable bool) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		verb := "enable"
		if disable {
			verb = "disable"
		}
		m.appendLog(fmt.Sprintf("Usage: /mcp %s <name>", verb))
		return nil
	}
	name := fields[0]
	if err := mg.SetDisabled(name, disable); err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to %s %q: %v", toggleVerb(disable), name, err))
		return nil
	}
	m.appendLog(fmt.Sprintf("MCP server %q %s", name, toggleVerb(disable)+"d"))
	m.refreshMCPList()
	return nil
}

// mcpReconnectCmd forces a fresh connect on the next tool fetch.
func mcpReconnectCmd(m *appModel, args string) tea.Cmd {
	mg := mcpManager(m)
	if mg == nil {
		return nil
	}
	fields := strings.Fields(args)
	if len(fields) != 1 {
		m.appendLog("Usage: /mcp reconnect <name>")
		return nil
	}
	name := fields[0]
	if err := mg.Reconnect(name); err != nil {
		m.appendLog(fmt.Sprintf("⚠ failed to reconnect %q: %v", name, err))
		return nil
	}
	m.appendLog(fmt.Sprintf("Reconnecting MCP server %q on next tool fetch", name))
	m.refreshMCPList()
	return nil
}

// mcpStatusGlyph returns the status glyph for a server status: ● connected,
// ○ disabled, ✗ failed, ◌ idle (not yet connected this run).
func mcpStatusGlyph(s MCPServerStatus) string {
	switch s.Status {
	case "connected":
		return "●"
	case "disabled":
		return "○"
	case "failed":
		return "✗"
	default:
		return "◌"
	}
}

func toggleVerb(disable bool) string {
	if disable {
		return "disable"
	}
	return "enable"
}
