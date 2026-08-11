//go:build !dev

package web

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

// distFS embeds the built frontend assets from webui/dist.
// A symlink internal/web/dist -> ../../webui/dist is required at build time.
//
//go:embed all:dist
var distFS embed.FS

// getFrontendAssets returns the embedded frontend asset filesystem.
// Production builds use this to serve the SPA from inside the binary.
func getFrontendAssets() http.FileSystem {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Printf("web: failed to sub embedded dist: %v", err)
		return http.Dir(".") // fallback, won't serve anything useful
	}
	return http.FS(sub)
}
