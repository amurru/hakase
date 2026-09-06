// Package websearch implements the built-in keyless web research fallback:
// DuckDuckGo HTML search with a Wikipedia API supplement, and a URL fetch
// returning page content as markdown. The agent exposes these tools only
// while no research-capable MCP server is connected
// (see internal/agent/websearch_fallback.go).
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	htmlescape "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/vision"

	"golang.org/x/net/html"
)

const (
	defaultDDGSearchURL  = "https://html.duckduckgo.com/html/"
	defaultWikiAPIURL    = "https://en.wikipedia.org/w/api.php"
	defaultJinaReaderURL = "https://r.jina.ai/"
	defaultUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) HakaseAgent/1.0"
	wikiUserAgent        = "hakase-agent/1.0 (keyless fallback research tool)"
	maxFetchBytes        = 256 << 10 // cap on reader markdown
	maxRawFetchBytes     = 512 << 10 // cap on raw HTML before stripping
	defaultSearchResults = 8
	maxSearchResults     = 10
	wikiSupplementCount  = 2
)

// Result is one search hit. Source names the provider ("duckduckgo" or
// "wikipedia") so the model can weigh general vs encyclopedic results.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Source  string `json:"source"`
}

// Providers holds the keyless search/fetch backends. Base URLs are fields so
// tests can point them at httptest servers; hostCheck guards user-supplied
// URLs in FetchPage and is overridable for the same reason.
type Providers struct {
	client        *http.Client
	DDGSearchURL  string
	WikiAPIURL    string
	JinaReaderURL string
	hostCheck     func(host string) error
}

// NewProviders returns providers wired to the production endpoints.
func NewProviders() *Providers {
	p := &Providers{
		DDGSearchURL:  defaultDDGSearchURL,
		WikiAPIURL:    defaultWikiAPIURL,
		JinaReaderURL: defaultJinaReaderURL,
		hostCheck:     vision.CheckHostPublic,
	}
	p.client = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if err := p.hostCheck(req.URL.Hostname()); err != nil {
				return fmt.Errorf("redirect to non-public host blocked: %w", err)
			}
			return nil
		},
	}
	return p
}

// get issues a GET with the given UA and returns up to limit bytes.
func (p *Providers) get(ctx context.Context, rawURL, ua string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bad request url: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,text/plain,application/json,*/*")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Search runs the query against DuckDuckGo's keyless HTML endpoint and
// appends up to two Wikipedia hits when room remains. When DuckDuckGo fails
// or yields nothing (rate limit, block page), it falls back to Wikipedia.
func (p *Providers) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty search query")
	}
	if maxResults <= 0 {
		maxResults = defaultSearchResults
	}
	if maxResults > maxSearchResults {
		maxResults = maxSearchResults
	}
	results, ddgErr := p.searchDDG(ctx, query, maxResults)
	if ddgErr != nil || len(results) == 0 {
		wiki, wikiErr := p.searchWikipedia(ctx, query, maxResults)
		if wikiErr != nil {
			if ddgErr != nil {
				return nil, fmt.Errorf("duckduckgo: %v; wikipedia: %v", ddgErr, wikiErr)
			}
			return nil, fmt.Errorf("no duckduckgo results; wikipedia: %v", wikiErr)
		}
		return wiki, nil
	}
	if len(results) < maxResults {
		if wiki, err := p.searchWikipedia(ctx, query, wikiSupplementCount); err == nil {
			results = append(results, wiki...)
			if len(results) > maxResults {
				results = results[:maxResults]
			}
		}
	}
	return results, nil
}

func (p *Providers) searchDDG(ctx context.Context, query string, maxResults int) ([]Result, error) {
	body, err := p.get(ctx, p.DDGSearchURL+"?q="+url.QueryEscape(query), defaultUserAgent, maxRawFetchBytes)
	if err != nil {
		return nil, err
	}
	return parseDDGHTML(string(body), maxResults)
}

type wikiSearchResponse struct {
	Query struct {
		Search []struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"` // HTML fragment
		} `json:"search"`
	} `json:"query"`
}

func (p *Providers) searchWikipedia(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 {
		maxResults = 3
	}
	q := url.Values{}
	q.Set("action", "query")
	q.Set("list", "search")
	q.Set("srsearch", query)
	q.Set("srlimit", strconv.Itoa(maxResults))
	q.Set("format", "json")
	q.Set("utf8", "1")
	body, err := p.get(ctx, p.WikiAPIURL+"?"+q.Encode(), wikiUserAgent, maxRawFetchBytes)
	if err != nil {
		return nil, err
	}
	var parsed wikiSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse wikipedia response: %w", err)
	}
	results := make([]Result, 0, len(parsed.Query.Search))
	for _, hit := range parsed.Query.Search {
		results = append(results, Result{
			Title:   hit.Title,
			URL:     wikiArticleURL(p.WikiAPIURL, hit.Title),
			Snippet: stripHTMLFragment(hit.Snippet),
			Source:  "wikipedia",
		})
	}
	return results, nil
}

// wikiArticleURL builds the article URL from a title, deriving the wiki base
// from the configured API URL so httptest servers round-trip.
func wikiArticleURL(apiURL, title string) string {
	base := apiURL
	if i := strings.Index(base, "/w/"); i >= 0 {
		base = base[:i]
	}
	u := url.URL{Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
	return base + u.EscapedPath()
}

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	blockRe  = regexp.MustCompile(`(?i)<(?:br|/p|/div|/h[1-6]|/li|/ul|/ol|/tr|/table|/blockquote)[^>]*>`)

	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// FetchPage returns the content of rawURL as markdown. Primary path is the
// keyless Jina Reader; if it refuses (rate limit, block), the page is
// fetched directly and tags are stripped. User-supplied URLs must pass the
// public-host guard before any request.
func (p *Providers) FetchPage(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid url %q", rawURL)
	}
	if p.hostCheck != nil {
		if err := p.hostCheck(u.Host); err != nil {
			return "", fmt.Errorf("blocked non-public host: %w", err)
		}
	}
	md, jinaErr := p.fetchJina(ctx, rawURL)
	if jinaErr == nil && strings.TrimSpace(md) != "" {
		return md, nil
	}
	page, directErr := p.fetchDirect(ctx, rawURL)
	if directErr != nil {
		return "", fmt.Errorf("reader: %v; direct: %v", jinaErr, directErr)
	}
	return page, nil
}

func (p *Providers) fetchJina(ctx context.Context, rawURL string) (string, error) {
	body, err := p.get(ctx, p.JinaReaderURL+rawURL, defaultUserAgent, maxFetchBytes)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (p *Providers) fetchDirect(ctx context.Context, rawURL string) (string, error) {
	body, err := p.get(ctx, rawURL, defaultUserAgent, maxRawFetchBytes)
	if err != nil {
		return "", err
	}
	return stripHTMLPage(string(body)), nil
}

// stripHTMLPage converts a full HTML document to readable text: script/style
// dropped, block boundaries become newlines, other tags removed.
func stripHTMLPage(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = htmlescape.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(multiNewlineRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// stripHTMLFragment converts a small HTML snippet to single-spaced text.
func stripHTMLFragment(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, "")
	s = htmlescape.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// parseDDGHTML extracts results from the html.duckduckgo.com layout:
// result__a anchors carry title + a //duckduckgo.com/l/?uddg= redirect href;
// the following result__snippet anchor carries the snippet.
func parseDDGHTML(body string, maxResults int) ([]Result, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse duckduckgo html: %w", err)
	}
	var results []Result
	current := -1 // index into results of the result__a anchor last seen
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			switch {
			case hasClass(n, "result__a"):
				link, err := unwrapDDGHref(attrOf(n, "href"))
				if err == nil {
					results = append(results, Result{
						Title:  strings.TrimSpace(nodeText(n)),
						URL:    link,
						Source: "duckduckgo",
					})
					current = len(results) - 1
				} else {
					current = -1
				}
			case hasClass(n, "result__snippet"):
				if current >= 0 && results[current].Snippet == "" {
					results[current].Snippet = strings.TrimSpace(nodeText(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(results) == 0 {
		return nil, errors.New("no results parsed from duckduckgo html")
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results, nil
}

// unwrapDDGHref resolves the //duckduckgo.com/l/?uddg=<encoded> redirect
// wrapper to the real target. Plain http(s) hrefs pass through unchanged.
func unwrapDDGHref(href string) (string, error) {
	if href == "" {
		return "", errors.New("empty href")
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	if enc := u.Query().Get("uddg"); enc != "" {
		return url.QueryUnescape(enc)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.String(), nil
	}
	return "", fmt.Errorf("unsupported href %q", href)
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, f := range strings.Fields(a.Val) {
			if f == want {
				return true
			}
		}
	}
	return false
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// nodeText concatenates the text content of a subtree.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
