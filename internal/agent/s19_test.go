// s19_test.go - SSRF-guarded download tests (S19).
package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/vision"

	"google.golang.org/adk/v2/agent"
)

// stubDownloadNet replaces the host check and client builder, restoring on
// cleanup. allowHosts, when non-nil, decides the host check; client, when
// non-nil, replaces the HTTP client.
func stubDownloadNet(t *testing.T, allowHosts func(host string) error, client *http.Client) {
	t.Helper()
	savedCheck, savedNew := downloadHostCheck, newDownloadClient
	if allowHosts != nil {
		downloadHostCheck = allowHosts
	}
	if client != nil {
		newDownloadClient = func() *http.Client { return client }
	}
	t.Cleanup(func() { downloadHostCheck, newDownloadClient = savedCheck, savedNew })
}

// runDownload drives the download_file tool like the ADK would.
func runDownload(t *testing.T, url, filename string) (FileDownloadOutput, error) {
	t.Helper()
	tool, err := createDownloadTool()
	if err != nil {
		t.Fatalf("createDownloadTool: %v", err)
	}
	runnable, ok := tool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", tool)
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	result, err := runnable.Run(ctx, map[string]any{"url": url, "filename": filename})
	if err != nil {
		return FileDownloadOutput{}, err
	}
	out, _ := result["saved_path"].(string)
	var n int64
	switch v := result["bytes_downloaded"].(type) {
	case int64:
		n = v
	case float64:
		n = int64(v)
	case int:
		n = int64(v)
	}
	ct, _ := result["content_type"].(string)
	return FileDownloadOutput{SavedPath: out, BytesDownloaded: n, ContentType: ct}, nil
}

// TestS19DownloadSSRFBlocked pins the SSRF refusal: loopback and metadata
// URLs are refused before any dial, with no file left behind.
func TestS19DownloadSSRFBlocked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("SSRF-blocked server must never be dialed")
	}))
	defer srv.Close()
	for _, raw := range []string{srv.URL + "/x.png", "http://169.254.169.254/latest/meta-data/"} {
		if _, err := runDownload(t, raw, "blocked.png"); err == nil {
			t.Fatalf("SSRF URL %q: want refusal", raw)
		} else if !strings.Contains(err.Error(), "blocked internal address") &&
			!strings.Contains(err.Error(), "download refused") {
			t.Fatalf("SSRF URL %q: refusal must name the guard, got: %v", raw, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("refused downloads must leave no files, found %v", entries)
	}
}

// zeroReader is an unbounded 'a' source for cap tests without big allocs.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// TestS19DownloadSizeCap pins the 50MB cap: an over-cap body errors and the
// partial file is unlinked (no file left).
func TestS19DownloadSizeCap(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "big.bin")
	f, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	// 128 bytes over the cap exercises the boundary without a 51MB copy.
	smallMax := int64(1 << 20)
	_, err = saveDownloadBody(io.LimitReader(zeroReader{}, smallMax+128), f, dst, smallMax)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over-cap body: want cap error, got %v", err)
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("over-cap partial file must be unlinked")
	}
	// At-cap bodies still save.
	dst2 := filepath.Join(dir, "ok.bin")
	f2, err := os.Create(dst2)
	if err != nil {
		t.Fatalf("create dst2: %v", err)
	}
	n, err := saveDownloadBody(io.LimitReader(zeroReader{}, smallMax), f2, dst2, smallMax)
	if err != nil {
		t.Fatalf("at-cap body: %v", err)
	}
	if n != smallMax {
		t.Fatalf("at-cap body: wrote %d, want %d", n, smallMax)
	}
}

// redirectRT answers one 302 to target, failing the test on any second hop.
type redirectRT struct {
	t      *testing.T
	target string
}

func (r redirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/start") {
		return &http.Response{
			Status: "302 Found", StatusCode: 302,
			Header:        http.Header{"Location": []string{r.target}},
			Body:          io.NopCloser(strings.NewReader("")),
			Request:       req,
			ContentLength: 0,
		}, nil
	}
	r.t.Errorf("rebound redirect must be stopped before the second hop (got %s)", req.URL)
	return &http.Response{
		Status: "200 OK", StatusCode: 200,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no")),
		Request: req,
	}, nil
}

// TestS19DownloadRedirectReboundBlocked pins per-hop re-checking: a first
// hop that passes the pre-flight followed by a redirect onto an internal
// host is stopped by the redirect guard before the second dial.
func TestS19DownloadRedirectReboundBlocked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stubDownloadNet(t,
		func(host string) error { return nil }, // pre-flight passes
		&http.Client{
			Transport:     redirectRT{t: t, target: "http://127.0.0.1:9/evil"},
			CheckRedirect: vision.GuardedCheckRedirect,
			Timeout:       5 * time.Second,
		},
	)
	if _, err := runDownload(t, "http://public.test/start", "rebound.png"); err == nil {
		t.Fatal("redirect rebound onto loopback: want blocked")
	} else if !strings.Contains(err.Error(), "redirect target blocked") &&
		!strings.Contains(err.Error(), "blocked internal address") {
		t.Fatalf("rebound refusal must name the guard, got: %v", err)
	}
}

// hangRT blocks until the request context ends (timeout injection probe).
type hangRT struct{}

func (hangRT) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, fmt.Errorf("hangRT: %w", req.Context().Err())
}

// TestS19DownloadTimeout pins the total budget with a short test-local
// timeout: a hanging upstream fails fast instead of hanging the tool.
func TestS19DownloadTimeout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stubDownloadNet(t,
		func(host string) error { return nil },
		&http.Client{
			Transport:     hangRT{},
			CheckRedirect: vision.GuardedCheckRedirect,
			Timeout:       100 * time.Millisecond,
		},
	)
	start := time.Now()
	if _, err := runDownload(t, "http://public.test/hang", "hang.png"); err == nil {
		t.Fatal("hanging download: want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout must fire within budget, took %v", elapsed)
	}
}
