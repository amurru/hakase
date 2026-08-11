//go:build dev

package web

import (
	"net/http"
	"os"
)

// getFrontendAssets returns a live filesystem pointing to webui/dist
// so frontend changes are reflected immediately in development.
// Served via os.DirFS -> http.FS by the SPA handler.
func getFrontendAssets() http.FileSystem {
	dir := "webui/dist"
	if _, err := os.Stat(dir); err != nil {
		// Fallback: try relative to current working directory
		dir = "webui/dist"
	}
	return http.Dir(dir)
}
