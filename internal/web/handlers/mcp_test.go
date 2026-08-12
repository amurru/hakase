package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
)

// setupMCPHandlerTest isolates the MCP user registry and installs a fresh
// manager as the package global.
func setupMCPHandlerTest(t *testing.T, servers map[string]*config.MCPServerConfig) *mcp.MCPServerManager {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	config.MCPRegistryFile = ""
	t.Cleanup(func() { config.MCPRegistryFile = "" })

	cfg := &config.Config{MCPServers: config.MCPConfig{Servers: servers}}
	mg, err := mcp.NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	old := mcp.MCPManager
	mcp.MCPManager = mg
	t.Cleanup(func() { mcp.MCPManager = old })
	return mg
}

func TestMCPServerList(t *testing.T) {
	setupMCPHandlerTest(t, map[string]*config.MCPServerConfig{
		"lightpanda": {Type: "http", URL: "http://localhost:9223/mcp"},
	})

	handler := (&MCPAPI{}).ListServers
	req := httptest.NewRequest("GET", "/mcp/servers", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var servers []MCPServerDTO
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "lightpanda" {
		t.Fatalf("expected lightpanda, got %+v", servers)
	}
	if servers[0].Config == nil || servers[0].Config.URL != "http://localhost:9223/mcp" {
		t.Fatalf("expected config in DTO, got %+v", servers[0])
	}
	// Env and Headers must always be write-only (no value echo on read).
	if len(servers[0].Config.Env) != 0 {
		t.Fatalf("env must be empty in read DTO, got %v", servers[0].Config.Env)
	}
	if len(servers[0].Config.Headers) != 0 {
		t.Fatalf("headers must be empty in read DTO, got %v", servers[0].Config.Headers)
	}
}

func TestMCPServerListStripsEnvHeaders(t *testing.T) {
	setupMCPHandlerTest(t, map[string]*config.MCPServerConfig{
		"github": {
			Type:    "stdio",
			Command: []string{"npx", "@github/mcp-server"},
			Env:     map[string]string{"GITHUB_PAT": "ghp_secret_token"},
			Headers: map[string]string{"Authorization": "Bearer abc123"},
		},
	})

	handler := (&MCPAPI{}).ListServers
	req := httptest.NewRequest("GET", "/mcp/servers", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the raw JSON body does not contain the secret values.
	body := w.Body.String()
	if strings.Contains(body, "ghp_secret_token") {
		t.Fatal("response must not contain env value")
	}
	if strings.Contains(body, "abc123") {
		t.Fatal("response must not contain headers value")
	}

	var servers []MCPServerDTO
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "github" {
		t.Fatalf("expected github server, got %+v", servers)
	}
	// Env and Headers must be empty maps (structure present, values stripped).
	if servers[0].Config == nil {
		t.Fatal("Config must not be nil")
	}
	if len(servers[0].Config.Env) != 0 {
		t.Fatalf("env must be empty after stripping, got %v", servers[0].Config.Env)
	}
	if len(servers[0].Config.Headers) != 0 {
		t.Fatalf("headers must be empty after stripping, got %v", servers[0].Config.Headers)
	}
	// Other config fields must still be present.
	if servers[0].Config.Type != "stdio" {
		t.Fatalf("expected type=stdio, got %q", servers[0].Config.Type)
	}
}

func TestMCPServerCreate(t *testing.T) {
	setupMCPHandlerTest(t, nil)

	body := `{"name": "web", "type": "http", "url": "http://localhost:9333/mcp"}`
	handler := (&MCPAPI{}).CreateServer
	req := httptest.NewRequest("POST", "/mcp/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	servers := mcp.MCPManager.ListServers()
	if len(servers) != 1 || servers[0].Name != "web" {
		t.Fatalf("expected web server, got %+v", servers)
	}
}

func TestMCPServerCreateEmptyName(t *testing.T) {
	setupMCPHandlerTest(t, nil)

	body := `{"name": "", "type": "http", "url": "http://localhost:9333/mcp"}`
	handler := (&MCPAPI{}).CreateServer
	req := httptest.NewRequest("POST", "/mcp/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMCPServerCreateInvalidConfig(t *testing.T) {
	setupMCPHandlerTest(t, nil)

	// No url and no command - validation must reject.
	body := `{"name": "bad", "type": "http"}`
	handler := (&MCPAPI{}).CreateServer
	req := httptest.NewRequest("POST", "/mcp/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "needs command") {
		t.Fatalf("expected validation error message, got: %s", w.Body.String())
	}
}

func TestMCPServerUpdate(t *testing.T) {
	setupMCPHandlerTest(t, map[string]*config.MCPServerConfig{
		"web": {Type: "http", URL: "http://localhost:9000/mcp"},
	})

	body := `{"type": "http", "url": "http://localhost:9001/mcp"}`
	handler := (&MCPAPI{}).UpdateServer
	req := httptest.NewRequest("PUT", "/mcp/servers/web", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if got, ok := mcp.MCPManager.ServerConfig("web"); !ok || got.URL != "http://localhost:9001/mcp" {
		t.Fatalf("expected updated URL, got %+v ok=%v", got, ok)
	}
}

func TestMCPServerDelete(t *testing.T) {
	setupMCPHandlerTest(t, map[string]*config.MCPServerConfig{
		"web": {Type: "http", URL: "http://localhost:9000/mcp"},
	})

	handler := (&MCPAPI{}).DeleteServer
	req := httptest.NewRequest("DELETE", "/mcp/servers/web", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if len(mcp.MCPManager.ListServers()) != 0 {
		t.Fatalf("expected server removed, got %+v", mcp.MCPManager.ListServers())
	}
}

func TestMCPServerDeleteProjectServerPersists(t *testing.T) {
	setupMCPHandlerTest(t, map[string]*config.MCPServerConfig{
		"github": {Type: "stdio", Command: []string{"npx", "@github/mcp-server"}},
	})

	handler := (&MCPAPI{}).DeleteServer
	req := httptest.NewRequest("DELETE", "/mcp/servers/github", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Rebuild a manager from the same config - the deletion must persist.
	m2, err := mcp.NewMCPServerManager(&config.Config{MCPServers: config.MCPConfig{
		Servers: map[string]*config.MCPServerConfig{
			"github": {Type: "stdio", Command: []string{"npx", "@github/mcp-server"}},
		},
	}}, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(m2.ListServers()) != 0 {
		t.Fatalf("project server deletion should persist, got %+v", m2.ListServers())
	}
}

func TestMCPServerManagerUnavailable(t *testing.T) {
	old := mcp.MCPManager
	mcp.MCPManager = nil
	t.Cleanup(func() { mcp.MCPManager = old })

	handler := (&MCPAPI{}).ListServers
	req := httptest.NewRequest("GET", "/mcp/servers", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMCPRouterRegistration(t *testing.T) {
	// Verify the router wires the new routes by exercising them through a
	// minimal fake router that records patterns.
	var patterns []string
	fr := &recordingMCPRouter{patterns: &patterns}

	RegisterMCPRoutes(fr)

	want := []string{
		"GET /mcp/servers",
		"POST /mcp/servers",
		"PUT /mcp/servers/{name}",
		"DELETE /mcp/servers/{name}",
		"POST /mcp/servers/{name}/enable",
		"POST /mcp/servers/{name}/disable",
		"POST /mcp/servers/{name}/reconnect",
	}
	for _, w := range want {
		found := false
		for _, p := range patterns {
			if p == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %q not registered; got %v", w, patterns)
		}
	}
}

type recordingMCPRouter struct {
	patterns *[]string
}

func (r *recordingMCPRouter) Get(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "GET "+pattern)
}
func (r *recordingMCPRouter) Post(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "POST "+pattern)
}
func (r *recordingMCPRouter) Put(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "PUT "+pattern)
}
func (r *recordingMCPRouter) Delete(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "DELETE "+pattern)
}

// ensure imports stay used when running only these tests
var _ = os.Getenv
var _ = filepath.Join
