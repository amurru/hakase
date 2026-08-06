package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setupMCPPersistTest(t *testing.T) {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	mcpRegistryFile = ""
	t.Cleanup(func() { mcpRegistryFile = "" })
}

func TestMCPUserRegistryRoundTrip(t *testing.T) {
	setupMCPPersistTest(t)

	reg := MCPUserRegistry{
		Servers: map[string]*MCPServerConfig{
			"github": {
				Type:    "stdio",
				Command: []string{"npx", "-y", "@github/mcp-server"},
			},
		},
		Disabled: []string{"old-server"},
	}

	if err := saveMCPUserRegistry(reg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadMCPUserRegistry()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(loaded.Servers))
	}
	if loaded.Servers["github"].Command[2] != "@github/mcp-server" {
		t.Errorf("Command = %v, want @github/mcp-server", loaded.Servers["github"].Command)
	}
	if len(loaded.Disabled) != 1 || loaded.Disabled[0] != "old-server" {
		t.Errorf("Disabled = %v, want [old-server]", loaded.Disabled)
	}
}

func TestMCPUserRegistryMissingFile(t *testing.T) {
	setupMCPPersistTest(t)

	reg, err := loadMCPUserRegistry()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.Servers != nil {
		t.Errorf("expected nil Servers for missing file, got %v", reg.Servers)
	}
	if reg.Disabled != nil {
		t.Errorf("expected nil Disabled for missing file, got %v", reg.Disabled)
	}
}

func TestMCPUserRegistryUpdateMutation(t *testing.T) {
	setupMCPPersistTest(t)

	// Seed an initial registry.
	initial := MCPUserRegistry{
		Servers: map[string]*MCPServerConfig{
			"s1": {
				Type:    "stdio",
				Command: []string{"echo"},
			},
		},
	}
	if err := saveMCPUserRegistry(initial); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	// Update to add a server.
	if err := updateMCPUserRegistry(func(reg *MCPUserRegistry) error {
		reg.Servers = map[string]*MCPServerConfig{
			"s1": {
				Type:    "stdio",
				Command: []string{"echo"},
			},
			"s2": {
				Type: "http",
				URL:  "http://localhost:8080/mcp",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	loaded, err := loadMCPUserRegistry()
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	if len(loaded.Servers) != 2 {
		t.Fatalf("expected 2 servers after update, got %d", len(loaded.Servers))
	}
	if loaded.Servers["s2"] == nil || loaded.Servers["s2"].URL != "http://localhost:8080/mcp" {
		t.Errorf("s2 URL = %q, want %q",
			func() string {
				if loaded.Servers["s2"] != nil {
					return loaded.Servers["s2"].URL
				}
				return "nil"
			}(),
			"http://localhost:8080/mcp")
	}
}

func TestMCPUserRegistryCorruptFile(t *testing.T) {
	setupMCPPersistTest(t)

	// Write corrupt JSON directly to the file.
	dir := hakaseHome()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fpath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(fpath, []byte("not json{{{"), 0644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, err := loadMCPUserRegistry()
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
}

func TestMCPUserRegistryUpdateError(t *testing.T) {
	setupMCPPersistTest(t)

	// Seed a valid registry.
	initial := MCPUserRegistry{
		Servers: map[string]*MCPServerConfig{
			"s1": {Type: "stdio", Command: []string{"echo"}},
		},
	}
	if err := saveMCPUserRegistry(initial); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	// Update returns an error - the file should not change.
	err := updateMCPUserRegistry(func(reg *MCPUserRegistry) error {
		reg.Servers["extra"] = &MCPServerConfig{Type: "stdio", Command: []string{"bad"}}
		return os.ErrNotExist // arbitrary error to cancel the mutation
	})
	if err == nil {
		t.Fatal("expected update to return error")
	}

	loaded, err := loadMCPUserRegistry()
	if err != nil {
		t.Fatalf("load after failed update: %v", err)
	}
	if len(loaded.Servers) != 1 {
		t.Errorf("expected original 1 server after failed update, got %d", len(loaded.Servers))
	}
}
