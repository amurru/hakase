package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

// Fixture token material for the mock OAuth server. Synthetic values - the
// server only checks code equality and the transport only echoes them back.
// Fixture token material for the mock OAuth server. Synthetic values - the
// server only checks code equality and the transport only echoes them back.
const (
	fixtureAccessToken = "tok123"
	fixtureRefreshed   = "tok-refreshed-next"
	fixtureAuthCode    = "good-code"
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
			if f.lastAuth != "Bearer "+fixtureAccessToken {
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
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.FormValue("grant_type") == "refresh_token" {
				f.mu.Lock()
				f.tokenRequests++
				f.mu.Unlock()
				writeJSONBytes(w, map[string]any{
					"access_token": fixtureRefreshed,
					"token_type":   "Bearer",
					"expires_in":   3600,
				})
				return
			}
			if r.FormValue("code") != fixtureAuthCode {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.tokenRequests++
			f.mu.Unlock()
			writeJSONBytes(w, map[string]any{
				"access_token": fixtureAccessToken,
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
			Code:  fixtureAuthCode,
			State: u.Query().Get("state"),
			Iss:   issuer,
		}, nil
	}
}

// TestOAuthEndToEnd: a 401-challenged MCP server authorizes through a mock
// AS (preregistered client, canned code fetch) and the tool call completes.
func TestOAuthEndToEnd(t *testing.T) {
	f := newOAuthFixture(t)
	handler, err := newAuthorizationHandler("asrv", "", "client-1", "", "http://localhost:8931/callback", nil, nil,
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

	handler, err := buildOAuthHandler("psrv", "", &config.MCPOAuthConfig{
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
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("token store mode = %v, want 0600", info.Mode().Perm())
		}
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
	h1, err := oauthHandlerFor("cache", "https://mcp.example/mcp", cfgA, 0)
	if err != nil {
		t.Fatalf("oauthHandlerFor: %v", err)
	}
	h2, err := oauthHandlerFor("cache", "https://mcp.example/mcp", cfgA, 0)
	if err != nil {
		t.Fatalf("oauthHandlerFor cached: %v", err)
	}
	if h1 != h2 {
		t.Fatal("same config should reuse the cached handler")
	}
	dropStaleOAuthHandlers(map[string]string{"cache": "b"})
	if _, err := oauthHandlerFor("cache", "https://mcp.example/mcp", cfgB, 0); err != nil {
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

// TestOAuthScopesMergedIntoAuthURL: configured scopes reach the authorization
// URL alongside the SDK-derived ones.
func TestOAuthScopesMergedIntoAuthURL(t *testing.T) {
	f := newOAuthFixture(t)
	var authURL string
	fetcher := func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		authURL = args.URL
		return cannedFetcher(f.ts.URL)(context.Background(), args)
	}
	handler, err := newAuthorizationHandler("scopes-srv", "", "client-1", "", "http://localhost:8931/callback",
		[]string{"custom:scope"}, nil, fetcher, nil,
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

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("captured auth URL %q: %v", authURL, err)
	}
	got := strings.Fields(u.Query().Get("scope"))
	want := map[string]bool{"mcp:read": true, "custom:scope": true}
	if len(got) != len(want) {
		t.Fatalf("scope param = %v, want exactly %v", got, want)
	}
	for _, s := range got {
		if !want[s] {
			t.Fatalf("scope param = %v, want exactly %v", got, want)
		}
	}
}

// TestOAuthRestoreBoundToResource: a persisted token is only restored when
// it was issued for the same MCP resource; a URL change forces fresh
// authorization instead of sending the old bearer token to the new endpoint.
func TestOAuthRestoreBoundToResource(t *testing.T) {
	t.Setenv("HAKASE_HOME", t.TempDir())
	tok := &oauth2.Token{
		AccessToken: "persisted",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}
	if err := saveStoredToken("bsrv", storedToken{
		Resource: "https://old.example/mcp",
		Issuer:   "https://as.example.com/token",
		Token:    tok,
	}); err != nil {
		t.Fatalf("saveStoredToken: %v", err)
	}
	cfg := &config.MCPOAuthConfig{ClientID: "client-1"}

	// Different resource: nothing restored - the SDK will authorize fresh.
	handler, err := buildOAuthHandler("bsrv", "https://new.example/mcp", cfg, nil)
	if err != nil {
		t.Fatalf("buildOAuthHandler: %v", err)
	}
	if tsrc, err := handler.TokenSource(context.Background()); err != nil || tsrc != nil {
		t.Fatalf("mismatched resource must restore nothing, got tsrc=%v err=%v", tsrc, err)
	}

	// Same resource: the stored token comes back.
	handler, err = buildOAuthHandler("bsrv", "https://old.example/mcp", cfg, nil)
	if err != nil {
		t.Fatalf("buildOAuthHandler: %v", err)
	}
	tsrc, err := handler.TokenSource(context.Background())
	if err != nil || tsrc == nil {
		t.Fatalf("matching resource must restore the token, got tsrc=%v err=%v", tsrc, err)
	}
	got, err := tsrc.Token()
	if err != nil || got.AccessToken != "persisted" {
		t.Fatalf("restored token = %+v err=%v, want persisted", got, err)
	}
}

// TestOAuthRestoreRefreshesAndPersists: a stored token whose access token is
// expired refreshes through the stored token endpoint, and the refreshed
// token lands back in the store.
func TestOAuthRestoreRefreshesAndPersists(t *testing.T) {
	f := newOAuthFixture(t)
	t.Setenv("HAKASE_HOME", t.TempDir())
	if err := saveStoredToken("rsrv", storedToken{
		Resource: f.ts.URL + "/mcp",
		Issuer:   f.ts.URL + "/token",
		Token: &oauth2.Token{
			AccessToken:  "expired",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(-time.Hour), // forces a refresh on first Token()
			RefreshToken: "rt",
		},
	}); err != nil {
		t.Fatalf("saveStoredToken: %v", err)
	}

	handler, err := buildOAuthHandler("rsrv", f.ts.URL+"/mcp", &config.MCPOAuthConfig{ClientID: "client-1"}, f.ts.Client())
	if err != nil {
		t.Fatalf("buildOAuthHandler: %v", err)
	}
	tsrc, err := handler.TokenSource(context.Background())
	if err != nil || tsrc == nil {
		t.Fatalf("TokenSource: %v %v", tsrc, err)
	}
	got, err := tsrc.Token()
	if err != nil {
		t.Fatalf("token after refresh: %v", err)
	}
	if got.AccessToken != fixtureRefreshed {
		t.Fatalf("access token = %q, want refreshed %q", got.AccessToken, fixtureRefreshed)
	}

	// The refresh must have been persisted.
	st, ok, err := loadStoredToken("rsrv")
	if err != nil || !ok {
		t.Fatalf("loadStoredToken: ok=%v err=%v", ok, err)
	}
	if st.Token.AccessToken != fixtureRefreshed {
		t.Fatalf("persisted access token = %q, want refreshed %q", st.Token.AccessToken, fixtureRefreshed)
	}
}

// TestOpenBrowserSchemeGuard pins openBrowser's contract end to end: a fake
// opener binary on PATH records every dispatch, so the test asserts that
// non-web schemes (file:, custom handlers) never reach the OS opener while
// an https URL is dispatched exactly once. No production seam needed.
func TestOpenBrowserSchemeGuard(t *testing.T) {
	bin, script := "xdg-open", "#!/bin/sh\necho \"$@\" >> %s\n"
	if runtime.GOOS == "windows" {
		// exec.Command("rundll32", ...) resolves rundll32.bat via PATHEXT.
		bin, script = "rundll32.bat", "@echo %* >> %s\r\n"
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.txt")
	stub := filepath.Join(dir, bin)
	script = strings.ReplaceAll(script, "%s", calls)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write opener stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	openBrowser("file:///etc/passwd")
	openBrowser("custom://handler")
	// The opener is async (Start returns before the stub writes), so give a
	// broken guard time to (wrongly) dispatch before asserting absence.
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("non-web schemes reached the OS opener (calls file: %v)", err)
	}

	openBrowser("https://example.com/auth")
	var data []byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		d, err := os.ReadFile(calls)
		if err == nil {
			data = d
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("https dispatch not recorded within 2s: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(string(data), "https://example.com/auth") {
		t.Fatalf("opener argv = %q, want it to carry the URL", string(data))
	}
}
