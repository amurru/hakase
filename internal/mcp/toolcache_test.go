package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/config"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// countingToolset records how many times Tools() is invoked, so the tool-list
// cache can be shown to actually take effect.
type countingToolset struct {
	calls atomic.Int64
	tools []tool.Tool
	err   error
}

func (c *countingToolset) Name() string { return "counting" }

func (c *countingToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	c.calls.Add(1)
	return c.tools, c.err
}

func TestManagedServerCachesToolList(t *testing.T) {
	ts := &countingToolset{tools: []tool.Tool{}}
	ms := &managedServer{name: "s", toolset: ts, status: "idle"}

	now := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := ms.toolsCached(mcpTestCtx{}, now); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := ts.calls.Load(); got != 1 {
		t.Fatalf("expected 1 SDK call across 5 lookups within the TTL, got %d", got)
	}

	// Past the TTL it must re-list, or a restarted server's new tools would
	// never appear.
	if _, err := ms.toolsCached(mcpTestCtx{}, now.Add(toolListTTL+time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 2 {
		t.Fatalf("expected a re-list after the TTL, got %d total calls", got)
	}
}

func TestManagedServerDoesNotCacheToolErrors(t *testing.T) {
	boom := errors.New("dial refused")
	ts := &countingToolset{err: boom}
	ms := &managedServer{name: "s", toolset: ts, status: "idle"}

	now := time.Now()
	for i := 0; i < 3; i++ {
		_, err := ms.toolsCached(mcpTestCtx{}, now)
		if !errors.Is(err, boom) {
			t.Fatalf("call %d: got %v, want the server error", i, err)
		}
	}
	// Errors must NOT be cached. The cooldown gate - not this cache - paces
	// retries, and a cached error would keep the server in "failed" after it
	// recovered, because every call would re-arm the cooldown on stale data.
	if got := ts.calls.Load(); got != 3 {
		t.Fatalf("expected each errored lookup to re-probe (3 SDK calls), got %d", got)
	}
}

// TestManagedServerDropsCachedListOnFailure is the other half: a server that
// goes bad must stop serving its last good tool list.
func TestManagedServerDropsCachedListOnFailure(t *testing.T) {
	ts := &countingToolset{tools: []tool.Tool{}}
	ms := &managedServer{name: "s", toolset: ts, status: "idle"}

	now := time.Now()
	got, err := ms.toolsCached(mcpTestCtx{}, now)
	if err != nil || len(got) != 0 {
		t.Fatalf("priming the cache: %v, %d tools", err, len(got))
	}

	ts.err = errors.New("went away")
	if _, err := ms.toolsCached(mcpTestCtx{}, now.Add(toolListTTL+time.Second)); err == nil {
		t.Fatal("expected the re-list to surface the new failure")
	}

	// Back within the TTL, the stale list must not be served.
	ts.err = nil
	got, err = ms.toolsCached(mcpTestCtx{}, now.Add(toolListTTL+2*time.Second))
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("served a stale tool list after a failure: %d tools", len(got))
	}
}

func TestManagedServerInvalidateToolCacheForcesRefresh(t *testing.T) {
	ts := &countingToolset{tools: []tool.Tool{}}
	ms := &managedServer{name: "s", toolset: ts, status: "idle"}

	now := time.Now()
	if _, err := ms.toolsCached(mcpTestCtx{}, now); err != nil {
		t.Fatal(err)
	}
	ms.invalidateToolCache()
	if _, err := ms.toolsCached(mcpTestCtx{}, now); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 2 {
		t.Fatalf("invalidate must force a re-list, got %d SDK calls", got)
	}
}

// TestReconnectInvalidatesToolCache pins the user-facing contract: a manual
// reconnect exists to re-run connect and tools/list, so it must not be
// satisfied from the cache.
func TestReconnectInvalidatesToolCache(t *testing.T) {
	m, err := NewMCPServerManager(&config.Config{}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}
	ts := &countingToolset{tools: []tool.Tool{}}
	ms := &managedServer{name: "s", cfg: &config.MCPServerConfig{}, toolset: ts, status: "idle"}
	m.servers = map[string]*managedServer{"s": ms}

	now := time.Now()
	if _, err := ms.toolsCached(mcpTestCtx{}, now); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconnect("s"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if _, err := ms.toolsCached(mcpTestCtx{}, now); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 2 {
		t.Fatalf("Reconnect did not force a re-list: %d SDK calls", got)
	}
}

// TestToolsListIsBoundedRegardlessOfSDKRetries pins the total dial bound.
//
// timeout_ms is a per-request timeout, but the go-sdk retries a failed
// initialize up to 5 times, so the real cost of a dead server was
// 5 x timeout_ms on the critical path of every model call. One trace showed
// 77s, twice in one turn. The whole SDK call is now wrapped in a deadline so
// the retry loop cannot outlive the budget.
func TestToolsListIsBoundedRegardlessOfSDKRetries(t *testing.T) {
	mcpTestIsolate(t)

	orig := defaultToolsListBudget
	defaultToolsListBudget = 150 * time.Millisecond
	t.Cleanup(func() { defaultToolsListBudget = orig })

	// Hangs forever. TimeoutMs is left unset so the per-request timeout is the
	// 10s default and the total budget is the 150ms under test: a single
	// un-bounded attempt would already be ~66x over budget, and the SDK
	// retries 5 times, so the un-bounded cost would be minutes.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	m, err := NewMCPServerManager(&config.Config{MCPServers: config.MCPConfig{
		Servers: map[string]*config.MCPServerConfig{
			"black-hole": {Type: "http", URL: srv.URL},
		}}}, func(string) {})
	if err != nil {
		t.Fatalf("NewMCPServerManager: %v", err)
	}

	start := time.Now()
	if _, err := m.Tools(mcpTestCtx{}); err != nil {
		t.Fatalf("a dead server must be skipped, not surfaced as an error: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 3*defaultToolsListBudget {
		t.Errorf("listing a black-hole server took %v, over the %v budget (plus slack); the SDK retry loop is not being bounded",
			elapsed, defaultToolsListBudget)
	}
}

// TestToolsListBudgetHonoursExplicitTimeout: a server configured with a
// deliberately long timeout_ms must not be truncated by the default budget.
func TestToolsListBudgetHonoursExplicitTimeout(t *testing.T) {
	cases := []struct {
		name        string
		timeoutMs   int
		wantAtLeast time.Duration
		wantExact   bool
	}{
		{"no explicit timeout uses the default", 0, defaultToolsListBudget, true},
		{"small explicit timeout does not shrink the default", 500, defaultToolsListBudget, true},
		{"large explicit timeout is honoured", 30000, 32 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := &managedServer{cfg: &config.MCPServerConfig{TimeoutMs: tc.timeoutMs}}
			got := ms.toolsListBudget()
			if tc.wantExact && got != tc.wantAtLeast {
				t.Fatalf("budget = %v, want exactly %v", got, tc.wantAtLeast)
			}
			if !tc.wantExact && got < tc.wantAtLeast {
				t.Errorf("budget = %v, want at least %v", got, tc.wantAtLeast)
			}
		})
	}
}
