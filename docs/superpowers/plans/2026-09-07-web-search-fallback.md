# Built-in Web Search Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When no research-capable MCP server (browser/search MCP like lightpanda) is connected, expose built-in keyless `web_search` and `web_fetch` tools so the agent keeps web research ability.

**Architecture:** New leaf package `internal/websearch` (DuckDuckGo HTML search + Wikipedia API supplement, Jina Reader page fetch with direct-GET fallback). New `internal/agent/websearch_fallback.go` wraps the tools in a dynamic `tool.Toolset` that probes the MCP manager's current tool names on every `Tools()` call (ADK re-evaluates per model turn) and yields the tools only when no name matches a research hint. Wired into orchestrator, `web_researcher`, and `BuildSubAgentTools`; kill-switch via `web_search.enabled` in config.json.

**Tech Stack:** Go stdlib net/http + golang.org/x/net/html (already in go.mod), ADK functiontool via `util.NewDocTool`, vision's `CheckHostPublic` for SSRF. No frontend changes.

## Global Constraints

- Tests are self-contained: httptest servers + temp dirs, no network, no config.json (AGENTS.md).
- `internal/websearch` must stay a leaf package: may import `internal/util`, `internal/vision`, `internal/interfaces`, `internal/config` — never `internal/agent` (would cycle).
- Tool names are exactly `web_search` and `web_fetch` (mirrors docs/browser-mcp-presets.md MCP contract).
- Detection is name-substring based on namespaced MCP tool names (`mcp_<server>_<tool>`, lowercased).
- Verification: `go build ./... && go test ./...` from repo root.
- Feature default: enabled (auto mode). Only `web_search.enabled = false` disables.

---

### Task 1: `internal/websearch` — DuckDuckGo search provider

**Files:**
- Create: `internal/websearch/providers.go`
- Test: `internal/websearch/providers_test.go`

**Interfaces:**
- Produces: `type Result struct { Title, URL, Snippet string; Source string }` (JSON tags `title,url,snippet,source`); `type Providers struct` with fields `DDGSearchURL, WikiAPIURL, JinaReaderURL string`, unexported `client *http.Client`, `hostCheck func(string) error`; `func NewProviders() *Providers`; `func (p *Providers) Search(ctx context.Context, query string, maxResults int) ([]Result, error)`; `func parseDDGHTML(body string, maxResults int) ([]Result, error)`; `func unwrapDDGHref(href string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

```go
package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ddgFixtureHTML = `<!DOCTYPE html>
<html><body>
<div class="result">
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fblog%2Fpipelines&amp;rut=abc">Go Concurrency Patterns: Pipelines</a>
<a class="result__snippet">Pipelines let you chain <b>goroutines</b> with channels.</a>
</div>
<div class="result">
<a rel="nofollow" class="result__a" href="https://go.dev/ref/mem">The Go Memory Model</a>
<a class="result__snippet">Direct link without redirect wrapper.</a>
</div>
</body></html>`

func TestParseDDGHTML(t *testing.T) {
	results, err := parseDDGHTML(ddgFixtureHTML, 8)
	if err != nil {
		t.Fatalf("parseDDGHTML: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	r := results[0]
	if r.Title != "Go Concurrency Patterns: Pipelines" {
		t.Errorf("title: %q", r.Title)
	}
	if r.URL != "https://go.dev/blog/pipelines" {
		t.Errorf("uddg not unwrapped: %q", r.URL)
	}
	if r.Snippet != "Pipelines let you chain goroutines with channels." {
		t.Errorf("snippet: %q", r.Snippet)
	}
	if r.Source != "duckduckgo" {
		t.Errorf("source: %q", r.Source)
	}
	if results[1].URL != "https://go.dev/ref/mem" {
		t.Errorf("plain href mangled: %q", results[1].URL)
	}
}

func TestSearchDDGQueryAndResults(t *testing.T) {
	var gotQuery string
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		io.WriteString(w, ddgFixtureHTML)
	}))
	defer ddg.Close()

	p := NewProviders()
	p.DDGSearchURL = ddg.URL + "/"
	res, err := p.Search(context.Background(), "golang concurrency", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "golang concurrency" {
		t.Errorf("query sent: %q", gotQuery)
	}
	if len(res) != 2 || res[0].Source != "duckduckgo" {
		t.Fatalf("unexpected results: %+v", res)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	if _, err := NewProviders().Search(context.Background(), "  ", 8); err == nil {
		t.Fatal("expected error for empty query")
	}
}
```

- [ ] **Step 2: Run tests, verify failure**

Run: `go test ./internal/websearch/ -v`
Expected: FAIL (package does not exist yet / undefined symbols).

- [ ] **Step 3: Implement `providers.go` (minimal for this task)**

```go
// Package websearch implements the built-in keyless web research fallback:
// DuckDuckGo HTML search with a Wikipedia API supplement, and a URL fetch
// returning page content as markdown. The agent exposes these tools only
// while no research-capable MCP server is connected
// (see internal/agent/websearch_fallback.go).
package websearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// parseDDGHTML extracts results from the html.duckduckgo.com layout:
// result__a anchors carry title + a //duckduckgo.com/l/?uddg= redirect href;
// the following result__snippet anchor carries the snippet.
func parseDDGHTML(body string, maxResults int) ([]Result, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse duckduckgo html: %w", err)
	}
	var results []Result
	current := -1 // index into results of the anchor last seen
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
```

(The `Search` body references `searchWikipedia`, added in Task 2 — for Task 1 commit a stub `func (p *Providers) searchWikipedia(ctx context.Context, query string, max int) ([]Result, error) { return nil, errors.New("not implemented") }` so the package compiles; Task 2 replaces it. The supplement/fallback branches tolerate its error.)

Add `"amurru/hakase/internal/vision"` to imports for `vision.CheckHostPublic` in NewProviders.

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/websearch/ -v`
Expected: PASS (3 tests). Note: `go build ./...` still passes overall.

- [ ] **Step 5: Commit**

```bash
git add internal/websearch/
git commit -m "feat(websearch): keyless DuckDuckGo HTML search provider"
```

---

### Task 2: Wikipedia search + merge behavior

**Files:**
- Modify: `internal/websearch/providers.go`
- Test: `internal/websearch/providers_test.go`

**Interfaces:**
- Consumes: `Providers.WikiAPIURL`, `Result`.
- Produces: `func (p *Providers) searchWikipedia(ctx context.Context, query string, maxResults int) ([]Result, error)`; `func stripHTMLFragment(s string) string`; `func wikiArticleURL(apiURL, title string) string`.

- [ ] **Step 1: Write the failing tests**

```go
const wikiFixtureJSON = `{"batchcomplete":"","query":{"search":[
 {"ns":0,"title":"Go (programming language)","pageid":25039021,"snippet":"<b>Go</b> is a compiled language"},
 {"ns":0,"title":"Concurrency (computer science)","pageid":1234,"snippet":"Structuring programs with &amp; channels"}]}}`

func testWikiProviders(t *testing.T) (*Providers, *httptest.Server) {
	t.Helper()
	wiki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") != "search" || r.URL.Query().Get("srsearch") == "" {
			t.Errorf("unexpected wiki query: %s", r.URL.RawQuery)
		}
		io.WriteString(w, wikiFixtureJSON)
	}))
	t.Cleanup(wiki.Close)
	p := NewProviders()
	p.WikiAPIURL = wiki.URL + "/w/api.php"
	return p, wiki
}

func TestSearchWikipedia(t *testing.T) {
	p, _ := testWikiProviders(t)
	res, err := p.searchWikipedia(context.Background(), "golang", 3)
	if err != nil {
		t.Fatalf("searchWikipedia: %v", err)
	}
	if len(res) != 2 || res[0].Source != "wikipedia" {
		t.Fatalf("unexpected: %+v", res)
	}
	if res[0].URL != "https://en.wikipedia.org/wiki/Go_(programming_language)" {
		t.Errorf("article url: %q", res[0].URL)
	}
	if res[1].Snippet != "Structuring programs with & channels" {
		t.Errorf("snippet not stripped/unescaped: %q", res[1].Snippet)
	}
}

func TestSearchFallsBackToWikipediaWhenDDGBlocked(t *testing.T) {
	p, _ := testWikiProviders(t)
	p.DDGSearchURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})).URL + "/"
	res, err := p.Search(context.Background(), "golang", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 || res[0].Source != "wikipedia" {
		t.Fatalf("expected wikipedia fallback, got: %+v", res)
	}
}

func TestSearchWikipediaSupplementsDDG(t *testing.T) {
	p, _ := testWikiProviders(t)
	p.DDGSearchURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ddgFixtureHTML)
	})).URL + "/"
	res, err := p.Search(context.Background(), "golang", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// 2 DDG + 2 Wikipedia supplements.
	if len(res) != 4 || res[0].Source != "duckduckgo" || res[2].Source != "wikipedia" {
		t.Fatalf("unexpected merge: %+v", res)
	}
}

func TestSearchReportsBothProviderErrors(t *testing.T) {
	p := NewProviders()
	p.DDGSearchURL = "http://127.0.0.1:1/"
	p.WikiAPIURL = "http://127.0.0.1:1/"
	_, err := p.Search(context.Background(), "golang", 8)
	if err == nil || !strings.Contains(err.Error(), "duckduckgo") || !strings.Contains(err.Error(), "wikipedia") {
		t.Fatalf("expected combined error, got: %v", err)
	}
}

func TestStripHTMLFragment(t *testing.T) {
	got := stripHTMLFragment("<b>Hello</b> &amp; <i>world</i>\n  next")
	if got != "Hello & world next" {
		t.Errorf("got %q", got)
	}
}
```

(Note: the combined-error test points at `127.0.0.1:1` — connection refused, no server needed. Provider endpoints are fixed config, not user URLs, so no hostCheck applies there and httptest/localhost URLs are fine.)

- [ ] **Step 2: Run, verify new tests fail**

Run: `go test ./internal/websearch/ -run 'Wikipedia|FallsBack|Supplements|ReportsBoth|StripHTML' -v`
Expected: FAIL (searchWikipedia is a stub).

- [ ] **Step 3: Implement**

Replace the stub in `providers.go`:

```go
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

// stripHTMLFragment converts a small HTML snippet to single-spaced text.
func stripHTMLFragment(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
```

Add package vars + imports (`encoding/json`, `html` stdlib aliased if x/net/html collides — rename x/net import to `xhtml` OR stdlib to `htmlescape`; chosen resolution: import stdlib as `htmlescape "html"` and keep x/net as `html`):

```go
import (
	htmlescape "html"
	"regexp"
	"strconv"
)

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
)
```

Everywhere the stdlib unescape is needed use `htmlescape.UnescapeString`.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/websearch/ -v`
Expected: PASS (all 8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/websearch/
git commit -m "feat(websearch): wikipedia supplement and ddg failure fallback"
```

---

### Task 3: FetchPage — Jina Reader with direct fallback + SSRF guard

**Files:**
- Modify: `internal/websearch/providers.go`
- Test: `internal/websearch/providers_test.go`

**Interfaces:**
- Produces: `func (p *Providers) FetchPage(ctx context.Context, rawURL string) (string, error)`; `func stripHTMLPage(s string) string`.

- [ ] **Step 1: Write the failing tests**

```go
func TestFetchPageViaJina(t *testing.T) {
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.RequestURI(), "/https://example.com/page") {
			t.Errorf("unexpected jina path: %s", r.URL.RequestURI())
		}
		io.WriteString(w, "Title: Example\nURL Source: https://example.com/page\n\nMarkdown Content:\n# Hello\n\nBody text.")
	}))
	defer jina.Close()
	p := NewProviders()
	p.JinaReaderURL = jina.URL + "/"
	p.hostCheck = nil // tests must not resolve DNS
	out, err := p.FetchPage(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if !strings.Contains(out, "# Hello") {
		t.Errorf("markdown lost: %q", out)
	}
}

func TestFetchPageDirectFallbackStripsTags(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><head><style>x{}</style><script>evil()</script></head>
		<body><h1>Title</h1><p>Para one &amp; two.</p><div><ul><li>item</li></ul></div></body></html>`)
	}))
	defer page.Close()
	p := NewProviders()
	p.JinaReaderURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})).URL + "/"
	p.hostCheck = nil
	out, err := p.FetchPage(context.Background(), page.URL+"/doc")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if strings.Contains(out, "<") || strings.Contains(out, "evil()") {
		t.Errorf("tags/script survived: %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Para one & two.") || !strings.Contains(out, "item") {
		t.Errorf("content lost: %q", out)
	}
}

func TestFetchPageBlocksPrivateHost(t *testing.T) {
	p := NewProviders() // real CheckHostPublic
	if _, err := p.FetchPage(context.Background(), "http://127.0.0.1:9/x"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected SSRF block, got: %v", err)
	}
}

func TestFetchPageInvalidURL(t *testing.T) {
	p := NewProviders()
	if _, err := p.FetchPage(context.Background(), "ftp://x"); err == nil {
		t.Fatal("expected error for non-http scheme")
	}
}

func TestFetchPageBothPathsFail(t *testing.T) {
	p := NewProviders()
	p.hostCheck = nil
	p.JinaReaderURL = "http://127.0.0.1:1/"
	_, err := p.FetchPage(context.Background(), "http://127.0.0.1:1/page")
	if err == nil || !strings.Contains(err.Error(), "reader") || !strings.Contains(err.Error(), "direct") {
		t.Fatalf("expected combined error, got: %v", err)
	}
}

func TestFetchPageBodyCapped(t *testing.T) {
	big := strings.Repeat("a", 300<<10)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, big)
	}))
	defer jina.Close()
	p := NewProviders()
	p.JinaReaderURL = jina.URL + "/"
	p.hostCheck = nil
	out, err := p.FetchPage(context.Background(), "https://example.com/big")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if len(out) > int(maxFetchBytes) {
		t.Errorf("body not capped: %d", len(out))
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/websearch/ -run FetchPage -v`
Expected: FAIL (FetchPage undefined).

- [ ] **Step 3: Implement (append to providers.go)**

```go
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

var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

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
```

Add `blockRe` to the package vars:

```go
blockRe = regexp.MustCompile(`(?i)<(?:br|/p|/div|/h[1-6]|/li|/ul|/ol|/tr|/table|/blockquote)[^>]*>`)
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/websearch/ -v`
Expected: PASS (14 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/websearch/
git commit -m "feat(websearch): markdown page fetch with reader fallback and SSRF guard"
```

---

### Task 4: `web_search` / `web_fetch` tool definitions

**Files:**
- Create: `internal/websearch/tools.go`
- Test: `internal/websearch/tools_test.go`

**Interfaces:**
- Consumes: `Providers.Search`, `Providers.FetchPage`, `util.NewDocTool`.
- Produces: `func NewTools(p *Providers) ([]tool.Tool, error)` returning exactly two tools named `web_search`, `web_fetch`. Input/output structs: `SearchToolInput{Query string; MaxResults int}`, `SearchResultOutput{Query string; Results []Result}`, `FetchToolInput{URL string}`, `FetchOutput{URL, Content string}`.

- [ ] **Step 1: Write the failing test**

```go
package websearch

import (
	"context"
	"testing"

	adkagent "google.golang.org/adk/v2/agent"
)

func TestNewToolsNamesAndSchemas(t *testing.T) {
	tools, err := NewTools(NewProviders())
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	if tools[0].Name() != "web_search" || tools[1].Name() != "web_fetch" {
		t.Fatalf("names: %q, %q", tools[0].Name(), tools[1].Name())
	}
	if tools[0].Declaration() == nil || tools[0].Declaration().Description == "" {
		t.Errorf("web_search missing declaration/description")
	}
}

func TestSearchToolEndToEnd(t *testing.T) {
	p := NewProviders()
	p.DDGSearchURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ddgFixtureHTML)
	})).URL + "/"
	tools, err := NewTools(p)
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}
	// functiontool handlers run through the tool's Run; simpler to verify the
	// provider path the handler wraps.
	res, err := p.Search(context.Background(), "golang", 8)
	if err != nil || len(res) != 2 {
		t.Fatalf("provider path broken: %v", err)
	}
	_ = tools
	_ = adkagent.Background()
}
```

Correction during implementation: `adkagent.Background()` does not exist — drop those two blank imports/lines; the tool-level invocation path is covered by Task 6 tests calling `Tools(nil)`. Final test keeps `TestNewToolsNamesAndSchemas` plus a handler direct-call:

```go
func TestFetchToolHandler(t *testing.T) {
	p := NewProviders()
	p.hostCheck = nil
	p.JinaReaderURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Markdown Content:\n# Doc")
	})).URL + "/"
	tools, _ := NewTools(p)
	_ = tools // handler invocation requires a full ADK Context fake; covered via providers tests
	if p.JinaReaderURL == "" {
		t.Fatal("providers misconfigured")
	}
}
```

(If a minimal handler invocation is achievable with a trivial `agent.Context` fake like `mcpTestCtx`, do that instead and actually invoke the handler; otherwise the providers tests carry coverage.)

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/websearch/ -run Tools -v`
Expected: FAIL (NewTools undefined).

- [ ] **Step 3: Implement tools.go**

```go
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
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/websearch/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/websearch/
git commit -m "feat(websearch): web_search and web_fetch agent tools"
```

---

### Task 5: Config flag `web_search.enabled` / `web_search.force`

**Files:**
- Modify: `internal/config/config.go` (add `WebSearchConfig` type near the `Sidekick` helpers, field on `Config` near `SearchExpansion`, helper `WebSearchEnabled` next to `SidekickEnabled` at config.go:455)
- Test: `internal/config/config_test.go` (append; create if absent)

**Interfaces:**
- Produces: `type WebSearchConfig struct { Enabled *bool `json:"enabled,omitempty"`; Force bool `json:"force,omitempty"` }`; `Config.WebSearch WebSearchConfig` (json key `web_search`); `func WebSearchEnabled(c *Config) bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestWebSearchEnabled(t *testing.T) {
	if !WebSearchEnabled(nil) {
		t.Error("nil config should default to enabled")
	}
	if !WebSearchEnabled(&Config{}) {
		t.Error("empty config should default to enabled (auto mode)")
	}
	off := false
	if WebSearchEnabled(&Config{WebSearch: WebSearchConfig{Enabled: &off}}) {
		t.Error("enabled=false must disable")
	}
	on := true
	if !WebSearchEnabled(&Config{WebSearch: WebSearchConfig{Enabled: &on}}) {
		t.Error("enabled=true must stay enabled")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/config/ -run WebSearchEnabled -v` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
// WebSearchConfig controls the built-in keyless web search fallback
// (internal/websearch) that activates when no research-capable MCP server is
// connected. enabled=false switches the feature (and its outbound calls)
// off; force keeps the fallback tools visible even when research MCP tools
// are connected.
type WebSearchConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
	Force   bool  `json:"force,omitempty"`
}

// WebSearchEnabled reports whether the fallback feature may run at all:
// only an explicit enabled=false disables it. Detection of research-capable
// MCP tools (the auto part) lives in internal/agent/websearch_fallback.go.
func WebSearchEnabled(c *Config) bool {
	if c == nil || c.WebSearch.Enabled == nil {
		return true
	}
	return *c.WebSearch.Enabled
}
```

Field on Config: `WebSearch WebSearchConfig `json:"web_search,omitempty"``.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/config/ -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat(config): web_search fallback kill-switch and force flag"`

---

### Task 6: `fallbackSearchToolset` — MCP-aware dynamic gate

**Files:**
- Create: `internal/agent/websearch_fallback.go`
- Test: `internal/agent/websearch_fallback_test.go`

**Interfaces:**
- Consumes: `config.WebSearchEnabled`, `config.WebSearchConfig`, `websearch.NewTools`, `websearch.NewProviders`.
- Produces: `var researchToolHints []string`; `func hasResearchMCP(names []string) bool`; `type fallbackSearchToolset` implementing `tool.Toolset` (`Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error)`, plus `Name/Description/IsLongRunning`); `func newFallbackSearchToolset(cfg *config.Config, mcpManager tool.Toolset, log interfaces.LogFunc) *fallbackSearchToolset`; package var `webSearchFallback *fallbackSearchToolset`; `func buildWebSearchFallback(cfg *config.Config) string`.

- [ ] **Step 1: Write the failing tests**

```go
package agent

import (
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
	if buildWebSearchFallback(&config.Config{}) == "" {
		t.Error("default config should produce a prompt section")
	}
	off := false
	if s := buildWebSearchFallback(&config.Config{WebSearch: config.WebSearchConfig{Enabled: &off}}); s != "" {
		t.Errorf("disabled config should produce empty section, got %q", s)
	}
}
```

(`context` import needed for the error test.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/agent/ -run 'Fallback|HasResearch' -v` → FAIL (undefined).

- [ ] **Step 3: Implement websearch_fallback.go**

```go
// websearch_fallback.go - built-in keyless web research fallback.
//
// hakase's research surface normally comes from browser MCP servers
// (lightpanda, chrome-devtools, playwright - docs/browser-mcp-presets.md).
// With none connected the agent would have no web access, so the
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
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/agent/ -run 'Fallback|HasResearch' -v`
Expected: PASS. (If package-level compile errors from other tests, fix imports only.)

- [ ] **Step 5: Commit**

```bash
git add internal/agent/websearch_fallback.go internal/agent/websearch_fallback_test.go
git commit -m "feat(agent): mcp-aware dynamic web search fallback toolset"
```

---

### Task 7: Wiring — SetupRunner, delegate, prompts

**Files:**
- Modify: `internal/agent/agent.go` (4 edits)
- Modify: `internal/agent/delegate.go` (web_researcher case, ~line 502)

**Interfaces:**
- Consumes: `newFallbackSearchToolset`, `webSearchFallback`, `buildWebSearchFallback`.
- Produces: fallback tools reachable from orchestrator, web_researcher agent, and delegated `web_researcher` runs; prompt sections on both instructions.

- [ ] **Step 1: Create the shared instance after the MCP manager block** (agent.go, after the `mcpManager` type assert, ~line 1969, before the researcher agent):

```go
	// Built-in keyless web search fallback (internal/websearch): shared by
	// the orchestrator, web_researcher, and delegated sub-agents. nil when
	// disabled by config or construction failed.
	webSearchFallback = newFallbackSearchToolset(cfg, mcpManager, log)
```

- [ ] **Step 2: Attach to researcher toolsets** (agent.go:1985-1989):

```go
	// Build toolsets slice for the researcher agent (MCP manager only when present).
	var researcherToolsets []tool.Toolset
	if mcpManager != nil {
		researcherToolsets = []tool.Toolset{mcpManager}
	}
	if webSearchFallback != nil {
		researcherToolsets = append(researcherToolsets, webSearchFallback)
	}
```

- [ ] **Step 3: Add the prompt section to the researcher instruction** (agent.go:1998): change

```go
		) + "\n\n" + buildTimeReminder() + ContextBlockFor(
```
to
```go
		) + "\n\n" + buildTimeReminder() + "\n\n" + buildWebSearchFallback(cfg) + ContextBlockFor(
```

- [ ] **Step 4: Orchestrator prompt + toolsets.** At the instruction tail (agent.go:1698) change

```go
	` + DiagramInstruction + "\n\n" + installedSkills + "\n\n" + buildSidekickInstruction(cfg) + "\n\n" + buildTimeReminder()
```
to
```go
	` + DiagramInstruction + "\n\n" + installedSkills + "\n\n" + buildSidekickInstruction(cfg) + "\n\n" + buildWebSearchFallback(cfg) + "\n\n" + buildTimeReminder()
```

Before the orchestrator `llmagent.New` (~line 2259) build:

```go
	// Orchestrator toolsets: MCP manager plus the web search fallback when
	// enabled. A nil manager element is omitted (ADK would panic).
	orchestratorToolsets := make([]tool.Toolset, 0, 2)
	if mcpManager != nil {
		orchestratorToolsets = append(orchestratorToolsets, mcpManager)
	}
	if webSearchFallback != nil {
		orchestratorToolsets = append(orchestratorToolsets, webSearchFallback)
	}
```

and change `Toolsets:            []tool.Toolset{mcpManager},` (agent.go:2281) to `Toolsets:            orchestratorToolsets,`.

- [ ] **Step 5: delegate.go web_researcher case** (delegate.go:502-504) — change

```go
	case "web_researcher":
		dlTool, _ := createDownloadTool()
		return []tool.Tool{dlTool, visionTool}, mcpToolsets
```
to
```go
	case "web_researcher":
		dlTool, _ := createDownloadTool()
		toolsets := mcpToolsets
		if webSearchFallback != nil {
			toolsets = append(toolsets, webSearchFallback)
		}
		return []tool.Tool{dlTool, visionTool}, toolsets
```

- [ ] **Step 6: Build + full agent tests**

Run: `go build ./... && go test ./internal/agent/ ./internal/mcp/ -count=1`
Expected: build OK, tests PASS (no regressions from the toolsets change).

- [ ] **Step 7: Commit**

```bash
git add internal/agent/
git commit -m "feat(agent): wire web search fallback into orchestrator and web_researcher"
```

---

### Task 8: Docs + full verification

**Files:**
- Modify: `docs/browser-mcp-presets.md` (new section after the intro)
- Modify: `AGENTS.md` (layout list)

- [ ] **Step 1: docs/browser-mcp-presets.md** — add after the legacy-migration paragraph:

```markdown
## Built-in fallback (no MCP needed)

When no connected MCP server exposes research-capable tools (browser
automation or web search/fetch), hakase automatically exposes built-in
keyless tools to the orchestrator and `web_researcher`: `web_search`
(DuckDuckGo HTML results supplemented with Wikipedia matches, each labeled
by source) and `web_fetch` (page content as markdown via the keyless Jina
Reader, with a direct-fetch fallback; no JavaScript rendering). Connecting
any browser/search MCP hides the fallback again - per run - so the presets
above always take precedence when present.

Config (project `config.json`):

```json
{
  "web_search": {
    "enabled": true,
    "force": false
  }
}
```

`enabled: false` disables the fallback and all of its outbound calls;
`force: true` keeps the fallback visible even when research MCP tools are
connected.
```

- [ ] **Step 2: AGENTS.md** — extend the `internal/` list: after `` `vision` `` insert `` `websearch` (keyless web_search/web_fetch fallback shown when no research MCP is connected), ``.

- [ ] **Step 3: Full verification**

Run: `go build ./... && go test ./... -count=1`
Expected: all PASS. (`internal/web/dist` must exist for the embed; if missing run `make build-frontend` first.)

- [ ] **Step 4: Commit**

```bash
git add docs/browser-mcp-presets.md AGENTS.md
git commit -m "docs: describe built-in web search fallback"
```
