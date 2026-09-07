package agent

import (
	"context"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// fakeManagerToolset stands in for the MCP manager: Tools returns canned
// tools (or an error) and ignores ctx, so tests pass nil.
type fakeManagerToolset struct {
	tools []tool.Tool
	err   error
}

func (f *fakeManagerToolset) Name() string { return "fake_mcp" }

func (f *fakeManagerToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return f.tools, f.err
}

func fallbackTestTool(name string) tool.Tool {
	t, err := util.NewDocTool(functiontool.Config{Name: name, Description: "test"}, func(ctx adkagent.Context, in struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	if err != nil {
		panic(err)
	}
	return t
}

func newTestFallback(t *testing.T, force bool, mcp tool.Toolset) *fallbackSearchToolset {
	t.Helper()
	cfg := &config.Config{WebSearch: config.WebSearchConfig{Force: force}}
	ts := newFallbackSearchToolset(cfg, mcp, nil)
	if ts == nil {
		t.Fatal("fallback toolset unexpectedly nil")
	}
	return ts
}

func TestHasResearchMCP(t *testing.T) {
	cases := []struct {
		names []string
		want  bool
	}{
		{nil, false},
		{[]string{"mcp_fs_read_file", "mcp_git_status"}, false},
		{[]string{"mcp_github_search_repositories"}, false}, // repo search is not web research
		{[]string{"mcp_lightpanda_search"}, true},
		{[]string{"mcp_lightpanda_markdown"}, true},
		{[]string{"mcp_browser_navigate"}, true},
		{[]string{"mcp_browser_take_screenshot"}, true},
		{[]string{"mcp_fetch_fetch"}, true},
		{[]string{"mcp_tavily_web_search"}, true},
	}
	for _, tc := range cases {
		if got := hasResearchMCP(tc.names); got != tc.want {
			t.Errorf("hasResearchMCP(%v) = %v, want %v", tc.names, got, tc.want)
		}
	}
}

func TestFallbackHiddenWhenResearchMCPConnected(t *testing.T) {
	mgr := &fakeManagerToolset{tools: []tool.Tool{
		fallbackTestTool("mcp_lightpanda_search"),
		fallbackTestTool("mcp_lightpanda_markdown"),
	}}
	got, err := newTestFallback(t, false, mgr).Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("fallback should hide, got %d tools", len(got))
	}
}

func TestFallbackShownWhenOnlyUnrelatedMCP(t *testing.T) {
	mgr := &fakeManagerToolset{tools: []tool.Tool{fallbackTestTool("mcp_fs_read_file")}}
	got, err := newTestFallback(t, false, mgr).Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name() != "web_search" || got[1].Name() != "web_fetch" {
		t.Fatalf("expected web_search+web_fetch, got %+v", got)
	}
}

func TestFallbackShownWithoutMCPAndOnError(t *testing.T) {
	for _, mgr := range []tool.Toolset{nil, &fakeManagerToolset{err: context.DeadlineExceeded}} {
		got, err := newTestFallback(t, false, mgr).Tools(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("manager %T: expected fallback shown, got %d", mgr, len(got))
		}
	}
}

func TestFallbackForceOverridesDetection(t *testing.T) {
	mgr := &fakeManagerToolset{tools: []tool.Tool{fallbackTestTool("mcp_lightpanda_search")}}
	got, err := newTestFallback(t, true, mgr).Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("force should expose fallback, got %d", len(got))
	}
}

func TestFallbackDisabledByConfig(t *testing.T) {
	off := false
	cfg := &config.Config{WebSearch: config.WebSearchConfig{Enabled: &off}}
	if ts := newFallbackSearchToolset(cfg, nil, nil); ts != nil {
		t.Fatal("enabled=false must yield nil toolset")
	}
	if !config.WebSearchEnabled(&config.Config{}) {
		t.Fatal("default must be enabled")
	}
}

func TestBuildWebSearchFallbackPrompt(t *testing.T) {
	if s := buildWebSearchFallback(&config.Config{}); s == "" {
		t.Error("default config should produce a prompt section")
	}
	off := false
	if s := buildWebSearchFallback(&config.Config{WebSearch: config.WebSearchConfig{Enabled: &off}}); s != "" {
		t.Errorf("disabled config should produce empty section, got %q", s)
	}
}
