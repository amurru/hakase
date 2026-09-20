// oauth.go - OAuth2 client authorization for remote MCP servers (spec
// 2026-07-28): CIMD-first registration, RFC 9207 issuer validation, and
// token persistence under ~/.hakase (0600, flocked).
//
// The go-sdk's auth.AuthorizationCodeHandler does the protocol work; this
// file adapts it to hakase:
//   - config comes from MCPOAuthConfig (env-expanded here, house convention)
//   - AuthorizationCodeFetcher runs a localhost redirect listener (D4: the
//     CLI/TUI/serve default; tests inject a canned fetcher)
//   - NewTokenSource/InitialTokenSource persist and restore tokens so
//     restarts skip re-auth; entries are keyed by server name and record the
//     issuing authorization server (SEP-2352 issuer binding: a new issuer
//     replaces the stored credentials for that server)
package mcp

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// defaultOAuthRedirect is the redirect URL used when the config omits one.
const defaultOAuthRedirect = "http://localhost:8931/callback"

// tokenStorePath returns ~/.hakase/mcp-tokens.json (overridable in tests via
// HAKASE_HOME). Empty when no home is configured.
func tokenStorePath() string {
	home := config.HakaseHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "mcp-tokens.json")
}

// storedToken is one server's persisted credential: the issuing authorization
// server's issuer (SEP-2352 binding) plus the oauth2 token (access, refresh,
// expiry as issued).
type storedToken struct {
	Issuer string        `json:"issuer,omitempty"`
	Token  *oauth2.Token `json:"token"`
}

// tokenFile is the on-disk shape of mcp-tokens.json.
type tokenFile struct {
	Servers map[string]storedToken `json:"servers"`
}

// loadStoredToken reads one server's persisted token. Missing file or entry
// is not an error (nothing persisted yet).
func loadStoredToken(server string) (storedToken, bool, error) {
	path := tokenStorePath()
	if path == "" {
		return storedToken{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return storedToken{}, false, nil
		}
		return storedToken{}, false, err
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return storedToken{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	st, ok := tf.Servers[server]
	return st, ok && st.Token != nil, nil
}

// saveStoredToken persists one server's token: flock, rewrite, 0600. The
// whole file is rewritten because entries are tiny; concurrent hakase
// processes serialize on the lock.
func saveStoredToken(server string, st storedToken) error {
	path := tokenStorePath()
	if path == "" {
		return fmt.Errorf("no hakase home for token persistence")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := util.FlockExclusive(f); err != nil {
		return err
	}
	defer util.FlockUnlock(f)

	var tf tokenFile
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &tf); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	if tf.Servers == nil {
		tf.Servers = map[string]storedToken{}
	}
	tf.Servers[server] = st
	out, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err = f.Write(out)
	return err
}

// staticTokenSource wraps an already-obtained token as a TokenSource. It
// never refreshes on its own: the SDK handler re-authorizes when the server
// answers 401 after expiry.
type staticTokenSource struct{ tok *oauth2.Token }

func (s staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// newAuthorizationHandler builds the SDK handler from raw values; tests
// inject fetcher. Registration is CIMD-first, pre-registered as fallback.
func newAuthorizationHandler(name, cimdURL, clientID, clientSecret, redirect string, httpClient *http.Client, fetcher auth.AuthorizationCodeFetcher, initial oauth2.TokenSource, onToken func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error)) (auth.OAuthHandler, error) {
	if redirect == "" {
		redirect = defaultOAuthRedirect
	}
	handlerCfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirect,
		RequestRefreshToken:      true,
		Client:                   httpClient,
		AuthorizationCodeFetcher: fetcher,
		NewTokenSource:           onToken,
		InitialTokenSource:       initial,
	}
	if cimdURL != "" {
		handlerCfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: cimdURL}
	} else {
		creds := &oauthex.ClientCredentials{ClientID: clientID}
		if clientSecret != "" {
			creds.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: clientSecret}
		}
		handlerCfg.PreregisteredClient = creds
	}
	_ = name
	return auth.NewAuthorizationCodeHandler(handlerCfg)
}

// buildOAuthHandler creates the SDK authorization handler for one server
// from its config block. httpClient (may be nil) is reused for all OAuth
// metadata traffic so timeouts/headers behave like the MCP transport.
// Installed on StreamableClientTransport.OAuthHandler; the transport then
// adds bearer tokens automatically and re-authorizes on 401/403.
func buildOAuthHandler(name string, oauthCfg *config.MCPOAuthConfig, httpClient *http.Client) (auth.OAuthHandler, error) {
	if oauthCfg == nil {
		return nil, fmt.Errorf("mcp server %q: nil oauth config", name)
	}
	cimdURL := config.ExpandEnv(oauthCfg.ClientIDURL)
	clientID := config.ExpandEnv(oauthCfg.ClientID)
	clientSecret := config.ExpandEnv(oauthCfg.ClientSecret)
	redirect := config.ExpandEnv(oauthCfg.RedirectURL)
	if cimdURL == "" && clientID == "" {
		return nil, fmt.Errorf("mcp server %q: oauth needs client_id_url or client_id", name)
	}

	// Restore a persisted token if present so restarts skip the browser.
	var initial oauth2.TokenSource
	if st, ok, err := loadStoredToken(name); err != nil {
		return nil, fmt.Errorf("loading persisted mcp token: %w", err)
	} else if ok {
		initial = staticTokenSource{tok: st.Token}
	}

	return newAuthorizationHandler(name, cimdURL, clientID, clientSecret, redirect, httpClient,
		func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return localhostAuthorizationFlow(ctx, args, redirect)
		},
		initial,
		func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			// Token issuer: the AS serving the token endpoint (SEP-2352
			// binding key). A new issuer replaces the stored entry.
			if err := saveStoredToken(name, storedToken{Issuer: oc.Endpoint.TokenURL, Token: tok}); err != nil {
				return nil, fmt.Errorf("persisting mcp token: %w", err)
			}
			return oc.TokenSource(ctx, tok), nil
		})
}

// callbackResult is what the localhost listener captures from the redirect.
type callbackResult struct {
	code  string
	state string
	iss   string
	err   error
}

// localhostAuthorizationFlow completes the browser leg (D4 CLI/TUI/serve
// default): starts a one-shot listener on the redirect URL's port, opens the
// authorization URL in the desktop browser (best-effort; the URL is always
// printed so it can be opened by hand), waits for the redirect carrying
// code/state/iss (RFC 9207), and tears down.
func localhostAuthorizationFlow(ctx context.Context, args *auth.AuthorizationArgs, redirect string) (*auth.AuthorizationResult, error) {
	u, err := url.Parse(redirect)
	if err != nil {
		return nil, fmt.Errorf("oauth redirect_url: %w", err)
	}
	port := u.Port()
	if port == "" {
		return nil, fmt.Errorf("oauth redirect_url %q needs an explicit localhost port", redirect)
	}
	result := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := callbackResult{code: q.Get("code"), state: q.Get("state"), iss: q.Get("iss")}
		if e := q.Get("error"); e != "" {
			res.err = fmt.Errorf("authorization server error: %s (%s)", e, q.Get("error_description"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, "<html><body><h3>hakase: authorization received</h3>You can close this tab.</body></html>")
		select {
		case result <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return nil, fmt.Errorf("oauth redirect listener on port %s: %w (is another hakase waiting?)", port, err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	fmt.Printf("mcp oauth: open this URL to authorize:\n%s\n", args.URL)
	openBrowser(args.URL)

	const timeout = 5 * time.Minute
	select {
	case res := <-result:
		if res.err != nil {
			return nil, res.err
		}
		return &auth.AuthorizationResult{Code: res.code, State: res.state, Iss: res.iss}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("oauth authorization canceled: %w", ctx.Err())
	case <-time.After(timeout):
		return nil, fmt.Errorf("oauth authorization timed out after %s", timeout)
	}
}

// openBrowser tries the platform opener; failures are non-fatal (the URL is
// printed for manual opening).
func openBrowser(target string) {
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"open", target}
	case "windows":
		argv = []string{"rundll32", "url.dll,FileProtocolHandler", target}
	default:
		argv = []string{"xdg-open", target}
	}
	_ = exec.Command(argv[0], argv[1:]...).Start()
}

// oauthHandlers caches one handler per server so repeated toolset rebuilds
// (reload/Reconnect) reuse the in-memory token source instead of re-reading
// the store on every connect. Fingerprints detect config changes: a server
// whose OAuth block changed gets a fresh handler.
var oauthHandlers = struct {
	sync.Mutex
	m            map[string]auth.OAuthHandler
	fingerprints map[string]string
}{m: map[string]auth.OAuthHandler{}, fingerprints: map[string]string{}}

// dropStaleOAuthHandlers evicts cached handlers for servers that disappeared
// or whose OAuth config changed. Called from reload().
func dropStaleOAuthHandlers(want map[string]string) {
	oauthHandlers.Lock()
	defer oauthHandlers.Unlock()
	for name := range oauthHandlers.m {
		if want[name] == "" {
			delete(oauthHandlers.m, name)
			delete(oauthHandlers.fingerprints, name)
		}
	}
	for name, fp := range oauthHandlers.fingerprints {
		if want[name] != fp {
			delete(oauthHandlers.m, name)
			delete(oauthHandlers.fingerprints, name)
		}
	}
}

// oauthHandlerFor returns the cached handler for a server, building it on
// first use. httpTimeout bounds metadata traffic like the MCP transport.
func oauthHandlerFor(name string, oauthCfg *config.MCPOAuthConfig, httpTimeout time.Duration) (auth.OAuthHandler, error) {
	oauthHandlers.Lock()
	defer oauthHandlers.Unlock()
	fp := oauthCfg.ClientIDURL + "|" + oauthCfg.ClientID + "|" + oauthCfg.RedirectURL
	if h, ok := oauthHandlers.m[name]; ok && oauthHandlers.fingerprints[name] == fp {
		return h, nil
	}
	var client *http.Client
	if httpTimeout > 0 {
		client = &http.Client{Timeout: httpTimeout}
	}
	h, err := buildOAuthHandler(name, oauthCfg, client)
	if err != nil {
		return nil, err
	}
	oauthHandlers.m[name] = h
	oauthHandlers.fingerprints[name] = fp
	return h, nil
}
