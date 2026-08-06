package main

import (
	"os"
	"strings"
	"testing"
)

func setupMCPConfigTest(t *testing.T) {
	t.Helper()
	// Isolate ~/.hakase so user mcp.json does not leak.
	t.Setenv("HAKASE_HOME", t.TempDir())
	// Reset the cached mcp registry file path between tests.
	mcpRegistryFile = ""
	t.Cleanup(func() { mcpRegistryFile = "" })
}

func TestLoadMCPRegistryLegacyMigration(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServerURL: "http://localhost:9223/mcp",
	}
	reg, err := LoadMCPRegistry(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg == nil || reg.Servers == nil {
		t.Fatal("registry or servers map is nil")
	}
	srv, ok := reg.Servers["lightpanda"]
	if !ok {
		t.Fatal("expected lightpanda server from legacy migration")
	}
	if srv.Type != "http" {
		t.Errorf("Type = %q, want %q", srv.Type, "http")
	}
	if srv.URL != "http://localhost:9223/mcp" {
		t.Errorf("URL = %q, want %q", srv.URL, "http://localhost:9223/mcp")
	}
}

func TestLoadMCPRegistryLegacyNotDuplicated(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServerURL: "http://localhost:9223/mcp",
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"lightpanda": {
					Type: "http",
					URL:  "http://other:8080/mcp",
				},
			},
		},
	}
	reg, err := LoadMCPRegistry(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := reg.Servers["lightpanda"]
	if srv.URL != "http://other:8080/mcp" {
		t.Errorf("URL = %q, want user entry %q", srv.URL, "http://other:8080/mcp")
	}
}

func TestLoadMCPRegistryUserWinsOnCollision(t *testing.T) {
	setupMCPConfigTest(t)

	// Seed a user registry with a same-name server.
	userReg := MCPUserRegistry{
		Servers: map[string]*MCPServerConfig{
			"github": {
				Type:    "stdio",
				Command: []string{"npx", "-y", "@user/mcp-server"},
			},
		},
	}
	if err := saveMCPUserRegistry(userReg); err != nil {
		t.Fatalf("save user registry: %v", err)
	}

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"github": {
					Type:    "stdio",
					Command: []string{"npx", "-y", "@project/mcp-server"},
				},
			},
		},
	}
	reg, err := LoadMCPRegistry(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := reg.Servers["github"]
	if srv.Command[0] != "npx" || srv.Command[2] != "@user/mcp-server" {
		t.Errorf("Command = %v, want user entry to win", srv.Command)
	}
}

func TestLoadMCPRegistryUserOnlyServer(t *testing.T) {
	setupMCPConfigTest(t)

	userReg := MCPUserRegistry{
		Servers: map[string]*MCPServerConfig{
			"user-only": {
				Type:    "stdio",
				Command: []string{"my-server"},
			},
		},
	}
	if err := saveMCPUserRegistry(userReg); err != nil {
		t.Fatalf("save user registry: %v", err)
	}

	cfg := &Config{}
	reg, err := LoadMCPRegistry(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := reg.Servers["user-only"]; !ok {
		t.Fatal("expected user-only server to appear in merged registry")
	}
}

func TestLoadMCPRegistryDisabledApplies(t *testing.T) {
	setupMCPConfigTest(t)

	userReg := MCPUserRegistry{
		Disabled: []string{"disabled-one"},
	}
	if err := saveMCPUserRegistry(userReg); err != nil {
		t.Fatalf("save user registry: %v", err)
	}

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"disabled-one": {
					Type:    "stdio",
					Command: []string{"npx", "-y", "some-server"},
				},
				"enabled-one": {
					Type:    "stdio",
					Command: []string{"npx", "-y", "another-server"},
				},
			},
		},
	}
	reg, err := LoadMCPRegistry(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reg.Servers["disabled-one"].Disabled {
		t.Error("expected disabled-one to be marked Disabled")
	}
	if reg.Servers["enabled-one"].Disabled {
		t.Error("expected enabled-one to NOT be marked Disabled")
	}
}

func TestLoadMCPRegistryValidationBadType(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"bad": {
					Type:    "ssh",
					Command: []string{"echo"},
				},
			},
		},
	}
	_, err := LoadMCPRegistry(cfg)
	if err == nil {
		t.Fatal("expected validation error for bad type")
	}
	if !strings.Contains(err.Error(), "invalid mcp server") {
		t.Errorf("error should mention invalid server: %v", err)
	}
}

func TestLoadMCPRegistryValidationEmptyCommandAndURL(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"empty": {},
			},
		},
	}
	_, err := LoadMCPRegistry(cfg)
	if err == nil {
		t.Fatal("expected validation error for empty command and url")
	}
	if !strings.Contains(err.Error(), "command (stdio) or url (http)") {
		t.Errorf("error should mention command or url: %v", err)
	}
}

func TestLoadMCPRegistryValidationBadURLScheme(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"bad-url": {
					URL: "ftp://example.com/mcp",
				},
			},
		},
	}
	_, err := LoadMCPRegistry(cfg)
	if err == nil {
		t.Fatal("expected validation error for bad URL scheme")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Errorf("error should mention http or https: %v", err)
	}
}

func TestLoadMCPRegistryValidationEmptyCommandFirstElement(t *testing.T) {
	setupMCPConfigTest(t)

	cfg := &Config{
		MCPServers: MCPConfig{
			Servers: map[string]*MCPServerConfig{
				"bad-cmd": {
					Command: []string{"", "arg"},
				},
			},
		},
	}
	_, err := LoadMCPRegistry(cfg)
	if err == nil {
		t.Fatal("expected validation error for empty first command element")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("error should mention non-empty: %v", err)
	}
}

func TestExpandEnvVarSet(t *testing.T) {
	t.Setenv("MY_VAR", "hello")
	got := ExpandEnv("prefix-${MY_VAR}-suffix")
	if got != "prefix-hello-suffix" {
		t.Errorf("ExpandEnv = %q, want %q", got, "prefix-hello-suffix")
	}
}

func TestExpandEnvVarUnset(t *testing.T) {
	t.Setenv("MY_VAR", "")
	os.Unsetenv("UNSET_VAR")
	got := ExpandEnv("prefix-${UNSET_VAR}-suffix")
	if got != "prefix--suffix" {
		t.Errorf("ExpandEnv = %q, want %q", got, "prefix--suffix")
	}
}

func TestExpandEnvVarDefault(t *testing.T) {
	t.Setenv("MY_VAR", "")
	os.Unsetenv("UNSET_VAR")
	got := ExpandEnv("${UNSET_VAR:-fallback}")
	if got != "fallback" {
		t.Errorf("ExpandEnv = %q, want %q", got, "fallback")
	}
}

func TestExpandEnvMultiple(t *testing.T) {
	t.Setenv("A", "1")
	t.Setenv("B", "2")
	got := ExpandEnv("${A}-${B}-${C:-3}")
	if got != "1-2-3" {
		t.Errorf("ExpandEnv = %q, want %q", got, "1-2-3")
	}
}

func TestExpandEnvMalformed(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"${UNCLOSED", "${UNCLOSED"},
		{"${", "${"},
		{"${}emptyvar", "${}emptyvar"},
		{"plain text", "plain text"},
	}
	for _, tc := range tests {
		got := ExpandEnv(tc.in)
		if got != tc.out {
			t.Errorf("ExpandEnv(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func TestExpandEnvMap(t *testing.T) {
	t.Setenv("MY_KEY", "secret")
	in := map[string]string{
		"key1": "${MY_KEY}",
		"key2": "static",
	}
	out := ExpandEnvMap(in)
	if out["key1"] != "secret" {
		t.Errorf("ExpandEnvMap key1 = %q, want %q", out["key1"], "secret")
	}
	if out["key2"] != "static" {
		t.Errorf("ExpandEnvMap key2 = %q, want %q", out["key2"], "static")
	}
	if in["key1"] != "${MY_KEY}" {
		t.Error("ExpandEnvMap mutated input")
	}
}

func TestExpandEnvMapNil(t *testing.T) {
	if out := ExpandEnvMap(nil); out != nil {
		t.Errorf("ExpandEnvMap(nil) = %v, want nil", out)
	}
}

func TestSanitizeMCPServerName(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"my-server", "my-server"},
		{"My_Server", "My_Server"},
		{"my.server", "my_server"},
		{"name with spaces", "name_with_spaces"},
		{"日本語", "___"},
		{"a/b", "a_b"},
		{"valid_name-123", "valid_name-123"},
	}
	for _, tc := range tests {
		got := SanitizeMCPServerName(tc.in)
		if got != tc.out {
			t.Errorf("SanitizeMCPServerName(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}
