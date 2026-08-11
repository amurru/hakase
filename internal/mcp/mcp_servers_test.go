package mcp

import (
	"amurru/hakase/internal/config"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// mcpTestCtx is a minimal agent.ReadonlyContext fake: the manager and the
// underlying ADK mcptoolset only pass the context along (connect/list/call),
// so a bare context.Background-backed stub suffices for unit tests.
type mcpTestCtx struct {
	context.Context
}

func (mcpTestCtx) UserContent() *genai.Content          { return nil }
func (mcpTestCtx) InvocationID() string                 { return "test" }
func (mcpTestCtx) AgentName() string                    { return "test" }
func (mcpTestCtx) ReadonlyState() session.ReadonlyState { return nil }
func (mcpTestCtx) UserID() string                       { return "test" }
func (mcpTestCtx) AppName() string                      { return "hakase" }
func (mcpTestCtx) SessionID() string                    { return "test" }
func (mcpTestCtx) Branch() string                       { return "" }

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
	MCPManager = nil
	t.Cleanup(func() { MCPManager = nil })
}

func TestMCPServerManagerListServers(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"github": {
			Type:    "stdio",
			Command: []string{"npx", "-y", "@github/mcp-server"},
			Env:     map[string]string{"GITHUB_PAT": "${GITHUB_PAT}"},
		},
		"lightpanda": {
			Type: "http",
			URL:  "http://localhost:9223/mcp",
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
	MCPManager = m

	servers := m.ListServers()
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}
	// Sorted by name: github, lightpanda, slack.
	if servers[0].Name != "github" || servers[1].Name != "lightpanda" || servers[2].Name != "slack" {
		t.Fatalf("unexpected order: %+v", servers)
	}
	if servers[0].Type != "stdio" || servers[0].Transport != "npx -y @github/mcp-server" {
		t.Errorf("github transport wrong: %+v", servers[0])
	}
	if servers[1].Type != "http" || servers[1].Transport != "http://localhost:9223/mcp" {
		t.Errorf("lightpanda transport wrong: %+v", servers[1])
	}
	if servers[2].Status != "disabled" || !servers[2].Disabled {
		t.Errorf("slack should be disabled: %+v", servers[2])
	}
}

func TestMCPServerManagerToolsResilience(t *testing.T) {
	// A stdio server whose command does not exist must not fail the model
	// call: Tools returns no error and the server is marked failed.
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"broken": {
			Type:    "stdio",
			Command: []string{"/nonexistent/mcp-server-binary"},
		},
	})

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	MCPManager = m

	tools, err := m.Tools(mcpTestCtx{Context: context.Background()})
	if err != nil {
		t.Fatalf("Tools must never error on a dead server, got: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools from a dead server, got %d", len(tools))
	}
	st, ok := m.ServerStatus("broken")
	if !ok {
		t.Fatal("ServerStatus(broken) not found")
	}
	if st.Status != "failed" {
		t.Fatalf("expected status failed, got %q (err: %s)", st.Status, st.Error)
	}
	if st.Error == "" {
		t.Error("expected a recorded error message")
	}
}

func TestMCPServerManagerDisabledServerYieldsNoTools(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"off": {
			Type:     "stdio",
			Command:  []string{"/nonexistent/mcp-server-binary"},
			Disabled: true,
		},
	})

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	MCPManager = m

	// A disabled server is never spawned, so Tools is a no-op.
	tools, err := m.Tools(mcpTestCtx{Context: context.Background()})
	if err != nil {
		t.Fatalf("Tools errored: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools from a disabled server, got %d", len(tools))
	}
	if st, _ := m.ServerStatus("off"); st.Status != "disabled" {
		t.Fatalf("expected disabled status, got %q", st.Status)
	}
}

func TestMCPServerManagerSetDisabled(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"github": {
			Type:    "stdio",
			Command: []string{"npx", "-y", "@github/mcp-server"},
		},
	})

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	MCPManager = m

	if st, _ := m.ServerStatus("github"); st.Disabled {
		t.Fatal("github should start enabled")
	}

	// Disable: persisted to the user registry AND reflected in status.
	if err := m.SetDisabled("github", true); err != nil {
		t.Fatalf("SetDisabled(true): %v", err)
	}
	if st, _ := m.ServerStatus("github"); !st.Disabled || st.Status != "disabled" {
		t.Fatalf("expected disabled after toggle: %+v", st)
	}

	// The toggle must survive a fresh manager built from the same config
	// (it lives in the user registry, not the project config).
	m2, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if st, _ := m2.ServerStatus("github"); !st.Disabled {
		t.Fatal("toggle did not persist across manager instances")
	}

	// Re-enable clears the persisted toggle.
	if err := m.SetDisabled("github", false); err != nil {
		t.Fatalf("SetDisabled(false): %v", err)
	}
	if st, _ := m.ServerStatus("github"); st.Disabled {
		t.Fatal("github should be enabled after re-enable")
	}

	// The user registry file should exist with the final (empty) state.
	data, err := os.ReadFile(filepath.Join(os.Getenv("HAKASE_HOME"), "mcp.json"))
	if err != nil {
		t.Fatalf("reading user registry: %v", err)
	}
	var reg config.MCPUserRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("parsing user registry: %v", err)
	}
	if len(reg.Disabled) != 0 {
		t.Fatalf("expected no disabled entries after re-enable, got %v", reg.Disabled)
	}
}

func TestMCPServerManagerUpsertAddsNewServer(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(nil)

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	if len(m.ListServers()) != 0 {
		t.Fatal("expected no servers initially")
	}

	if err := m.UpsertServer("web", &config.MCPServerConfig{
		Type: "http",
		URL:  "http://localhost:9333/mcp",
	}); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}

	servers := m.ListServers()
	if len(servers) != 1 || servers[0].Name != "web" {
		t.Fatalf("expected one server 'web', got %+v", servers)
	}
	if servers[0].Type != "http" || servers[0].Transport != "http://localhost:9333/mcp" {
		t.Fatalf("unexpected server: %+v", servers[0])
	}

	// Must survive a fresh manager built from the same config (user registry).
	m2, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if len(m2.ListServers()) != 1 || m2.ListServers()[0].Name != "web" {
		t.Fatalf("upsert did not persist: %+v", m2.ListServers())
	}
}

func TestMCPServerManagerUpsertOverridesProjectServer(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"web": {Type: "http", URL: "http://project:9000/mcp"},
	})

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	// Replace the project definition with a user definition.
	if err := m.UpsertServer("web", &config.MCPServerConfig{
		Type: "http",
		URL:  "http://user:9001/mcp",
	}); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}

	if got, _ := m.ServerConfig("web"); got.URL != "http://user:9001/mcp" {
		t.Fatalf("expected user URL to override project, got %q", got.URL)
	}

	// After removal, the project definition must come back (no removed entry).
	if err := m.RemoveServer("web"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if got, ok := m.ServerConfig("web"); !ok || got.URL != "http://project:9000/mcp" {
		t.Fatalf("expected project server restored after removing user override, got %+v ok=%v", got, ok)
	}
}

func TestMCPServerManagerRemoveHidesProjectServer(t *testing.T) {
	mcpTestIsolate(t)
	cfg := mcpTestConfig(map[string]*config.MCPServerConfig{
		"github": {Type: "stdio", Command: []string{"npx", "@github/mcp-server"}},
		"web":    {Type: "http", URL: "http://localhost:9223/mcp"},
	})

	m, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	if len(m.ListServers()) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(m.ListServers()))
	}

	if err := m.RemoveServer("github"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}

	if len(m.ListServers()) != 1 || m.ListServers()[0].Name != "web" {
		t.Fatalf("expected only 'web' after removal, got %+v", m.ListServers())
	}

	// Removal of a project server must persist (via the removed list).
	m2, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if len(m2.ListServers()) != 1 {
		t.Fatalf("removal did not persist: %+v", m2.ListServers())
	}

	// Re-adding via UpsertServer undoes the removal.
	if err := m2.UpsertServer("github", &config.MCPServerConfig{Type: "stdio", Command: []string{"npx", "@github/mcp-server"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if len(m2.ListServers()) != 2 {
		t.Fatalf("expected 2 servers after re-add, got %+v", m2.ListServers())
	}
}

func TestMCPServerManagerUpsertValidates(t *testing.T) {
	mcpTestIsolate(t)
	m, err := NewMCPServerManager(mcpTestConfig(nil), nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	// Empty name is rejected.
	if err := m.UpsertServer("", &config.MCPServerConfig{Type: "http", URL: "http://x/mcp"}); err == nil {
		t.Fatal("expected error for empty name")
	}
	// Server without command or url is rejected.
	if err := m.UpsertServer("bad", &config.MCPServerConfig{Type: "http"}); err == nil {
		t.Fatal("expected error for server without url/command")
	}
	if len(m.ListServers()) != 0 {
		t.Fatal("no servers should have been added")
	}
}

func TestMCPServerManagerRemoveUnknownServer(t *testing.T) {
	mcpTestIsolate(t)
	m, err := NewMCPServerManager(mcpTestConfig(nil), nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	// Removing a non-existent server records it in the removed list; no error.
	if err := m.RemoveServer("ghost"); err != nil {
		t.Fatalf("RemoveServer(ghost): %v", err)
	}
	if len(m.ListServers()) != 0 {
		t.Fatal("expected no servers")
	}
}

func TestMCPToolName(t *testing.T) {
	cases := []struct{ server, tool, want string }{
		{"github", "list_repos", "mcp_github_list_repos"},
		{"my mcp server", "do thing", "mcp_my_mcp_server_do_thing"},
		{"GitHub.API", "get.repo", "mcp_GitHub_API_get_repo"},
	}
	for _, c := range cases {
		if got := MCPToolName(c.server, c.tool); got != c.want {
			t.Errorf("MCPToolName(%q, %q) = %q, want %q", c.server, c.tool, got, c.want)
		}
	}

	// Non-ASCII server/tool names must be fully sanitized to [a-zA-Z0-9_-]
	// so provider function-name constraints are satisfied.
	got := MCPToolName("встреча", "tool")
	for _, r := range got {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			t.Errorf("unsanitized rune %q in MCPToolName output %q", r, got)
		}
	}
	if !strings.HasPrefix(got, "mcp_") || !strings.HasSuffix(got, "_tool") {
		t.Errorf("unexpected namespaced shape: %q", got)
	}
}

func TestAllowsMCPTool(t *testing.T) {
	exclude := &config.MCPServerConfig{Tools: &config.MCPServerToolsConfig{Exclude: []string{"mcp_github_delete_repo"}}}
	if allowsMCPTool(exclude, "mcp_github_list_repos") != true {
		t.Error("non-excluded tool should pass")
	}
	if allowsMCPTool(exclude, "mcp_github_delete_repo") != false {
		t.Error("excluded tool should be blocked")
	}

	include := &config.MCPServerConfig{Tools: &config.MCPServerToolsConfig{Include: []string{"mcp_github_list_repos"}}}
	if allowsMCPTool(include, "mcp_github_list_repos") != true {
		t.Error("included tool should pass")
	}
	if allowsMCPTool(include, "mcp_github_create_repo") != false {
		t.Error("non-included tool should be blocked")
	}

	both := &config.MCPServerConfig{Tools: &config.MCPServerToolsConfig{
		Include: []string{"mcp_github_*"},
		Exclude: []string{"mcp_github_delete_repo"},
	}}
	// Include is exact-match only; a wildcard entry allows nothing else.
	if allowsMCPTool(both, "mcp_github_delete_repo") != false {
		t.Error("exclude must win over include")
	}
}
