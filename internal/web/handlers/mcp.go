// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"github.com/go-chi/chi/v5"
)

// MCPServerDTO is the API response for an MCP server. Config carries the
// effective server definition (type/url/command/env/headers) for editing.
type MCPServerDTO struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Transport string `json:"transport"`
	Disabled  bool   `json:"disabled"`
	ToolCount int    `json:"toolCount"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	// Config is the editable server definition (merged project + user state).
	Config *config.MCPServerConfig `json:"config,omitempty"`
}

// MCPServerRequest is the write payload for add/update.
type MCPServerRequest struct {
	Name      string              `json:"name"`
	Type      string              `json:"type"`
	URL       string              `json:"url"`
	Command   []string            `json:"command"`
	Env       map[string]string   `json:"env"`
	Headers   map[string]string   `json:"headers"`
	Disabled  bool                `json:"disabled"`
	Tools     *config.MCPServerToolsConfig `json:"tools"`
	TimeoutMs int                 `json:"timeout_ms"`
	OAuth     map[string]string   `json:"oauth"`
}

// MCPRouter is the minimum interface needed by RegisterMCPRoutes.
type MCPRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Put(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
}

// MCPAPI wraps MCP server operations for the web API layer.
type MCPAPI struct{}

// RegisterMCPRoutes registers all MCP server API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterMCPRoutes(r MCPRouter) {
	api := &MCPAPI{}

	r.Get("/mcp/servers", api.ListServers)
	r.Post("/mcp/servers", api.CreateServer)
	r.Put("/mcp/servers/{name}", api.UpdateServer)
	r.Delete("/mcp/servers/{name}", api.DeleteServer)
	r.Post("/mcp/servers/{name}/enable", api.EnableServer)
	r.Post("/mcp/servers/{name}/disable", api.DisableServer)
	r.Post("/mcp/servers/{name}/reconnect", api.ReconnectServer)
}

// mcpName extracts the {name} URL parameter from the request. It prefers the
// chi route context (normal HTTP flow) and falls back to parsing the path so
// handlers remain unit-testable without a router.
func mcpName(r *http.Request) string {
	if name := chi.URLParam(r, "name"); name != "" {
		return name
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, p := range parts {
		if p == "servers" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// manager returns the live MCP manager or writes a 503 and returns nil.
func manager(w http.ResponseWriter) *mcp.MCPServerManager {
	if mcp.MCPManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MCP manager not available"})
		return nil
	}
	return mcp.MCPManager
}

// ListServers handles GET /mcp/servers - returns all configured MCP servers.
func (api *MCPAPI) ListServers(w http.ResponseWriter, r *http.Request) {
	mg := manager(w)
	if mg == nil {
		return
	}

	servers := mg.ListServers()
	dtos := make([]MCPServerDTO, 0, len(servers))
	for _, s := range servers {
		dto := MCPServerDTO{
			Name:      s.Name,
			Type:      s.Type,
			Transport: s.Transport,
			Disabled:  s.Disabled,
			ToolCount: s.ToolCount,
			Status:    s.Status,
			Error:     s.Error,
		}
		if cfg, ok := mg.ServerConfig(s.Name); ok {
			dto.Config = cfg
		}
		dtos = append(dtos, dto)
	}

	writeJSON(w, http.StatusOK, dtos)
}

// CreateServer handles POST /mcp/servers - adds a new server.
func (api *MCPAPI) CreateServer(w http.ResponseWriter, r *http.Request) {
	mg := manager(w)
	if mg == nil {
		return
	}

	var req MCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	srv := serverRequestToConfig(&req)
	if err := mg.UpsertServer(req.Name, srv); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "name": req.Name})
}

// UpdateServer handles PUT /mcp/servers/{name} - replaces an existing server.
func (api *MCPAPI) UpdateServer(w http.ResponseWriter, r *http.Request) {
	mg := manager(w)
	if mg == nil {
		return
	}

	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	var req MCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	srv := serverRequestToConfig(&req)
	if err := mg.UpsertServer(name, srv); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": name})
}

// DeleteServer handles DELETE /mcp/servers/{name} - removes a server.
func (api *MCPAPI) DeleteServer(w http.ResponseWriter, r *http.Request) {
	mg := manager(w)
	if mg == nil {
		return
	}

	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	if err := mg.RemoveServer(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// serverRequestToConfig converts a write request into an MCPServerConfig.
func serverRequestToConfig(req *MCPServerRequest) *config.MCPServerConfig {
	return &config.MCPServerConfig{
		Type:      req.Type,
		Command:   req.Command,
		Env:       req.Env,
		URL:       req.URL,
		Headers:   req.Headers,
		Disabled:  req.Disabled,
		Tools:     req.Tools,
		TimeoutMs: req.TimeoutMs,
		OAuth:     req.OAuth,
	}
}

// EnableServer handles POST /mcp/servers/{name}/enable - enables a server.
func (api *MCPAPI) EnableServer(w http.ResponseWriter, r *http.Request) {
	name := mcpName(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name is required"})
		return
	}

	mg := manager(w)
	if mg == nil {
		return
	}

	if err := mg.SetDisabled(name, false); err != nil {
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

	mg := manager(w)
	if mg == nil {
		return
	}

	if err := mg.SetDisabled(name, true); err != nil {
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

	mg := manager(w)
	if mg == nil {
		return
	}

	if err := mg.Reconnect(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reconnected"})
}
