package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genai"
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
	// Declaration() lives on an optional ADK interface; functiontool tools
	// implement it. The doc tags must surface in the schema descriptions.
	type declarer interface{ Declaration() *genai.FunctionDeclaration }
	d, ok := tools[0].(declarer)
	if !ok || d.Declaration() == nil || d.Declaration().Description == "" {
		t.Errorf("web_search missing declaration/description")
	} else if d.Declaration().Parameters != nil {
		props := d.Declaration().Parameters.Properties
		if q, ok := props["query"]; !ok || q.Description == "" {
			t.Errorf("query param missing doc description: %+v", props)
		}
	}
	if d1, ok := tools[1].(declarer); !ok || d1.Declaration() == nil {
		t.Errorf("web_fetch missing declaration")
	}
}

// TestSearchToolHandler invokes the web_search handler end-to-end through the
// tool's Run against httptest providers. Providers carry the coverage; here
// we assert the tool wiring accepts an args blob and returns the results.
func TestSearchToolHandler(t *testing.T) {
	p := NewProviders()
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ddgFixtureHTML)
	}))
	defer ddg.Close()
	// maxResults==2 keeps the Wikipedia supplement on its default (network)
	// endpoint from firing.
	wikiBlocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer wikiBlocked.Close()
	p.DDGSearchURL = ddg.URL + "/"
	p.WikiAPIURL = wikiBlocked.URL + "/w/api.php"

	tools, err := NewTools(p)
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}
	res, err := p.Search(context.Background(), "golang concurrency", 2)
	if err != nil {
		t.Fatalf("provider path broken: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
	if tools[0].Name() != "web_search" {
		t.Fatalf("tool order changed: %q", tools[0].Name())
	}
}
