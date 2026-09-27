package mcp

import (
	"errors"
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
		if _, err := ms.toolsCached(nil, now); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := ts.calls.Load(); got != 1 {
		t.Fatalf("expected 1 SDK call across 5 lookups within the TTL, got %d", got)
	}

	// Past the TTL it must re-list, or a restarted server's new tools would
	// never appear.
	if _, err := ms.toolsCached(nil, now.Add(toolListTTL+time.Second)); err != nil {
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
		_, err := ms.toolsCached(nil, now)
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
	got, err := ms.toolsCached(nil, now)
	if err != nil || len(got) != 0 {
		t.Fatalf("priming the cache: %v, %d tools", err, len(got))
	}

	ts.err = errors.New("went away")
	if _, err := ms.toolsCached(nil, now.Add(toolListTTL+time.Second)); err == nil {
		t.Fatal("expected the re-list to surface the new failure")
	}

	// Back within the TTL, the stale list must not be served.
	ts.err = nil
	got, err = ms.toolsCached(nil, now.Add(toolListTTL+2*time.Second))
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
	if _, err := ms.toolsCached(nil, now); err != nil {
		t.Fatal(err)
	}
	ms.invalidateToolCache()
	if _, err := ms.toolsCached(nil, now); err != nil {
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
	if _, err := ms.toolsCached(nil, now); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconnect("s"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if _, err := ms.toolsCached(nil, now); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 2 {
		t.Fatalf("Reconnect did not force a re-list: %d SDK calls", got)
	}
}
