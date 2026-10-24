// r33_test.go - R33 download choke tests (rooted streaming create, no
// outer MkdirAll, truncate + partial unlink preserved).
package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR33DownloadChokeOwnsDirectory pins choke ownership: the downloads
// directory need not exist up front (no outer MkdirAll - the choke
// creates parents), the file lands inside the downloads root, and the
// saved bytes match.
func TestR33DownloadChokeOwnsDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	saved := deps
	deps = &Deps{}
	t.Cleanup(func() { deps = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("r33-payload"))
	}))
	defer srv.Close()
	stubDownloadNet(t, func(host string) error { return nil }, srv.Client())

	if _, err := os.Stat(filepath.Join(dir, "downloads")); !os.IsNotExist(err) {
		t.Fatal("fixture: downloads dir must not exist before the download")
	}
	out, err := runDownload(t, srv.URL+"/f.bin", "f.bin")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(out.SavedPath), filepath.Clean(filepath.Join(dir, "downloads"))) {
		t.Fatalf("saved path must sit under the downloads root, got %q", out.SavedPath)
	}
	got, err := os.ReadFile(out.SavedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "r33-payload" {
		t.Fatalf("saved bytes = %q, want the served payload", got)
	}
	if out.BytesDownloaded != int64(len("r33-payload")) {
		t.Fatalf("byte count = %d, want %d", out.BytesDownloaded, len("r33-payload"))
	}
}

// TestR33DownloadTruncatePreserved pins truncate semantics: a repeated
// download of the same name overwrites (no duplicate, no append) -
// partial files still unlink on over-cap per saveDownloadBody (S19).
func TestR33DownloadTruncatePreserved(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	saved := deps
	deps = &Deps{}
	t.Cleanup(func() { deps = saved })

	body := "second"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	stubDownloadNet(t, func(host string) error { return nil }, srv.Client())

	if _, err := runDownload(t, srv.URL+"/f.bin", "f.bin"); err != nil {
		t.Fatalf("first download: %v", err)
	}
	out, err := runDownload(t, srv.URL+"/f.bin", "f.bin")
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	got, err := os.ReadFile(out.SavedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("same-name re-download must truncate, got %q", got)
	}
}
