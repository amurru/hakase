package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

func TestInstallServerAndCredentialPlan(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HAKASE_HOME", tempHome)
	config.MCPRegistryFile = ""
	t.Cleanup(func() { config.MCPRegistryFile = "" })

	mockServer := RegistryServer{
		Name:       "test-server",
		Version:    "1.0.0",
		Transports: []string{"stdio"},
		Command:    []string{"node", "index.js"},
		Env:        map[string]string{"API_KEY": ""},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockServer)
	}))
	defer ts.Close()

	t.Setenv("HAKASE_MCP_REGISTRY_URL", ts.URL)

	cfg := &config.Config{}
	mgr, err := NewMCPServerManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewMCPServerManager failed: %v", err)
	}

	// Test credential plan missing error when Yes is false
	opts := InstallOptions{
		Ref:   "test-server",
		Scope: "user",
		Yes:   false,
	}
	_, err = InstallServer(context.Background(), mgr, opts)
	if err == nil {
		t.Fatal("expected error due to missing API_KEY credential, got nil")
	}

	// Test successful install with provided allow-env
	opts.AllowEnv = map[string]string{"API_KEY": "secret123"}
	opts.Yes = true

	srvCfg, err := InstallServer(context.Background(), mgr, opts)
	if err != nil {
		t.Fatalf("InstallServer failed: %v", err)
	}

	if srvCfg.Env["API_KEY"] != "${API_KEY}" {
		t.Errorf("expected API_KEY=${API_KEY} in env placeholder, got %s", srvCfg.Env["API_KEY"])
	}

	// Verify user config file was updated and contains NO raw secret
	mcpPath := filepath.Join(tempHome, "mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading %s: %v", mcpPath, err)
	}
	if strings.Contains(string(data), "secret123") {
		t.Errorf("secret123 persisted in %s; secrets must never be saved in plain text!", mcpPath)
	}
}
