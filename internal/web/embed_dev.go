//go:build dev

package web

import (
	"net/http"
	"os"
)

// FrontendAssets returns a live filesystem pointing to webui/dist
// so frontend changes are reflected immediately in development.
// Served via os.DirFS -> http.FS by the SPA handler.
func FrontendAssets() http.FileSystem {
	dir := "webui/dist"
	if _, err := os.Stat(dir); err != nil {
		// Fallback: try relative to current working directory
		dir = "webui/dist"
	}
	return http.Dir(dir)
}
