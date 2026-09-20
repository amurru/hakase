package handlers

import (
	"net/http"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/sandbox"
)

// Build metadata for health reporting. Defaults describe an ad-hoc build;
// the web/serve bootstrap (cmd/hakase/web.go) overrides them from
// internal/cli build vars at startup.
var (
	HealthVersion = "dev"
	HealthCommit  = "unknown"
	HealthDate    = "unknown"
)

// HealthResponse is GET /api/health (liveness): always 200 when the process
// serves HTTP, plus build metadata so operators can tell what is running.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`
}

// ReadyMCPServer is one MCP server's readiness state.
type ReadyMCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ReadyResponse is GET /api/ready (readiness): version plus the signals an
// operator needs to decide whether the agent can do work - provider
// configured, MCP server states, sandbox mode/availability.
type ReadyResponse struct {
	Status   string `json:"status"` // ok | degraded
	Version  string `json:"version"`
	Provider struct {
		Name       string `json:"name"`
		Configured bool   `json:"configured"`
	} `json:"provider"`
	MCP struct {
		Servers []ReadyMCPServer `json:"servers"`
	} `json:"mcp"`
	Sandbox struct {
		Mode      string `json:"mode"`
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
	} `json:"sandbox"`
}

// ReadyHandler returns a handler for GET /api/ready. Unauthenticated like
// /api/health so container orchestrators can probe it. Never fails closed
// on missing config: an unconfigured provider reports degraded, not 500.
func ReadyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := ReadyResponse{Status: "ok", Version: HealthVersion}

		// Provider: best-effort config load (env-merged by LoadConfig).
		if path := resolveConfigPath("config.json"); path != "" {
			if cfg, err := config.LoadConfig(path); err == nil && cfg != nil {
				resp.Provider.Name = cfg.Provider
				resp.Provider.Configured = cfg.Provider != "" && cfg.APIKey != ""
			}
		}
		if resp.Provider.Name == "" {
			resp.Provider.Name = "unconfigured"
		}

		// MCP servers: nil manager means none configured (not an error).
		if mcp.MCPManager != nil {
			for _, st := range mcp.MCPManager.ListServers() {
				resp.MCP.Servers = append(resp.MCP.Servers, ReadyMCPServer{
					Name:   st.Name,
					Status: st.Status,
					Error:  st.Error,
				})
			}
		}
		if resp.MCP.Servers == nil {
			resp.MCP.Servers = []ReadyMCPServer{}
		}

		// Sandbox: nil or off means confinement disabled (available by
		// definition); paths mode is always available; bubblewrap/landlock
		// report the configured mode and are assumed available (the exec
		// path coerces to paths when the binary is missing).
		sb := sandbox.CurrentSandbox
		if sb == nil || sb.Mode == "" || sb.Mode == sandbox.SandboxModeOff {
			resp.Sandbox.Mode = "off"
			resp.Sandbox.Enabled = false
			resp.Sandbox.Available = true
		} else {
			resp.Sandbox.Mode = string(sb.Mode)
			resp.Sandbox.Enabled = true
			resp.Sandbox.Available = true
		}

		if !resp.Provider.Configured {
			resp.Status = "degraded"
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
