package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"amurru/hakase/internal/sandbox"
)

// TestValidateConfigUpdateRefusesLandlock pins issue #14: the settings API
// must refuse landlock mode (unimplemented) instead of persisting a config
// that silently degrades at exec time.
func TestValidateConfigUpdateRefusesLandlock(t *testing.T) {
	var req map[string]interface{}
	if err := json.Unmarshal([]byte(`{"sandbox": {"mode": "landlock"}}`), &req); err != nil {
		t.Fatalf("bad test body: %v", err)
	}
	err := validateConfigUpdate(req)
	if err == nil || !strings.Contains(err.Error(), "landlock") {
		t.Fatalf("expected landlock refusal error, got %v", err)
	}

	for _, mode := range []string{"paths", "bubblewrap", "off"} {
		var ok map[string]interface{}
		if err := json.Unmarshal([]byte(`{"sandbox": {"mode": "`+mode+`"}}`), &ok); err != nil {
			t.Fatalf("bad test body: %v", err)
		}
		if err := validateConfigUpdate(ok); err != nil {
			t.Errorf("validateConfigUpdate(%q) = %v, want nil", mode, err)
		}
	}
}

// TestReadyDegradesOnUnenforceableSandbox verifies GET /api/ready surfaces an
// unenforceable sandbox as degraded readiness (visible notice, issue #14).
// Landlock is always unenforceable, so the test is hermetic regardless of
// whether bwrap is installed on the test machine.
func TestReadyDegradesOnUnenforceableSandbox(t *testing.T) {
	saved := sandbox.CurrentSandbox
	t.Cleanup(func() { sandbox.CurrentSandbox = saved })

	sandbox.CurrentSandbox = &sandbox.SandboxConfig{Mode: sandbox.SandboxModeLandlock}
	w := httptest.NewRecorder()
	ReadyHandler()(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready = %d, want 200", w.Code)
	}
	var resp ReadyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Sandbox.Mode != "landlock" {
		t.Errorf("sandbox.mode = %q, want landlock", resp.Sandbox.Mode)
	}
	if resp.Sandbox.Available {
		t.Error("landlock sandbox should report available=false")
	}
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want degraded for unenforceable sandbox", resp.Status)
	}

	// Paths mode stays available and does not force degraded on its own.
	// (Provider may still be unconfigured -> degraded; only assert Available.)
	sandbox.CurrentSandbox = &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths}
	w = httptest.NewRecorder()
	ReadyHandler()(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready = %d, want 200", w.Code)
	}
	var resp2 ReadyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp2.Sandbox.Available {
		t.Error("paths sandbox should report available=true")
	}
}
