package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
