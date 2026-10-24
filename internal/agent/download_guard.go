// download_guard.go - S19 download seams.
//
// The SSRF-guarded client factory lives in the vision leaf package
// (internal/vision/download.go, ADR condition 2: no agent imports, so the
// media proxy and file handlers share it). These vars are the agent-side
// seams: tests override the host check to simulate a public first hop and
// the client builder to inject a short timeout without touching the 30s
// production budget.
package agent

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"amurru/hakase/internal/vision"
)

// saveDownloadBody streams body to dst through a size cap (S19). Over-cap
// bodies fail with "download exceeds 50MB limit" and the partial file at
// filePath is unlinked (never left behind). dst is closed by this helper
// on every path.
func saveDownloadBody(body io.Reader, dst *os.File, filePath string, maxBytes int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(body, maxBytes+1))
	dst.Close()
	if err != nil {
		return 0, fmt.Errorf("failed to save file contents: %w", err)
	}
	if n > maxBytes {
		// Over-cap: unlink the partial file, never leave it.
		_ = os.Remove(filePath)
		return 0, fmt.Errorf("download exceeds 50MB limit")
	}
	return n, nil
}

// downloadHostCheck pre-flights the initial URL host (fail fast) and
// re-checks every redirect hop via the shared redirect guard. The dialer
// itself re-validates at connect time (GuardedDialContext), so check-time
// and dial-time cannot be split by rebinding.
var downloadHostCheck = vision.CheckHostPublic

// newDownloadClient builds the per-call guarded client (30s total budget).
var newDownloadClient = func() *http.Client {
	return vision.NewGuardedClient(vision.DownloadTimeout)
}
