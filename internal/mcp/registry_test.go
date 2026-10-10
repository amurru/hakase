package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistryClient_SearchAndGetServer(t *testing.T) {
	mockServers := []RegistryServer{
		{
			Name:        "github",
			Version:     "1.0.0",
			Description: "GitHub MCP Server",
			Transports:  []string{"stdio"},
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-github"},
			Env:         map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": ""},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/servers" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(mockServers)
			return
		}
		if r.URL.Path == "/v1/servers/github" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(mockServers[0])
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewRegistryClient(ts.URL)

	// Test Search
	results, err := client.Search(context.Background(), "github", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "github" {
		t.Errorf("expected name github, got %s", results[0].Name)
	}

	// Test GetServer
	srv, err := client.GetServer(context.Background(), "github")
	if err != nil {
		t.Fatalf("GetServer failed: %v", err)
	}
	if srv.Name != "github" {
		t.Errorf("expected server name github, got %s", srv.Name)
	}
}
