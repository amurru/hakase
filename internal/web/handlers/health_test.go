package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthIncludesVersion verifies liveness carries build metadata.
func TestHealthIncludesVersion(t *testing.T) {
	oldV := HealthVersion
	HealthVersion = "v9.9.9-test"
	t.Cleanup(func() { HealthVersion = oldV })

	w := httptest.NewRecorder()
	HealthHandler()(w, httptest.NewRequest("GET", "/api/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("health = %d, want 200", w.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" || resp.Version != "v9.9.9-test" {
		t.Fatalf("resp = %+v, want status ok + test version", resp)
	}
}

// TestReadyReportsSignals verifies readiness detail shape: version,
// provider block, mcp list, sandbox block.
func TestReadyReportsSignals(t *testing.T) {
	oldV := HealthVersion
	HealthVersion = "v9.9.9-test"
	t.Cleanup(func() { HealthVersion = oldV })

	w := httptest.NewRecorder()
	ReadyHandler()(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready = %d, want 200", w.Code)
	}
	var resp ReadyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != "v9.9.9-test" {
		t.Fatalf("version = %q, want test version", resp.Version)
	}
	if resp.Status != "ok" && resp.Status != "degraded" {
		t.Fatalf("status = %q, want ok|degraded", resp.Status)
	}
	if resp.MCP.Servers == nil {
		t.Fatal("mcp.servers should be non-nil (empty when unconfigured)")
	}
	if resp.Sandbox.Mode == "" {
		t.Fatal("sandbox.mode should always be set")
	}
}
