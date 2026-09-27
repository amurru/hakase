package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"amurru/hakase/internal/config"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiagnoseMeasuresDialCost(t *testing.T) {
	mcpTestIsolate(t)

	orig := defaultToolsListBudget
	defaultToolsListBudget = 200 * time.Millisecond
	t.Cleanup(func() { defaultToolsListBudget = orig })

	// One healthy server and one that hangs forever.
	realSrv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	realHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return realSrv }, nil)
	okSrv := httptest.NewServer(http.HandlerFunc(realHandler.ServeHTTP))
	t.Cleanup(okSrv.Close)

	hang := make(chan struct{})
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(func() { close(hang); badSrv.Close() })

	m, err := NewMCPServerManager(&config.Config{MCPServers: config.MCPConfig{
		Servers: map[string]*config.MCPServerConfig{
			"broken": {Type: "http", URL: badSrv.URL},
			"off":    {Type: "http", URL: "http://127.0.0.1:1", Disabled: true},
			// TimeoutMs bounds the client's standing SSE stream so the test
			// server closes promptly at cleanup; without it the lingering
			// stream holds httptest.Server.Close for the 10s default.
			"working": {Type: "http", URL: okSrv.URL, TimeoutMs: 250},
		}}}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	diags := m.Diagnose(mcpTestCtx{})
	if len(diags) != 3 {
		t.Fatalf("got %d diagnostics, want 3", len(diags))
	}

	byName := map[string]ServerDiagnostic{}
	for _, d := range diags {
		byName[d.Name] = d
	}

	// The whole point: the broken server's reported cost is bounded.
	broken := byName["broken"]
	if broken.OK {
		t.Error("broken server reported OK")
	}
	if broken.Dial == 0 {
		t.Error("broken server reported no dial time; the cost is the whole point")
	}
	if max := 4 * defaultToolsListBudget; broken.Dial > max {
		t.Errorf("broken server dial reported as %v, over %v: Diagnose is not measuring the bounded cost",
			broken.Dial, max)
	}
	if broken.Error == "" {
		t.Error("broken server reported no error")
	}
	if got := broken.StatusWord(); got != "UNREACHABLE" {
		t.Errorf("status word = %q, want UNREACHABLE", got)
	}

	// Disabled servers are not dialled at all.
	off := byName["off"]
	if !off.Disabled || off.Dial != 0 || off.StatusWord() != "disabled" {
		t.Errorf("disabled server: %+v (word %q), want disabled with no dial", off, off.StatusWord())
	}

	// A working server must still be reported as working.
	if w := byName["working"]; !w.OK {
		t.Errorf("working server reported not OK: %+v", w)
	}

	// Second call is served from the tool-list cache and must say so, so the
	// steady state of a healthy server reads as free.
	for _, d := range m.Diagnose(mcpTestCtx{}) {
		if d.Name == "working" && !d.Cached {
			t.Errorf("second Diagnose did not report the working server as cached: %+v", d)
		}
	}
}

func TestDiagnoseReportsUnbuildableToolset(t *testing.T) {
	mcpTestIsolate(t)
	// A stdio server whose command does not exist cannot build a toolset, so
	// it is skipped in Tools() and must be reported rather than silently
	// absent.
	m, err := NewMCPServerManager(&config.Config{MCPServers: config.MCPConfig{
		Servers: map[string]*config.MCPServerConfig{
			"ghost": {Type: "stdio", Command: []string{"/nonexistent/mcp-server-binary"}},
		}}}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	diags := m.Diagnose(mcpTestCtx{})
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	if diags[0].OK || diags[0].Error == "" {
		t.Errorf("unbuildable server reported %+v, want a failure with an error", diags[0])
	}
}

func TestDiagnoseOnEmptyConfig(t *testing.T) {
	mcpTestIsolate(t)
	m, err := NewMCPServerManager(&config.Config{}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	if got := m.Diagnose(mcpTestCtx{}); len(got) != 0 {
		t.Errorf("empty config returned %d diagnostics, want 0", len(got))
	}
}
