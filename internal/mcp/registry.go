package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultRegistryURL is the default base URL for the official MCP registry.
const DefaultRegistryURL = "https://registry.modelcontextprotocol.org"

// RegistryServer represents an entry in the MCP server registry.
type RegistryServer struct {
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Transports  []string          `json:"transports,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	Command     []string          `json:"command,omitempty"`
	URL         string            `json:"url,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// RegistryClient is an HTTP client for querying MCP server registries.
type RegistryClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewRegistryClient initializes a RegistryClient with fallback to env or default URL.
func NewRegistryClient(baseURL string) *RegistryClient {
	if baseURL == "" {
		baseURL = os.Getenv("HAKASE_MCP_REGISTRY_URL")
	}
	if baseURL == "" {
		baseURL = DefaultRegistryURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &RegistryClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Search queries the registry for MCP servers matching query.
func (c *RegistryClient) Search(ctx context.Context, query string, limit int) ([]RegistryServer, error) {
	if limit <= 0 {
		limit = 20
	}

	endpoint := c.BaseURL + "/v1/servers"
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid registry url %q: %w", endpoint, err)
	}

	q := u.Query()
	if query != "" {
		q.Set("q", query)
	}
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned status %d %s", resp.StatusCode, resp.Status)
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding registry response: %w", err)
	}

	var servers []RegistryServer
	// Try parsing as array first
	if err := json.Unmarshal(raw, &servers); err == nil {
		return servers, nil
	}

	// Try parsing as object with servers or results wrapper
	var wrapper struct {
		Servers []RegistryServer `json:"servers"`
		Results []RegistryServer `json:"results"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		if len(wrapper.Servers) > 0 {
			return wrapper.Servers, nil
		}
		if len(wrapper.Results) > 0 {
			return wrapper.Results, nil
		}
	}

	return nil, nil
}

// GetServer fetches full manifest/details for a specific server name from registry.
func (c *RegistryClient) GetServer(ctx context.Context, name string) (*RegistryServer, error) {
	endpoint := fmt.Sprintf("%s/v1/servers/%s", c.BaseURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating get server request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching server %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("server %q not found in registry", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned status %d %s", resp.StatusCode, resp.Status)
	}

	var srv RegistryServer
	if err := json.NewDecoder(resp.Body).Decode(&srv); err != nil {
		return nil, fmt.Errorf("decoding server manifest: %w", err)
	}
	return &srv, nil
}
