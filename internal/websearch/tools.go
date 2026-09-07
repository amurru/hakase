package websearch

import (
	"fmt"

	"amurru/hakase/internal/util"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const searchDescription = `Searches the public web without any API keys: primary results come from DuckDuckGo, supplemented with Wikipedia matches (each result is labeled with its source). Use for current facts, documentation lookups, and news. Returns title, url, and snippet per result; follow up with web_fetch to read a promising page.`

const fetchDescription = `Fetches a public web page and returns its main content as markdown. No JavaScript rendering or login support. Use after web_search to read a result, or to pull a reference page when given a URL.`

// SearchToolInput is the web_search argument schema.
type SearchToolInput struct {
	Query      string `json:"query" doc:"Search query text."`
	MaxResults int    `json:"max_results,omitempty" doc:"Maximum results to return, 1-10 (default 8)."`
}

// SearchResultOutput is the web_search return payload.
type SearchResultOutput struct {
	Query   string   `json:"query"`
	Results []Result `json:"results"`
}

// FetchToolInput is the web_fetch argument schema.
type FetchToolInput struct {
	URL string `json:"url" doc:"Absolute http(s) URL of the page to fetch."`
}

// FetchOutput is the web_fetch return payload.
type FetchOutput struct {
	URL     string `json:"url"`
	Content string `json:"content"`
}

// NewTools builds the web_search and web_fetch fallback tools backed by p.
func NewTools(p *Providers) ([]tool.Tool, error) {
	searchTool, err := util.NewDocTool(functiontool.Config{
		Name:        "web_search",
		Description: searchDescription,
	}, func(ctx adkagent.Context, in SearchToolInput) (SearchResultOutput, error) {
		results, err := p.Search(ctx, in.Query, in.MaxResults)
		if err != nil {
			return SearchResultOutput{}, err
		}
		return SearchResultOutput{Query: in.Query, Results: results}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("web_search: %w", err)
	}
	fetchTool, err := util.NewDocTool(functiontool.Config{
		Name:        "web_fetch",
		Description: fetchDescription,
	}, func(ctx adkagent.Context, in FetchToolInput) (FetchOutput, error) {
		content, err := p.FetchPage(ctx, in.URL)
		if err != nil {
			return FetchOutput{}, err
		}
		return FetchOutput{URL: in.URL, Content: content}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("web_fetch: %w", err)
	}
	return []tool.Tool{searchTool, fetchTool}, nil
}
