package tui

import (
	mcp "amurru/hakase/internal/mcp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func openMCPModalWithServers(t *testing.T, servers []mcp.MCPServerStatus) *AppModel {
	t.Helper()
	m := newTestModel(t)
	m.mcpListFiltered = servers
	m.mcpListIndex = 0
	m.showMCPList = true
	return m
}

func mcpTestServers(n int) []mcp.MCPServerStatus {
	servers := make([]mcp.MCPServerStatus, 0, n)
	for i := 0; i < n; i++ {
		servers = append(servers, mcp.MCPServerStatus{
			Name:      "server",
			Transport: "stdio",
			Status:    "connected",
			ToolCount: i,
		})
	}
	servers[0].Name = "alpha-first"
	servers[n-1].Name = "omega-last"
	return servers
}

func TestMCPModalHighlightsSelection(t *testing.T) {
	m := openMCPModalWithServers(t, mcpTestServers(3))

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(rendered, "MCP Servers (3)") {
		t.Fatalf("modal missing server count:\n%s", rendered)
	}
	if n := strings.Count(rendered, "> "); n != 1 {
		t.Fatalf("expected exactly 1 cursor row, got %d:\n%s", n, rendered)
	}
	if !strings.Contains(rendered, "alpha-first") {
		t.Fatalf("modal should list servers:\n%s", rendered)
	}
}

func TestMCPModalWindowsLongList(t *testing.T) {
	m := openMCPModalWithServers(t, mcpTestServers(20))

	model, _ := m.Update(keyMsg("end"))
	mm := model.(*AppModel)
	rendered := ansiStrip.ReplaceAllString(mm.View().Content, "")
	if !strings.Contains(rendered, "omega-last") {
		t.Fatalf("window should follow the cursor to the last row:\n%s", rendered)
	}
	if strings.Contains(rendered, "alpha-first") {
		t.Fatalf("rows outside the window must scroll away:\n%s", rendered)
	}
	if !strings.Contains(rendered, "of 20") {
		t.Fatalf("modal should note the truncated list:\n%s", rendered)
	}
}

func TestMCPModalTruncatesLongNamesAndErrors(t *testing.T) {
	m := openMCPModalWithServers(t, []mcp.MCPServerStatus{{
		Name:      strings.Repeat("n", 200),
		Transport: "stdio",
		Status:    "failed",
		Error:     strings.Repeat("e", 200),
	}})

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	for _, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("modal line exceeds terminal width (%d > %d):\n%q", w, m.width, line)
		}
	}
}

func TestMCPModalEmptyState(t *testing.T) {
	m := openMCPModalWithServers(t, nil)

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(rendered, "(no MCP servers configured)") {
		t.Fatalf("empty modal should explain itself:\n%s", rendered)
	}
}

func TestMCPModalFailedShowsError(t *testing.T) {
	m := openMCPModalWithServers(t, []mcp.MCPServerStatus{{
		Name:      "flaky",
		Transport: "http",
		Status:    "failed",
		Error:     "connection refused",
	}})

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(rendered, "connection refused") {
		t.Fatalf("failed servers should show the error:\n%s", rendered)
	}
}
