package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/config"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// oauthFixture is a single httptest server playing three roles: an
// OAuth-protected stateless MCP server, the protected resource metadata
// endpoint, and the authorization server (metadata + token endpoint). The
// authorization code leg is canned by the test's fetcher.
type oauthFixture struct {
	ts            *httptest.Server
	tokenRequests int
	lastAuth      string
	hits          int
	mu            sync.Mutex
}

func (f *oauthFixture) tokenHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenRequests
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := &oauthFixture{}
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "oauthsrv", Version: "0"}, nil)
	mcpSrv.AddTool(&mcp.Tool{Name: "whoami", InputSchema: &jsonschema.Schema{Type: "object"}},
		func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "it-me"}}}, nil
		})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	f.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mcp":
			f.mu.Lock()
			f.lastAuth = r.Header.Get("Authorization")
			f.hits++
			f.mu.Unlock()
			if f.lastAuth != "Bearer tok123" {
				w.Header().Set("WWW-Authenticate",
					`Bearer resource_metadata="`+f.ts.URL+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		case r.URL.Path == "/.well-known/oauth-protected-resource":
			writeJSONBytes(w, map[string]any{
				"resource":              f.ts.URL + "/mcp",
				"authorization_servers": []string{f.ts.URL},
				"scopes_supported":      []string{"mcp:read"},
			})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			writeJSONBytes(w, map[string]any{
				"issuer":                                         f.ts.URL,
				"authorization_endpoint":                         f.ts.URL + "/authorize",
				"token_endpoint":                                 f.ts.URL + "/token",
				"response_types_supported":                       []string{"code"},
				"code_challenge_methods_supported":               []string{"S256"},
				"token_endpoint_auth_methods_supported":          []string{"none"},
				"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
				"authorization_response_iss_parameter_supported": true,
			})
		case r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil || r.FormValue("code") != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.tokenRequests++
			f.mu.Unlock()
			writeJSONBytes(w, map[string]any{
				"access_token": "tok123",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.ts.Close)
	return f
}

func writeJSONBytes(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// cannedFetcher returns an AuthorizationCodeFetcher that answers with a
// fixed code, echoing state and iss the way the browser redirect would.
func cannedFetcher(issuer string) auth.AuthorizationCodeFetcher {
	return func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		u, err := url.Parse(args.URL)
		if err != nil {
			return nil, err
		}
		return &auth.AuthorizationResult{
			Code:  "good-code",
			State: u.Query().Get("state"),
			Iss:   issuer,
		}, nil
	}
}

// TestOAuthEndToEnd: a 401-challenged MCP server authorizes through a mock
// AS (preregistered client, canned code fetch) and the tool call completes.
func TestOAuthEndToEnd(t *testing.T) {
	f := newOAuthFixture(t)
	handler, err := newAuthorizationHandler("asrv", "", "client-1", "", "http://localhost:8931/callback", nil,
		cannedFetcher(f.ts.URL), nil,
		func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return oc.TokenSource(ctx, tok), nil
		})
	if err != nil {
		t.Fatalf("newAuthorizationHandler: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "hakase", Version: "t"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:     f.ts.URL + "/mcp",
			OAuthHandler: handler,
		}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("tool result = %+v", res)
	}
	if got := f.tokenHits(); got != 1 {
		t.Fatalf("token endpoint hits = %d, want 1", got)
	}
	f.mu.Lock()
	t.Logf("mcp hits=%d lastAuth=%q", f.hits, f.lastAuth)
	f.mu.Unlock()
}

// TestOAuthTokenPersistence: a token written by NewTokenSource is restored
// by buildOAuthHandler on the next construction, and the fetcher (browser
// leg) is never invoked.
func TestOAuthTokenPersistence(t *testing.T) {
	t.Setenv("HAKASE_HOME", t.TempDir())
	if err := saveStoredToken("psrv", storedToken{
		Issuer: "https://as.example.com/token",
		Token: &oauth2.Token{
			AccessToken: "persisted",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(time.Hour),
		},
	}); err != nil {
		t.Fatalf("saveStoredToken: %v", err)
	}

	handler, err := buildOAuthHandler("psrv", &config.MCPOAuthConfig{
		ClientID:    "client-1",
		RedirectURL: "http://localhost:8931/callback",
	}, nil)
	if err != nil {
		t.Fatalf("buildOAuthHandler: %v", err)
	}
	tsrc, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if tsrc == nil {
		t.Fatal("TokenSource = nil, want restored token")
	}
	tok, err := tsrc.Token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.AccessToken != "persisted" {
		t.Fatalf("restored token = %q, want persisted", tok.AccessToken)
	}

	// The store itself: 0600, JSON round-trip.
	path := tokenStorePath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token store missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token store mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "persisted") {
		t.Fatalf("token store content = %q err=%v", string(data), err)
	}
	if filepath.Base(path) != "mcp-tokens.json" {
		t.Fatalf("store path = %s", path)
	}
}

// TestOAuthHandlerCacheInvalidation: changing a server's OAuth config gets a
// fresh handler; removing it drops the cache entry.
func TestOAuthHandlerCacheInvalidation(t *testing.T) {
	cfgA := &config.MCPOAuthConfig{ClientID: "a"}
	cfgB := &config.MCPOAuthConfig{ClientID: "b"}
	h1, err := oauthHandlerFor("cache", cfgA, 0)
	if err != nil {
		t.Fatalf("oauthHandlerFor: %v", err)
	}
	h2, err := oauthHandlerFor("cache", cfgA, 0)
	if err != nil {
		t.Fatalf("oauthHandlerFor cached: %v", err)
	}
	if h1 != h2 {
		t.Fatal("same config should reuse the cached handler")
	}
	dropStaleOAuthHandlers(map[string]string{"cache": "b"})
	if _, err := oauthHandlerFor("cache", cfgB, 0); err != nil {
		t.Fatalf("oauthHandlerFor after drop: %v", err)
	}
	dropStaleOAuthHandlers(nil)
	oauthHandlers.Lock()
	_, exists := oauthHandlers.m["cache"]
	oauthHandlers.Unlock()
	if exists {
		t.Fatal("handler survived full eviction")
	}
}
