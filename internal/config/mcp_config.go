package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// MCPServerConfig describes one MCP server. Name comes from the map key.
type MCPServerConfig struct {
	Type      string                `json:"type,omitempty"`       // "stdio" (default when Command set) | "http"
	Command   []string              `json:"command,omitempty"`    // stdio argv, e.g. ["npx","-y","@github/mcp-server"]
	Env       map[string]string     `json:"env,omitempty"`        // extra env for the stdio child
	URL       string                `json:"url,omitempty"`        // streamable HTTP endpoint
	Headers   map[string]string     `json:"headers,omitempty"`    // HTTP headers
	Disabled  bool                  `json:"disabled,omitempty"`   // explicit opt-out; default = enabled
	TimeoutMs int                   `json:"timeout_ms,omitempty"` // http servers: per-request timeout (connect+initialize+tools/list); default 10s
	Tools     *MCPServerToolsConfig `json:"tools,omitempty"`      // per-server tool filtering
	OAuth     map[string]string     `json:"oauth,omitempty"`      // reserved (phase 3): remote auth
}

// MCPServerToolsConfig holds per-server tool allow/deny lists.
type MCPServerToolsConfig struct {
	Include []string `json:"include,omitempty"` // allow-list of tool names (post-namespacing)
	Exclude []string `json:"exclude,omitempty"` // deny-list of tool names
}

// MCPConfig is the project-scope config block (config.json "mcp").
type MCPConfig struct {
	Servers map[string]*MCPServerConfig `json:"servers,omitempty"`
}

// MCPUserRegistry is the TUI-writable user-level state (~/.hakase/mcp.json).
type MCPUserRegistry struct {
	Servers  map[string]*MCPServerConfig `json:"servers,omitempty"`  // user-added servers (full config)
	Disabled []string                    `json:"disabled,omitempty"` // runtime-disabled server names
	Removed  []string                    `json:"removed,omitempty"`  // project servers the user deleted
}

// MCPRegistry is the effective merged view consumed by the runtime manager.
type MCPRegistry struct {
	Servers map[string]*MCPServerConfig // name -> effective config (Disabled already resolved)
}

// Validate checks that the server config is well-formed.
func (s *MCPServerConfig) Validate() error {
	if s.Type != "" && s.Type != "stdio" && s.Type != "http" {
		return fmt.Errorf("invalid mcp server type %q: must be \"\", \"stdio\", or \"http\"", s.Type)
	}
	if len(s.Command) > 0 {
		if s.Command[0] == "" {
			return fmt.Errorf("server command first element must be non-empty")
		}
	}
	if s.URL != "" {
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("invalid mcp server url %q: %w", s.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid mcp server url %q: scheme must be http or https", s.URL)
		}
	}
	if len(s.Command) == 0 && s.URL == "" {
		return fmt.Errorf("server needs command (stdio) or url (http)")
	}
	for k := range s.Env {
		if k == "" {
			return fmt.Errorf("env key must be non-empty")
		}
	}
	for k := range s.Headers {
		if k == "" {
			return fmt.Errorf("header key must be non-empty")
		}
	}
	return nil
}

// LoadMCPRegistry builds the effective MCP registry from project config,
// legacy migration, and user overrides.
func LoadMCPRegistry(cfg *Config) (*MCPRegistry, error) {
	merged := make(map[string]*MCPServerConfig)

	// 1. Seed from project config.
	for name, srv := range cfg.MCPServers.Servers {
		copy := *srv
		merged[name] = &copy
	}

	// 2. Legacy migration: mcp_server_url -> "lightpanda" server.
	if cfg.MCPServerURL != "" {
		if _, exists := merged["lightpanda"]; !exists {
			merged["lightpanda"] = &MCPServerConfig{
				Type: "http",
				URL:  cfg.MCPServerURL,
			}
		}
	}

	// 3. Load user registry; each user server FULL-ENTRY overrides project.
	userReg, err := loadMCPUserRegistry()
	if err != nil {
		return nil, fmt.Errorf("loading user mcp registry: %w", err)
	}

	// 3b. Apply removed list: project-config servers the user deleted via the
	// web/TUI are hidden. User registry re-adds (applied next) win.
	removedSet := make(map[string]bool, len(userReg.Removed))
	for _, name := range userReg.Removed {
		removedSet[name] = true
	}
	for name := range merged {
		if removedSet[name] {
			delete(merged, name)
		}
	}

	for name, srv := range userReg.Servers {
		copy := *srv
		merged[name] = &copy
	}

	// 4. Apply disabled list.
	for _, name := range userReg.Disabled {
		if srv, ok := merged[name]; ok {
			srv.Disabled = true
		}
	}

	// 5. Validate all servers.
	for name, srv := range merged {
		if err := srv.Validate(); err != nil {
			return nil, fmt.Errorf("invalid mcp server %q: %w", name, err)
		}
	}

	return &MCPRegistry{Servers: merged}, nil
}

var sanitizeMCPServerNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SanitizeMCPServerName replaces every rune not in [a-zA-Z0-9_-] with "_".
func SanitizeMCPServerName(name string) string {
	return sanitizeMCPServerNameRe.ReplaceAllString(name, "_")
}

var expandEnvRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ExpandEnv expands ${VAR} and ${VAR:-default} forms using os.Getenv.
// Unknown var without default expands to empty string. Unclosed/malformed
// ${ left as-is. Supports multiple expansions in one string.
func ExpandEnv(value string) string {
	return expandEnvRe.ReplaceAllStringFunc(value, func(match string) string {
		// match is like "${VAR}" or "${VAR:-default}"
		submatches := expandEnvRe.FindStringSubmatch(match)
		if submatches == nil {
			return match
		}
		varName := submatches[1]
		defaultVal := ""
		if len(submatches) > 2 && strings.Contains(match, ":-") {
			defaultVal = submatches[2]
		}
		if v, ok := os.LookupEnv(varName); ok {
			return v
		}
		if strings.Contains(match, ":-") {
			return defaultVal
		}
		return ""
	})
}

// ExpandEnvMap applies ExpandEnv to every VALUE in m (keys untouched).
// Returns a copy, never mutates input.
func ExpandEnvMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = ExpandEnv(v)
	}
	return out
}
