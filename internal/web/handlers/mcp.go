// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"net/http"

	"amurru/hakase/internal/mcp"
	"github.com/go-chi/chi/v5"
)

// MCPServerDTO is the API response for an MCP server.
type MCPServerDTO struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Transport string `json:"transport"`
	Disabled  bool   `json:"disabled"`
	ToolCount int    `json:"toolCount"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// MCPRouter is the minimum interface needed by RegisterMCPRoutes.
type MCPRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// MCPAPI wraps MCP server operations for the web API layer.
type MCPAPI struct{}

// RegisterMCPRoutes registers all MCP server API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterMCPRoutes(r MCPRouter) {
	api := &MCPAPI{}

	r.Get("/mcp/servers", api.ListServers)
	r.Post("/mcp/servers/{name}/enable", api.EnableServer)
	r.Post("/mcp/servers/{name}/disable", api.DisableServer)
	r.Post("/mcp/servers/{name}/reconnect", api.ReconnectServer)
}

// mcpName extracts the {name} URL parameter from the request.
func mcpName(r *http.Request) string {
	return chi.URLParam(r, "name")
}

// ListServers handles GET /mcp/servers - returns all configured MCP servers.
func (api *MCPAPI) ListServers(w http.ResponseWriter, r *http.Request) {
	if mcp.MCPManager == nil {
		writeJSON(w, http.StatusOK, []MCPServerDTO{})
		return
	}

	servers := mcp.MCPManager.ListServers()
	dtos := make([]MCPServerDTO, 0, len(servers))
	for _, s := range servers {
		dtos = append(dtos, MCPServerDTO{
			Name:      s.Name,
			Type:      s.Type,
			Transport: s.Transport,
			Disabled:  s.Disabled,
			ToolCount: s.ToolCount,
			Status:    s.Status,
			Error:     s.Error,
		})
	}

	writeJSON(w, http.StatusOK, dtos)
}

// EnableServer handles POST /mcp/servers/{name}/enable - enables a server.
func (api *MCPAPI) EnableServer(w http.ResponseWriter, r *http.Request) {
	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	if mcp.MCPManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MCP manager not available"})
		return
	}

	if err := mcp.MCPManager.SetDisabled(name, false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

// DisableServer handles POST /mcp/servers/{name}/disable - disables a server.
func (api *MCPAPI) DisableServer(w http.ResponseWriter, r *http.Request) {
	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	if mcp.MCPManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MCP manager not available"})
		return
	}

	if err := mcp.MCPManager.SetDisabled(name, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

// ReconnectServer handles POST /mcp/servers/{name}/reconnect - reconnects a server.
func (api *MCPAPI) ReconnectServer(w http.ResponseWriter, r *http.Request) {
	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	if mcp.MCPManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MCP manager not available"})
		return
	}

	if err := mcp.MCPManager.Reconnect(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reconnected"})
}
