// websearch_fallback.go - built-in keyless web research fallback.
//
// hakase's research surface normally comes from browser MCP servers
// (lightpanda, chrome-devtools, playwright - docs/browser-mcp-presets.md).
// With none connected the agent would have no web access at all, so the
// internal/websearch tools are exposed behind a dynamic toolset: ADK
// re-evaluates Tools() before every model call, so the fallback appears and
// disappears with the MCP connection state.
package agent

import (
	"fmt"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/websearch"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// researchToolHints are substrings matched against namespaced MCP tool names
// (mcp_<server>_<tool>, lowercased). Any match means a connected server
// already covers web research or browser automation, so the keyless fallback
// stays hidden. Covers the documented browser presets (named "browser" or
// "lightpanda", exposing navigate/screenshot/markdown tools) and the common
// search-API servers. Deliberately no bare "search": mcp_github_search_*
// would false-positive.
var researchToolHints = []string{
	"browser", "browse", "navigate", "screenshot", "markdown",
	"web_search", "web_fetch", "fetch", "open_url", "read_url",
	"duckduckgo", "google", "tavily", "brave", "searx",
	"lightpanda", "playwright", "puppeteer", "chrom",
}

// hasResearchMCP reports whether any tool name carries a research hint.
func hasResearchMCP(names []string) bool {
	for _, n := range names {
		ln := strings.ToLower(n)
		for _, h := range researchToolHints {
			if strings.Contains(ln, h) {
				return true
			}
		}
	}
	return false
}

// fallbackSearchToolset implements tool.Toolset for the built-in web search
// fallback.
type fallbackSearchToolset struct {
	mcp   tool.Toolset // MCP manager; nil when no MCP config exists
	tools []tool.Tool  // built-in web_search/web_fetch
	force bool         // web_search.force: expose regardless of MCP state
}

// newFallbackSearchToolset builds the shared fallback toolset, or nil when
// the feature is disabled by config or the tools failed to build.
func newFallbackSearchToolset(cfg *config.Config, mcpManager tool.Toolset, log interfaces.LogFunc) *fallbackSearchToolset {
	if !config.WebSearchEnabled(cfg) {
		return nil
	}
	tools, err := websearch.NewTools(websearch.NewProviders())
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("websearch: fallback tools unavailable: %v", err))
		}
		return nil
	}
	return &fallbackSearchToolset{
		mcp:   mcpManager,
		tools: tools,
		force: cfg != nil && cfg.WebSearch.Force,
	}
}

// Name implements tool.Toolset.
func (f *fallbackSearchToolset) Name() string { return "web_search_fallback" }

// Description implements the extended toolset interface.
func (f *fallbackSearchToolset) Description() string {
	return "Built-in keyless web search and page fetch, active only while no research-capable MCP tools are connected."
}

// IsLongRunning implements the extended toolset interface.
func (f *fallbackSearchToolset) IsLongRunning() bool { return false }

// Tools implements tool.Toolset: the fallback tools appear only when no
// connected MCP tool looks research-capable (or when forced). A manager
// probe error counts as "no research coverage" - a broken browser stack must
// not take search away too.
func (f *fallbackSearchToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	if !f.force && hasResearchMCP(f.mcpToolNames(ctx)) {
		return nil, nil
	}
	return f.tools, nil
}

// mcpToolNames lists current MCP tool names; nil manager or any probe error
// yields nil (treated as no research coverage).
func (f *fallbackSearchToolset) mcpToolNames(ctx adkagent.ReadonlyContext) []string {
	if f.mcp == nil {
		return nil
	}
	tools, err := f.mcp.Tools(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}

// webSearchFallback is the shared instance installed during SetupRunner and
// consulted by BuildSubAgentTools for delegated runs spawned after setup.
var webSearchFallback *fallbackSearchToolset

// buildWebSearchFallback returns the prompt section describing the fallback
// tools, or "" when the feature is disabled. Appended to the orchestrator
// and web_researcher instructions.
func buildWebSearchFallback(cfg *config.Config) string {
	if !config.WebSearchEnabled(cfg) {
		return ""
	}
	return `### WEB SEARCH FALLBACK:
You have built-in keyless research tools: 'web_search' (DuckDuckGo with Wikipedia supplements; returns title/url/snippet per result, labeled by source) and 'web_fetch' (returns a URL's content as markdown; no JavaScript rendering or logins). Use them for quick lookups and reading pages, and cite the source URLs in research answers. These tools are hidden automatically whenever a browser or search MCP server is connected - in that case prefer the richer MCP browsing tools instead.`
}
