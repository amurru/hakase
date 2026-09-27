package mcp

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/config"
)

// TestDeadServerCostIsAmortized reproduces a real trace
// (logs/hakase-debug-20260927T151220, 15:12): "what's the real price of crypto
// trading?" took 191s, of which 155s was dialling one unreachable MCP server
// (77s) twice in the same turn. Two independent faults compounded:
//
//  1. Tools() captured one `now` before the dial loop and armed the failure
//     cooldown with it, so for a server slower than the window being armed the
//     cooldown was born expired and every model call re-dialled.
//  2. The go-sdk retries a failed initialize 5 times and then sleeps through a
//     backoff that ignores context cancellation, so timeout_ms bounded one
//     attempt rather than the call.
//
// Together they made a dead server cost its full price on every model call
// instead of once per cooldown window.
func TestDeadServerCostIsAmortized(t *testing.T) {
	mcpTestIsolate(t)

	// Shrink both windows so the slow-failure shape is testable, preserving
	// the trace's ratio: a 77s failure against a 30s cooldown.
	origBase, origMax, origBudget := failureCooldownBase, failureCooldownMax, defaultToolsListBudget
	failureCooldownBase, failureCooldownMax = 100*time.Millisecond, 100*time.Millisecond
	defaultToolsListBudget = 300 * time.Millisecond
	t.Cleanup(func() {
		failureCooldownBase, failureCooldownMax, defaultToolsListBudget = origBase, origMax, origBudget
	})

	var hits atomic.Int32
	const hang = 300 * time.Millisecond // scaled stand-in for the 77s failure
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(hang):
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	m, err := NewMCPServerManager(&config.Config{MCPServers: config.MCPConfig{
		Servers: map[string]*config.MCPServerConfig{
			"deepwiki": {Type: "http", URL: srv.URL},
		}}}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	// Five model calls, as in a short multi-turn session.
	const modelCalls = 5
	start := time.Now()
	for i := 0; i < modelCalls; i++ {
		if _, err := m.Tools(mcpTestCtx{}); err != nil {
			t.Fatal(err)
		}
	}
	total := time.Since(start)
	t.Logf("%d model calls with one dead server: %v total, %v per call, %d requests",
		modelCalls, total.Round(time.Millisecond), (total / modelCalls).Round(time.Millisecond), hits.Load())

	// Only the first model call may pay. Before the fix this was
	// 5 x 300ms of dialing; the cooldown must cover the rest.
	if max := 2 * hang; total > max {
		t.Errorf("one dead server cost %v across %d model calls, want <= %v: it is being re-dialled instead of skipped by the cooldown",
			total, modelCalls, max)
	}

	// And the status must still report the failure rather than pretending the
	// server is fine.
	if st, ok := m.ServerStatus("deepwiki"); !ok || st.Status != "failed" {
		t.Errorf("status: %+v ok=%v, want failed", st, ok)
	}
}
