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
	if !strings.Contains(rendered, "alpha-first") {
		t.Fatalf("modal should list servers:\n%s", rendered)
	}
	// Selection is the cursor marker alone, no highlight wash: exactly
	// one row carries "> " and nothing emits reverse video.
	if n := strings.Count(rendered, "> "); n != 1 {
		t.Fatalf("expected exactly 1 cursor row, got %d:\n%s", n, rendered)
	}
	if strings.Contains(m.View().Content, "\x1b[7") {
		t.Fatal("selected row must not use reverse-video highlight")
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

func TestMCPModalLabelsNonConnectedHonestly(t *testing.T) {
	m := openMCPModalWithServers(t, []mcp.MCPServerStatus{
		{Name: "idle-srv", Transport: "stdio", Status: "idle"},
		{Name: "off-srv", Transport: "stdio", Status: "disabled"},
		{Name: "bad-srv", Transport: "http", Status: "failed", Error: "nope"},
		{Name: "live-srv", Transport: "stdio", Status: "connected", ToolCount: 3},
	})

	rendered := ansiStrip.ReplaceAllString(m.View().Content, "")
	for _, want := range []string{"idle", "off", "failed", "3 tools"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("modal missing state label %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "0 tools") {
		t.Fatalf("no row should print a misleading zero count:\n%s", rendered)
	}
}

func TestMCPModalTickRefreshesWhileOpen(t *testing.T) {
	m := newTestModel(t)
	if cmd := m.ToggleMCPList(); cmd == nil {
		t.Fatal("opening the modal should start the refresh tick")
	}
	m.showMCPList = true
	model, cmd := m.Update(mcpTickMsg{})
	mm := model.(*AppModel)
	if !mm.showMCPList {
		t.Fatal("tick must not close the modal")
	}
	if cmd == nil {
		t.Fatal("open-modal tick should chain the next refresh")
	}

	mm.showMCPList = false
	_, cmd = mm.Update(mcpTickMsg{})
	if cmd != nil {
		t.Fatal("closed-modal tick must stop the chain")
	}
}

func TestMCPModalProbeDoneLogsResult(t *testing.T) {
	m := newTestModel(t)
	m.showMCPList = true

	model, _ := m.Update(mcpProbeDoneMsg{name: "github", err: nil})
	mm := model.(*AppModel)
	if !mm.showMCPList {
		t.Fatal("probe result must not close the modal")
	}
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "github") && strings.Contains(l, "connected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("successful probe should log the result, got %v", mm.logLines)
	}
}

func TestMCPModalReconnectDialsInBackground(t *testing.T) {
	mcpTestIsolate(t)
	setupTestMCPManager(t)

	m := newModelWithSvc(t)
	m.ToggleMCPList()
	if cmd := m.mcpReconnectSelected(); cmd == nil {
		t.Fatal("reconnect should return a background probe command")
	}
	if !m.showMCPList {
		t.Fatal("reconnect must keep the modal open")
	}
}
