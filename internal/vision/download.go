// download.go - SSRF-guarded HTTP client leaf factory (S19).
//
// NewGuardedClient is the single choke for agent-driven URL downloads: a
// 30s total timeout, a dialer that validates every resolved address at
// connect time (GuardedDialContext, S21), and a redirect check that
// re-runs the full-set CheckHostPublic per hop with a 5-hop cap.
//
// Leaf constraint (ADR condition 2): this file imports no agent, sandbox
// policy, or flow state, so internal/web/handlers/file.go and the media
// path can share it without an import cycle. The byte cap lives here next
// to the client so every consumer enforces the same limit.
package vision

import (
	"fmt"
	"net/http"
	"time"
)

const (
	// DownloadTimeout bounds one guarded download end to end.
	DownloadTimeout = 30 * time.Second
	// DownloadMaxBytes caps a guarded download body (50MB).
	DownloadMaxBytes = 50 << 20
	// downloadMaxRedirects caps redirect hops; every hop is re-checked.
	downloadMaxRedirects = 5
)

// GuardedCheckRedirect is the shared redirect guard: at most 5 hops, and
// every hop's host passes the all-addrs CheckHostPublic (S21), so a
// redirect can never rebind onto an internal address.
func GuardedCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= downloadMaxRedirects {
		return fmt.Errorf("too many redirects")
	}
	if err := CheckHostPublic(req.URL.Host); err != nil {
		return fmt.Errorf("redirect target blocked: %w", err)
	}
	return nil
}

// NewGuardedClient builds the SSRF-guarded download client: Timeout is the
// total budget, the transport dials only through GuardedDialContext, and
// redirects re-check every hop. Callers still enforce DownloadMaxBytes on
// the body (LimitReader) and unlink over-cap partial files themselves.
func NewGuardedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           GuardedDialContext,
			MaxIdleConns:          8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
		},
		CheckRedirect: GuardedCheckRedirect,
	}
}
