package middleware

import "net/http"

// SecurityHeaders returns a middleware that adds standard security headers
// to all HTTP responses. The Content-Security-Policy is strict: 'self' only
// for scripts, styles, and connections; frame-ancestors 'none' prevents
// embedding; form-action 'self' prevents form hijacking. media-src is 'self'
// data: only (no external media, no frames) per the markdown-rendering Phase 0
// decision - see docs/markdown-rendering/research.md.
//
// In dev mode the Go server does not serve the SPA (Vite serves on :5173),
// so CSP has no effect on the frontend. These headers still protect API
// responses in all environments.
func SecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Strict Content-Security-Policy
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: https:; "+
					"font-src 'self'; "+
					"media-src 'self' data:; "+
					"connect-src 'self'; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")

			// Prevent MIME type sniffing
			w.Header().Set("X-Content-Type-Options", "nosniff")

			// Prevent the page from being embedded in frames
			w.Header().Set("X-Frame-Options", "DENY")

			// Control referrer information sent with cross-origin requests
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

			next.ServeHTTP(w, r)
		})
	}
}
