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
	// maxResults == result count so the Wikipedia supplement (whose default
	// endpoint would hit the network) does not fire.
	res, err := p.Search(context.Background(), "golang concurrency", 2)
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

const wikiFixtureJSON = `{"batchcomplete":"","query":{"search":[
 {"ns":0,"title":"Go (programming language)","pageid":25039021,"snippet":"<b>Go</b> is a compiled language"},
 {"ns":0,"title":"Concurrency (computer science)","pageid":1234,"snippet":"Structuring programs with &amp; channels"}]}}`

func testWikiProviders(t *testing.T) *Providers {
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
	return p
}

func TestSearchWikipedia(t *testing.T) {
	p := testWikiProviders(t)
	res, err := p.searchWikipedia(context.Background(), "golang", 3)
	if err != nil {
		t.Fatalf("searchWikipedia: %v", err)
	}
	if len(res) != 2 || res[0].Source != "wikipedia" {
		t.Fatalf("unexpected: %+v", res)
	}
	// Base is derived from the configured (httptest) API URL; parens arrive
	// percent-encoded, which Wikipedia decodes server-side.
	if !strings.HasSuffix(res[0].URL, "/wiki/Go_%28programming_language%29") {
		t.Errorf("article url: %q", res[0].URL)
	}
	if res[1].Snippet != "Structuring programs with & channels" {
		t.Errorf("snippet not stripped/unescaped: %q", res[1].Snippet)
	}
}

func TestSearchFallsBackToWikipediaWhenDDGBlocked(t *testing.T) {
	p := testWikiProviders(t)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer blocked.Close()
	p.DDGSearchURL = blocked.URL + "/"
	res, err := p.Search(context.Background(), "golang", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 || res[0].Source != "wikipedia" {
		t.Fatalf("expected wikipedia fallback, got: %+v", res)
	}
}

func TestSearchWikipediaSupplementsDDG(t *testing.T) {
	p := testWikiProviders(t)
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ddgFixtureHTML)
	}))
	defer ddg.Close()
	p.DDGSearchURL = ddg.URL + "/"
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
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rateLimited.Close()
	p.JinaReaderURL = rateLimited.URL + "/"
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
